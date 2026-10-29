package main

import (
	"github.com/nadavushka-dev/mailai/internal/tui"
)

func cmdTUI(args []string) error {
	parse(newFlags("tui"), args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	return tui.Run(tui.Live{C: c})
}
