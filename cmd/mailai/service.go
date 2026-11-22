package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nadavushka-dev/mailai/internal/config"
	"github.com/nadavushka-dev/mailai/internal/service"
)

const serviceUsage = "usage: mailai service install|uninstall|status"

// cmdService keeps the daemon running at login and after a crash: a
// launchd agent on macOS, a systemd user unit on Linux (SPEC §4).
func cmdService(args []string) error {
	if len(args) == 0 {
		return errors.New(serviceUsage)
	}
	sub := args[0]
	if rest := parse(newFlags("service "+sub), args[1:]); len(rest) > 0 {
		return errors.New(serviceUsage)
	}
	if sub != "install" && sub != "uninstall" && sub != "status" {
		return errors.New(serviceUsage)
	}
	p, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	o, err := service.Defaults(p.Log())
	if err != nil {
		return err
	}
	return runService(sub, o, os.Stdout)
}

func runService(sub string, o service.Options, w io.Writer) error {
	switch sub {
	case "install":
		if err := service.Install(o); err != nil {
			return err
		}
		if !jsonOut {
			fmt.Fprintf(w, "installed %s (%s): the daemon runs now, at every login and after a crash; log: %s\n",
				service.File(o), service.Manager(o), o.LogPath)
			return nil
		}
	case "uninstall":
		if err := service.Uninstall(o); err != nil {
			return err
		}
		if !jsonOut {
			fmt.Fprintf(w, "removed %s: the daemon is stopped and no longer starts by itself\n", service.File(o))
			return nil
		}
	case "status":
	default:
		return errors.New(serviceUsage)
	}
	s, err := service.Status(o)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(w, s)
	}
	switch {
	case !s.Installed:
		fmt.Fprintf(w, "not installed (%s): %s\n", s.Manager, s.Path)
	default:
		loaded, running := "loaded", "running"
		if !s.Loaded {
			loaded = "not loaded"
		}
		if !s.Running {
			running = "not running"
		}
		fmt.Fprintf(w, "installed (%s), %s, %s: %s\nlog: %s\n", s.Manager, loaded, running, s.Path, s.Log)
	}
	return nil
}
