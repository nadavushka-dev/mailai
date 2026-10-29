package main

import (
	"fmt"
	"os"
	"time"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
)

func cmdToken(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mailai token new <name> [--label L] | ls | revoke <name>")
	}
	sub, args := args[0], args[1:]
	fs := newFlags("token " + sub)
	label := fs.String("label", "", "what this token is for")
	rest := parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch sub {
	case "new":
		if len(rest) != 1 {
			return fmt.Errorf("usage: mailai token new <name>")
		}
		var r daemon.TokenNewResult
		if err := c.Call("token-new", daemon.TokenNewArgs{Name: store.Address(rest[0]), Label: *label}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		// Shown once; the daemon keeps only a hash.
		fmt.Printf("%s\n", r.Token)
		fmt.Fprintf(os.Stderr, "token for %s, shown once. Start the agent with MAILAI_TOKEN=%s in its environment.\n", r.Name, r.Token)
	case "ls", "list":
		var r struct {
			Tokens []tokens.Record `json:"tokens"`
		}
		if err := c.Call("token-list", nil, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		if len(r.Tokens) == 0 {
			fmt.Println("no tokens")
		}
		for _, t := range r.Tokens {
			fmt.Printf("%s\t%s\t%s\n", t.Name, time.Unix(0, t.CreatedAt).Format(time.RFC3339), t.Label)
		}
	case "revoke":
		if len(rest) != 1 {
			return fmt.Errorf("usage: mailai token revoke <name>")
		}
		var r map[string]int
		if err := c.Call("token-revoke", daemon.TokenRevokeArgs{Name: store.Address(rest[0])}, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Printf("revoked %d token(s) of %s\n", r["revoked"], rest[0])
	default:
		fmt.Fprintf(os.Stderr, "mailai token: unknown subcommand %q\n", sub)
		os.Exit(2)
	}
	return nil
}
