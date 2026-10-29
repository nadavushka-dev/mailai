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
	"github.com/nadavushka-dev/mailai/internal/store"
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
	logger := log.New(os.Stderr, "mailai ", log.LstdFlags)
	d := daemon.New(daemon.Options{Store: st, Log: logger})
	if err := d.Listen(p.Socket()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("%s listening on %s", buildinfo.Version, p.Socket())
	return d.Serve(ctx)
}
