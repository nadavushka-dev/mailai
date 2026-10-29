package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/stash"
)

func cmdStash(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mailai stash put <file> | get <stored> [dest] | list")
	}
	sub, args := args[0], args[1:]
	fs := newFlags("stash " + sub)
	session := fs.String("session", "", "session the file belongs to")
	rest := parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch sub {
	case "put":
		if len(rest) != 1 {
			return fmt.Errorf("usage: mailai stash put <file>")
		}
		p, err := filepath.Abs(rest[0])
		if err != nil {
			return err
		}
		var r daemon.StashPutResult
		if err := c.Call("stash-put", daemon.StashPutArgs{Session: *session, Path: p}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Println(r.Stored)
	case "get":
		if len(rest) < 1 || len(rest) > 2 {
			return fmt.Errorf("usage: mailai stash get <stored> [dest]")
		}
		dest := filepath.Base(rest[0])
		if len(rest) == 2 {
			dest = rest[1]
		}
		dest, err := filepath.Abs(dest)
		if err != nil {
			return err
		}
		var r map[string]string
		if err := c.Call("stash-get", daemon.StashGetArgs{Stored: rest[0], Dest: dest}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Println(r["dest"])
	case "list", "ls":
		var r struct {
			Files []stash.Entry `json:"files"`
		}
		if err := c.Call("stash-list", daemon.StashListArgs{Session: *session}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		if len(r.Files) == 0 {
			fmt.Println("nothing stashed")
		}
		for _, f := range r.Files {
			fmt.Printf("%s\t%d\t%s\n", f.Name, f.Bytes, f.Path)
		}
	default:
		fmt.Fprintf(os.Stderr, "mailai stash: unknown subcommand %q\n", sub)
		os.Exit(2)
	}
	return nil
}
