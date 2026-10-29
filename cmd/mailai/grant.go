package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

func cmdGrant(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mailai grant <principal> <rights> [--scope S] [--once] | grant ls | grant rm <id>")
	}
	switch args[0] {
	case "ls", "list":
		fs := newFlags("grant ls")
		parse(fs, args[1:])
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()
		var r daemon.GrantListResult
		if err := c.Call("grant-list", nil, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		if len(r.Grants) == 0 {
			fmt.Println("no grants")
		}
		for _, g := range r.Grants {
			extra := ""
			if g.Parent != 0 {
				extra = fmt.Sprintf("  delegated from #%d", g.Parent)
			}
			if g.Once {
				extra += "  once"
			}
			fmt.Printf("#%d  %-14s %-22s over %-16s by %s%s\n", g.ID, g.Principal, joinRights(g.Rights), g.Scope, g.GrantedBy, extra)
		}
		return nil
	case "rm":
		fs := newFlags("grant rm")
		rest := parse(fs, args[1:])
		if len(rest) != 1 {
			return fmt.Errorf("usage: mailai grant rm <id>")
		}
		id, err := strconv.ParseUint(strings.TrimPrefix(rest[0], "#"), 10, 64)
		if err != nil {
			return fmt.Errorf("grant id must be a number")
		}
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()
		var r map[string]int
		if err := c.Call("grant-rm", daemon.GrantRmArgs{ID: id}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Printf("removed %d grant(s)\n", r["removed"])
		return nil
	}
	fs := newFlags("grant")
	scope := fs.String("scope", "all", "all, session:<name>, or an agent's address")
	once := fs.Bool("once", false, "the grant is spent by its first answer")
	rest := parse(fs, args)
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mailai grant <principal> <rights> [--scope S] [--once]")
		os.Exit(2)
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var g store.Grant
	if err := c.Call("grant", daemon.GrantArgs{Principal: store.Address(rest[0]), Rights: rest[1], Scope: *scope, Once: *once}, &g); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(g)
	}
	fmt.Printf("granted #%d: %s may %s over %s\n", g.ID, g.Principal, joinRights(g.Rights), g.Scope)
	return nil
}

func joinRights(rs []store.Right) string {
	s := make([]string, len(rs))
	for i, r := range rs {
		s[i] = string(r)
	}
	return strings.Join(s, ",")
}
