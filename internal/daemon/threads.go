package daemon

import (
	"sort"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// The Mail view (D13): every thread, for the person and for anyone whose
// read scope covers the participants. A thread row is enough to pick one;
// the thread itself comes from read --thread.

type ThreadRow struct {
	ThreadID string        `json:"thread_id"`
	Subject  string        `json:"subject,omitempty"`
	From     store.Address `json:"from"`
	To       store.Address `json:"to,omitempty"`
	Session  string        `json:"session,omitempty"`
	Kind     store.Kind    `json:"kind"`
	Count    int           `json:"count"`
	Unread   int           `json:"unread"`
	// LastAt is the newest message's time; LastFrom who sent it.
	LastAt   int64         `json:"last_at"`
	LastFrom store.Address `json:"last_from"`
	// Verified says the last message carried a verified mark.
	Verified string `json:"verified,omitempty"`
	// First is the first line of the root message, for the row.
	First string `json:"first"`
}

type ThreadsResult struct {
	Threads []ThreadRow `json:"threads"`
}

func (d *Daemon) threads(c *conn) (any, error) {
	rows := map[string]*ThreadRow{}
	for _, box := range d.st.Mailboxes() {
		msgs, _ := d.st.Peek(store.PeekQuery{Mailbox: box})
		for _, m := range msgs {
			if !d.canSeeMessage(c, m) {
				continue
			}
			r := rows[m.ThreadID]
			if r == nil {
				r = &ThreadRow{ThreadID: m.ThreadID}
				rows[m.ThreadID] = r
			}
			r.Count++
			if m.ReadAt == 0 {
				r.Unread++
			}
			if m.MID == m.ThreadID || r.From == "" {
				r.From, r.To, r.Subject, r.Session, r.Kind = m.From, m.To, m.Subject, m.Session, m.Kind
				r.First = firstLine(m.Text)
			}
			if m.SentAt >= r.LastAt {
				r.LastAt, r.LastFrom, r.Verified = m.SentAt, m.From, m.Verified
			}
		}
	}
	out := make([]ThreadRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	return ThreadsResult{Threads: out}, nil
}

// canSeeMessage: the person sees all; an agent sees what it sent or
// received, and what its read scope or own session covers.
func (d *Daemon) canSeeMessage(c *conn, m store.Message) bool {
	if isPerson(c.pl) {
		return true
	}
	p := c.pl.Principal
	if p == "" {
		return false
	}
	if m.From == p || m.To == p {
		return true
	}
	so := d.sessionOf()
	for _, who := range []store.Address{m.From, m.To} {
		if who == "" || who == store.Human {
			continue
		}
		if _, ok := d.st.Allowed(p, store.RightRead, who, so); ok {
			return true
		}
		if mine := so(p); mine != "" && mine == so(who) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
