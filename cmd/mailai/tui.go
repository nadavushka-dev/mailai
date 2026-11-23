package main

import (
	"os"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/config"
	"github.com/nadavushka-dev/mailai/internal/tui"
)

func cmdTUI(args []string) error {
	parse(newFlags("tui"), args)
	p, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	// The TUI outlives a daemon restart: it dials the same socket again.
	sock, token := p.Socket(), os.Getenv("MAILAI_TOKEN")
	dial := func() (*client.Client, error) { return client.Dial(sock, token) }
	c, err := dial()
	if err != nil {
		return err
	}
	l := tui.NewLive(c, dial)
	defer l.Close()
	return tui.Run(l)
}
