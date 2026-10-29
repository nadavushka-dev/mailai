package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

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
	if _, err := config.Load(p.ConfigFile()); err != nil {
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
	// Panes come from the adapter (milestone 3); until then nothing is a pane.
	placer := &identity.Placer{Proc: procinfo.Kernel{}, Tokens: tk, SelfPID: os.Getpid()}
	d := daemon.New(daemon.Options{Store: st, Log: logger, Stash: stash.New(p.Stash()), Tokens: tk, Placer: placer})
	if err := d.Listen(p.Socket()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("%s listening on %s", buildinfo.Version, p.Socket())
	return d.Serve(ctx)
}
