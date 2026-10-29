package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/buildinfo"
	"github.com/nadavushka-dev/mailai/internal/config"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/identity"
	"github.com/nadavushka-dev/mailai/internal/procinfo"
	"github.com/nadavushka-dev/mailai/internal/stash"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
)

func cmdDaemon(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return fmt.Errorf("usage: mailai daemon run")
	}
	fs := newFlags("daemon run")
	fs.Parse(args[1:])
	p, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(p.ConfigFile())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Data, 0o700); err != nil {
		return err
	}
	db, err := store.OpenSQLite(p.StoreDB())
	if err != nil {
		return err
	}
	defer db.Close()
	st, err := store.Load(store.Options{}, db)
	if err != nil {
		return err
	}
	tk, err := tokens.Open(p.Tokens(), nil)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "mailai ", log.LstdFlags)
	ad := chooseAdapter(cfg)
	placer := &identity.Placer{Proc: procinfo.Kernel{}, Tokens: tk, Panes: ad, SelfPID: os.Getpid()}
	d := daemon.New(daemon.Options{Store: st, Log: logger, Stash: stash.New(p.Stash()), Tokens: tk, Placer: placer,
		Adapter: ad, Wake: cfg.Wake()})
	logger.Printf("adapter %s, wake %v", ad.Name(), cfg.Wake())
	if err := d.Listen(p.Socket()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("%s listening on %s", buildinfo.Version, p.Socket())
	return d.Serve(ctx)
}

// chooseAdapter picks by config, else by whether this daemon runs under
// Herdr (HERDR_ENV=1 or a socket path), else none (SPEC §8).
func chooseAdapter(cfg config.Config) adapter.Adapter {
	name := cfg.Adapter
	if name == "" {
		if os.Getenv("HERDR_ENV") == "1" || os.Getenv("HERDR_SOCKET_PATH") != "" {
			name = "herdr"
		} else {
			name = "none"
		}
	}
	switch name {
	case "herdr":
		return &adapter.Herdr{Run: adapter.ExecRunner{}, Proc: procinfo.Kernel{}}
	default:
		return adapter.None{}
	}
}
