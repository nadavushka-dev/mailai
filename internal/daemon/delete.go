package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Deleting mail (SPEC §6): the person deletes a thread or messages by hand;
// read mail goes by itself after KeepFor. Agents cannot delete.

// DeleteArgs names a whole thread, or messages by mid; or, alone,
// ReadBetweenAgents: every read thread between agents. DryRun only counts
// what that would delete, for a confirm. Via names the client ("cli" or
// "tui") for the daemon log only; the web is known without it.
type DeleteArgs struct {
	Thread            string   `json:"thread,omitempty"`
	MIDs              []string `json:"mids,omitempty"`
	ReadBetweenAgents bool     `json:"read_between_agents,omitempty"`
	DryRun            bool     `json:"dry_run,omitempty"`
	Via               string   `json:"via,omitempty"`
}

// DeleteResult: Deleted counts messages; Threads, threads (bulk delete).
type DeleteResult struct {
	Deleted int `json:"deleted"`
	Threads int `json:"threads,omitempty"`
}

// DefaultExpireEvery is how often read mail past KeepFor is swept.
const DefaultExpireEvery = time.Hour

// deletedVia says through what the person deleted, for the log: the web
// (a request with no socket), the CLI or TUI as they say, else the socket.
func deletedVia(c *conn, a DeleteArgs) string {
	switch {
	case c.Conn == nil:
		return "web"
	case a.Via == "cli" || a.Via == "tui":
		return a.Via
	}
	return "socket"
}

// logDeleted leaves the one daemon-log line every delete gets: how many
// messages in how many threads, by whom, and the threads (the first five,
// then how many more).
func (d *Daemon) logDeleted(msgs int, threads []string, by string) {
	ids := threads
	more := ""
	if len(ids) > 5 {
		ids, more = ids[:5], fmt.Sprintf(" +%d more", len(threads)-5)
	}
	d.log.Printf("deleted %d message%s in %d thread%s by %s: %s%s", msgs, plural(msgs), len(threads), plural(len(threads)), by, strings.Join(ids, " "), more)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (d *Daemon) deleteMail(c *conn, a DeleteArgs) (any, error) {
	if !isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person deletes mail")
	}
	by := "human via " + deletedVia(c, a)
	if a.ReadBetweenAgents {
		if a.Thread != "" || len(a.MIDs) > 0 {
			return nil, proto.Errorf(proto.CodeBadRequest, "read_between_agents deletes on its own; name no thread or messages with it")
		}
		return d.deleteReadBetweenAgents(a.DryRun, by)
	}
	if a.Thread == "" && len(a.MIDs) == 0 {
		return nil, proto.Errorf(proto.CodeBadRequest, "name a thread or messages to delete")
	}
	// The threads touched, for the log: the thread, then those of the mids.
	var threads []string
	seen := map[string]bool{}
	touch := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			threads = append(threads, id)
		}
	}
	if a.Thread != "" && len(d.st.Thread(a.Thread)) > 0 {
		touch(a.Thread)
	}
	for _, mid := range a.MIDs {
		if m, ok := d.st.Get(mid); ok {
			touch(m.ThreadID)
		}
	}
	n := 0
	var err error
	if a.Thread != "" {
		var k int
		k, err = d.st.DeleteThread(a.Thread)
		n += k
	}
	if err == nil && len(a.MIDs) > 0 {
		var k int
		k, err = d.st.Delete(a.MIDs...)
		n += k
	}
	if n > 0 {
		d.logDeleted(n, threads, by)
	}
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "nothing held here to delete")
	}
	d.publishEvent("deleted", map[string]any{"thread": a.Thread, "count": n})
	return DeleteResult{Deleted: n}, nil
}

// betweenAgentsRead says a thread is mail between agents that is all read:
// no message to or from the person (on any machine), none unread.
func betweenAgentsRead(msgs []store.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	for _, m := range msgs {
		if m.ReadAt == 0 {
			return false
		}
		for _, a := range []store.Address{m.From, m.To} {
			if name, _ := splitAddr(a); store.Address(name) == store.Human {
				return false
			}
		}
	}
	return true
}

// deleteReadBetweenAgents deletes every read thread between agents in this
// machine's store (§6); Inbox items stay. A thread is checked again right
// before it goes, so mail that arrived meanwhile keeps it.
func (d *Daemon) deleteReadBetweenAgents(dry bool, by string) (any, error) {
	seen := map[string]bool{}
	var ids []string
	for _, box := range d.st.Mailboxes() {
		msgs, _ := d.st.Peek(store.PeekQuery{Mailbox: box})
		for _, m := range msgs {
			if !seen[m.ThreadID] {
				seen[m.ThreadID] = true
				ids = append(ids, m.ThreadID)
			}
		}
	}
	res := DeleteResult{}
	var gone []string
	for _, id := range ids {
		th := d.st.Thread(id)
		if !betweenAgentsRead(th) {
			continue
		}
		if dry {
			res.Threads++
			res.Deleted += len(th)
			continue
		}
		n, err := d.st.DeleteThread(id)
		res.Deleted += n
		if n > 0 {
			res.Threads++
			gone = append(gone, id)
		}
		if err != nil {
			d.logDeleted(res.Deleted, gone, by+" (read between agents)")
			return nil, err
		}
	}
	if !dry && res.Deleted > 0 {
		d.logDeleted(res.Deleted, gone, by+" (read between agents)")
		d.publishEvent("deleted", map[string]any{"count": res.Deleted, "threads": res.Threads})
	}
	return res, nil
}

// expire deletes read mail past KeepFor; zero keeps it until evicted.
func (d *Daemon) expire() {
	if d.keepFor <= 0 {
		return
	}
	n, threads, err := d.st.ExpireThreads(d.keepFor)
	if err != nil {
		d.log.Printf("expire: %v", err)
	}
	if n > 0 {
		d.logDeleted(n, threads, fmt.Sprintf("expiry (read over %s ago)", d.keepFor))
		d.publishEvent("deleted", map[string]any{"count": n})
	}
}

func (d *Daemon) runExpire(ctx context.Context) {
	if d.keepFor <= 0 {
		return
	}
	t := time.NewTicker(d.expireEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.expire()
		}
	}
}
