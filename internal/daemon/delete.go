package daemon

import (
	"context"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Deleting mail (SPEC §6): the person deletes a thread or messages by hand;
// read mail goes by itself after KeepFor. Agents cannot delete.

// DeleteArgs names a whole thread, or messages by mid; or, alone,
// ReadBetweenAgents: every read thread between agents. DryRun only counts
// what that would delete, for a confirm.
type DeleteArgs struct {
	Thread            string   `json:"thread,omitempty"`
	MIDs              []string `json:"mids,omitempty"`
	ReadBetweenAgents bool     `json:"read_between_agents,omitempty"`
	DryRun            bool     `json:"dry_run,omitempty"`
}

// DeleteResult: Deleted counts messages; Threads, threads (bulk delete).
type DeleteResult struct {
	Deleted int `json:"deleted"`
	Threads int `json:"threads,omitempty"`
}

// DefaultExpireEvery is how often read mail past KeepFor is swept.
const DefaultExpireEvery = time.Hour

func (d *Daemon) deleteMail(c *conn, a DeleteArgs) (any, error) {
	if !isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person deletes mail")
	}
	if a.ReadBetweenAgents {
		if a.Thread != "" || len(a.MIDs) > 0 {
			return nil, proto.Errorf(proto.CodeBadRequest, "read_between_agents deletes on its own; name no thread or messages with it")
		}
		return d.deleteReadBetweenAgents(a.DryRun)
	}
	if a.Thread == "" && len(a.MIDs) == 0 {
		return nil, proto.Errorf(proto.CodeBadRequest, "name a thread or messages to delete")
	}
	n := 0
	if a.Thread != "" {
		k, err := d.st.DeleteThread(a.Thread)
		n += k
		if err != nil {
			return nil, err
		}
	}
	if len(a.MIDs) > 0 {
		k, err := d.st.Delete(a.MIDs...)
		n += k
		if err != nil {
			return nil, err
		}
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
func (d *Daemon) deleteReadBetweenAgents(dry bool) (any, error) {
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
		}
		if err != nil {
			return nil, err
		}
	}
	if !dry && res.Deleted > 0 {
		d.publishEvent("deleted", map[string]any{"count": res.Deleted, "threads": res.Threads})
	}
	return res, nil
}

// expire deletes read mail past KeepFor; zero keeps it until evicted.
func (d *Daemon) expire() {
	if d.keepFor <= 0 {
		return
	}
	n, err := d.st.Expire(d.keepFor)
	if err != nil {
		d.log.Printf("expire: %v", err)
	}
	if n > 0 {
		d.log.Printf("expired %d read message(s) older than %s", n, d.keepFor)
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
