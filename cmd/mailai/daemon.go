package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/buildinfo"
	"github.com/nadavushka-dev/mailai/internal/config"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/identity"
	"github.com/nadavushka-dev/mailai/internal/link"
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
	name := cfg.MachineName()
	st, err := store.Load(store.Options{MachineID: name}, db)
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
	if cfg.RequireTerminal() {
		placer.TTY = procinfo.Kernel{}
	}
	opts := daemon.Options{Store: st, Log: logger, Stash: stash.New(p.Stash()), Tokens: tk, Placer: placer,
		Adapter: ad, Wake: cfg.Wake(), Name: name, MachinesFile: p.Machines()}
	if target := cfg.Fleet.Inbox; target != "" {
		remote := cfg.Fleet.Command
		opts.Inbox = target
		opts.Dial = func(ctx context.Context) (io.ReadWriteCloser, error) { return link.Dial(ctx, target, remote) }
	}
	d := daemon.New(opts)
	role := "inbox machine"
	if cfg.Fleet.Inbox != "" {
		role = "spoke of " + cfg.Fleet.Inbox
	}
	logger.Printf("machine %s (%s), adapter %s, wake %v", name, role, ad.Name(), cfg.Wake())
	if err := d.Listen(p.Socket()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("%s listening on %s", buildinfo.Version, p.Socket())
	return d.Serve(ctx)
}

// cmdBridge is what a spoke runs over ssh (SPEC §13a): it copies the ssh
// session's stdio to this machine's daemon socket. Nothing listens on the
// network.
func cmdBridge(args []string) error {
	parse(newFlags("bridge"), args)
	p, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	return link.Bridge(os.Stdin, os.Stdout, p.Socket())
}

// chooseAdapter picks by config, else by whether this daemon runs under
// Herdr (HERDR_ENV=1 or a socket path), else none (SPEC §8).
func chooseAdapter(cfg config.Config) adapter.Adapter {
	name := cfg.Adapter
	if env := os.Getenv("MAILAI_ADAPTER"); env != "" {
		name = env
	}
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
