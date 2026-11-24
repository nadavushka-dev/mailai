package daemon

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Listen (SPEC §7, decided 2026-11-23): a connection that takes its agent's
// wake lines. While one is open nothing is typed into the agent's pane; the
// listener (a harness add-on running `mailai listen`) hands the line to its
// agent itself. The daemon knows no harness: the rules for when to wake
// stay here, and the line is the one that would have been typed.

// ListenArgs: Check only asks whether the caller may listen.
type ListenArgs struct {
	Check bool `json:"check,omitempty"`
}

// ListenResult names the mailbox whose wakes the connection now takes.
type ListenResult struct {
	Mailbox store.Address `json:"mailbox"`
}

// ListenEvent is pushed, event "listen", when the agent should wake.
type ListenEvent struct {
	Line string `json:"line"`
}

// listenAllowed: the add-on is switched on per agent ([addon] agents, glob
// patterns over the name); nil patterns (a daemon built without config)
// allow every agent.
func (d *Daemon) listenAllowed(box store.Address) bool {
	if d.listenAgents == nil {
		return true
	}
	name := string(box)
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	for _, p := range d.listenAgents {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func (d *Daemon) listen(c *conn, a ListenArgs) (any, error) {
	if isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeBadRequest, "listen is for agents; the person has mailai tui and the web page")
	}
	box := d.own(c.pl)
	if box == "" {
		return nil, proto.Errorf(proto.CodeBadRequest, "who are you? listen needs an agent (a pane, or MAILAI_TOKEN)")
	}
	if !d.listenAllowed(box) {
		return nil, proto.Errorf(proto.CodeForbidden, "the add-on is not switched on for %s: add it to [addon] agents in the config", box)
	}
	if a.Check {
		return ListenResult{Mailbox: box}, nil
	}
	d.mu.Lock()
	if d.listeners == nil {
		d.listeners = map[store.Address]map[*conn]bool{}
	}
	if d.listeners[box] == nil {
		d.listeners[box] = map[*conn]bool{}
	}
	d.listeners[box][c] = true
	d.mu.Unlock()
	return ListenResult{Mailbox: box}, nil
}

// dropListener forgets a connection that went away.
func (d *Daemon) dropListener(c *conn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for box, cs := range d.listeners {
		if cs[c] {
			delete(cs, c)
			if len(cs) == 0 {
				delete(d.listeners, box)
			}
		}
	}
}

// listenersOf are the connections taking box's wakes, if any.
func (d *Daemon) listenersOf(box store.Address) []*conn {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*conn
	for c := range d.listeners[box] {
		out = append(out, c)
	}
	return out
}

// tellListeners hands the wake line to box's listeners; false when none
// took it (none open, or every write failed), so the caller types instead.
func (d *Daemon) tellListeners(box store.Address, line string) bool {
	ok := false
	data, _ := json.Marshal(ListenEvent{Line: line})
	for _, c := range d.listenersOf(box) {
		if err := c.write(proto.Event{Event: "listen", Data: data}); err == nil {
			ok = true
		}
	}
	return ok
}
