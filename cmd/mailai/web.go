package main

import (
	"fmt"

	"github.com/nadavushka-dev/mailai/internal/daemon"
)

// cmdWeb starts the browser Inbox (D11) and prints its one-time URL. The
// daemon serves it on 127.0.0.1 only; reach it from elsewhere through your
// own tunnel (ssh -L, Tailscale Serve).
func cmdWeb(args []string) error {
	fs := newFlags("web")
	port := fs.Int("port", 0, "port on 127.0.0.1; a free one by default")
	stop := fs.Bool("stop", false, "stop the web UI")
	revoke := fs.Bool("revoke", false, "end every browser session and unused link")
	parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch {
	case *stop:
		var r map[string]bool
		if err := c.Call("web-stop", nil, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		if r["stopped"] {
			fmt.Println("web UI stopped")
		} else {
			fmt.Println("web UI was not running")
		}
		return nil
	case *revoke:
		var r map[string]int
		if err := c.Call("web-revoke", nil, &r); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(r)
		}
		fmt.Printf("revoked %d session(s)\n", r["revoked"])
		return nil
	}
	var r daemon.WebStartResult
	if err := c.Call("web-start", daemon.WebStartArgs{Port: *port}, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Println(r.URL)
	fmt.Printf("open it once; the link works a single time and the browser session lasts 12 h. Loopback only: tunnel it yourself for a phone.\n")
	return nil
}
