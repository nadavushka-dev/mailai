package main

import (
	"fmt"
	"os"

	"github.com/nadavushka-dev/mailai/internal/daemon"
)

// cmdMachine is the person's view of the star's machines (SPEC §13a): which
// spokes have linked to this inbox machine, and forgetting one so it can
// link again with a new machine key.
func cmdMachine(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mailai machine ls | forget <name>")
	}
	sub, args := args[0], args[1:]
	rest := parse(newFlags("machine "+sub), args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch sub {
	case "ls", "list":
		var r daemon.MachineListResult
		if err := c.Call("machine-list", nil, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		if len(r.Machines) == 0 {
			fmt.Println("no machines have linked here")
		}
		for _, m := range r.Machines {
			linked, pinned := "down", "unpinned"
			if m.Linked {
				linked = "linked"
			}
			if m.Pinned {
				pinned = "key pinned"
			}
			fmt.Printf("%s\t%s\t%s\n", m.Name, linked, pinned)
		}
	case "forget":
		if len(rest) != 1 {
			return fmt.Errorf("usage: mailai machine forget <name>")
		}
		var r map[string]string
		if err := c.Call("machine-forget", daemon.MachineForgetArgs{Name: rest[0]}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Printf("forgot %s; its next link pins its key\n", rest[0])
	default:
		fmt.Fprintf(os.Stderr, "mailai machine: unknown subcommand %q\n", sub)
		os.Exit(2)
	}
	return nil
}
