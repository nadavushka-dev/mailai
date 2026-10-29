package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

func cmdAgents(args []string) error {
	fs := newFlags("agents")
	parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r daemon.AgentsResult
	if err := c.Call("agents", nil, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Printf("adapter: %s\n", r.Adapter)
	if len(r.Agents) == 0 {
		fmt.Println("no agents known")
		return nil
	}
	for _, a := range r.Agents {
		fmt.Printf("%-16s %-8s %-10s %-6s %s\n", a.Principal, a.State, a.Harness, a.Session, a.Label)
	}
	return nil
}

func cmdQueue(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mailai queue <to> <text> | queue ls | queue rm <id>|--to <agent>")
	}
	switch args[0] {
	case "ls", "list":
		fs := newFlags("queue ls")
		parse(fs, args[1:])
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()
		var r daemon.QueueListResult
		if err := c.Call("queue-list", nil, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		if len(r.Queued) == 0 {
			fmt.Println("nothing queued")
		}
		for _, q := range r.Queued {
			fmt.Printf("#%d  to %-14s by %-10s %s\n", q.ID, q.To, q.By, firstLine(q.Text))
		}
		return nil
	case "rm":
		fs := newFlags("queue rm")
		to := fs.String("to", "", "drop everything queued for this agent")
		rest := parse(fs, args[1:])
		var id uint64
		if len(rest) == 1 {
			id, _ = strconv.ParseUint(strings.TrimPrefix(rest[0], "#"), 10, 64)
		}
		if id == 0 && *to == "" {
			return fmt.Errorf("usage: mailai queue rm <id> | --to <agent>")
		}
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()
		var r map[string]int
		if err := c.Call("queue-rm", daemon.QueueRmArgs{ID: id, To: store.Address(*to)}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Printf("removed %d\n", r["removed"])
		return nil
	}
	fs := newFlags("queue")
	rest := parse(fs, args)
	if len(rest) < 1 {
		return fmt.Errorf("usage: mailai queue <to> <text>")
	}
	text, err := textArg(rest[1:])
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r map[string]any
	if err := c.Call("queue", daemon.QueueArgs{To: store.Address(rest[0]), Text: strings.TrimSpace(text)}, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Printf("queued #%v for %s; typed when it comes to rest\n", r["id"], rest[0])
	return nil
}
