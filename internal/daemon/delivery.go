package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/render"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Delivery (SPEC §7, D3, D12): an agent is told once, in one line, that mail
// waits. A hook asks for the line mid-turn (notice); an agent at rest gets
// it typed (wake). The line never holds anything a sender chose.

const (
	wakeAfter   = 5 * time.Second  // unread and unannounced this long
	wakeSpacing = 30 * time.Second // between two wakes of one agent
	wakeTick    = time.Second
)

// noticeMark is the newest message id announced to a mailbox, by hook or
// wake. One mark serves both, so a message is announced once.
type noticeMark struct {
	announcedID uint64
	lastWake    time.Time
}

type NoticeArgs struct {
	Mailbox store.Address `json:"mailbox,omitempty"`
	// Flags are appended to the read command in the line.
	Flags string `json:"flags,omitempty"`
}

type NoticeResult struct {
	Count int             `json:"count"`
	From  []store.Address `json:"from,omitempty"`
	Line  string          `json:"line"`
	// New is false when every unread message was already announced, so a
	// hook adds nothing to the agent's context (once per message).
	New bool `json:"new"`
}

// notice builds the line for a mailbox and marks its unread mail announced.
// Only the mailbox's own principal, or the person, marks; anyone else gets
// the line without moving the mark.
func (d *Daemon) notice(c *conn, a NoticeArgs) (any, error) {
	box, err := d.box(c, a.Mailbox)
	if err != nil {
		return nil, err
	}
	n, from := d.st.Unread(box)
	line := render.Notice(n, from, a.Flags)
	newest := d.newestUnread(box)
	d.mu.Lock()
	mk := d.marks[box]
	isNew := n > 0 && newest > mk.announcedID
	if isNew && (box == d.own(c.pl) || isPerson(c.pl)) {
		mk.announcedID = newest
		d.marks[box] = mk
	}
	d.mu.Unlock()
	return NoticeResult{Count: n, From: from, Line: line, New: isNew}, nil
}

func (d *Daemon) newestUnread(box store.Address) uint64 {
	msgs, _ := d.st.Peek(store.PeekQuery{Mailbox: box, Unread: true})
	var newest uint64
	for _, m := range msgs {
		if m.ID > newest {
			newest = m.ID
		}
	}
	return newest
}

// runWakes types the notice at agents at rest whose mail stayed unread and
// unannounced for wakeAfter (SPEC §7). It runs until ctx ends.
func (d *Daemon) runWakes(ctx context.Context) {
	t := time.NewTicker(wakeTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.wakeOnce(ctx)
		}
	}
}

func (d *Daemon) wakeOnce(ctx context.Context) {
	if !d.wake {
		return
	}
	now := d.now()
	for _, box := range d.st.Mailboxes() {
		if box == store.Human || isNoticeBox(box) {
			continue
		}
		state, known := d.stateOf(box)
		// A listener's harness decides when the line lands (Pi: a steer,
		// after the current tool call), so a listened agent is woken
		// whatever its state, mid-turn too (listen.go). Typing waits for
		// an agent the adapter reports at rest.
		listened := len(d.listenersOf(box)) > 0
		if !listened && (!known || !state.AtRest()) {
			continue
		}
		msgs, _ := d.st.Peek(store.PeekQuery{Mailbox: box, Unread: true})
		if len(msgs) == 0 {
			continue
		}
		d.mu.Lock()
		mk := d.marks[box]
		d.mu.Unlock()
		var newest uint64
		oldestUnannounced := int64(0)
		for _, m := range msgs {
			if m.ID > mk.announcedID {
				if oldestUnannounced == 0 || m.SentAt < oldestUnannounced {
					oldestUnannounced = m.SentAt
				}
			}
			if m.ID > newest {
				newest = m.ID
			}
		}
		if oldestUnannounced == 0 || now.Sub(time.Unix(0, oldestUnannounced)) < wakeAfter {
			continue
		}
		if !mk.lastWake.IsZero() && now.Sub(mk.lastWake) < wakeSpacing {
			continue
		}
		// Rebuilt right before typing: mail read meanwhile drops the wake.
		n, from := d.st.Unread(box)
		if n == 0 {
			continue
		}
		line := render.Notice(n, from, "")
		if d.tellListeners(box, line) {
			// Taken by the agent's harness: nothing typed (SPEC §7).
		} else if err := d.adapter.Type(ctx, box, line); err != nil {
			// A failed wake waits the spacing too: no retry (or log line)
			// on every tick. The mail stays unannounced, so it goes later.
			d.mu.Lock()
			mk = d.marks[box]
			mk.lastWake = now
			d.marks[box] = mk
			d.mu.Unlock()
			d.log.Printf("wake %s: %v", box, err)
			continue
		}
		d.mu.Lock()
		mk = d.marks[box]
		mk.announcedID, mk.lastWake = newest, now
		d.marks[box] = mk
		d.mu.Unlock()
		d.publishEvent("wake", map[string]any{"mailbox": box, "count": n})
	}
}

func isNoticeBox(a store.Address) bool { return len(a) > 8 && a[:8] == "session:" }

// Queue (SPEC §7): text typed as the agent's next prompt when it comes to
// rest, never over a prompt it waits on, never twice. It is the one thing
// mailai types that a sender wrote, and it requires write on the target
// (grants, M4; until then any placed local caller or the person).

type QueueArgs struct {
	To   store.Address `json:"to"`
	Text string        `json:"text"`
}

type queued struct {
	ID     uint64        `json:"id"`
	To     store.Address `json:"to"`
	By     store.Address `json:"by"`
	Text   string        `json:"text"`
	Queued int64         `json:"queued_at"`
}

type QueueListResult struct {
	Queued []queued `json:"queued"`
}

type QueueRmArgs struct {
	ID uint64        `json:"id,omitempty"`
	To store.Address `json:"to,omitempty"`
}

func (d *Daemon) queue(c *conn, a QueueArgs) (any, error) {
	from, err := resolveFrom(c.pl, "")
	if err != nil {
		return nil, err
	}
	if a.To == "" || a.To == store.Human {
		return nil, proto.Errorf(proto.CodeBadRequest, "queue needs an agent to type at")
	}
	if _, host := splitAddr(d.localize(a.To)); host != "" {
		return nil, proto.Errorf(proto.CodeBadRequest, "queue does not cross machines")
	}
	a.To = d.localize(a.To)
	if len(a.Text) == 0 || len(a.Text) > store.MaxText {
		return nil, proto.Errorf(proto.CodeBadRequest, "text is 1 to %d bytes", store.MaxText)
	}
	if _, isNone := d.adapter.(adapter.None); isNone {
		return nil, proto.Errorf(proto.CodeBadRequest, "the none adapter cannot type; queue needs a multiplexer")
	}
	d.mu.Lock()
	d.queueSeq++
	q := queued{ID: d.queueSeq, To: a.To, By: from, Text: a.Text, Queued: d.now().UnixNano()}
	d.queued = append(d.queued, q)
	d.mu.Unlock()
	return q, nil
}

func (d *Daemon) queueList() QueueListResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]queued(nil), d.queued...)
	if out == nil {
		out = []queued{}
	}
	return QueueListResult{Queued: out}
}

func (d *Daemon) queueRm(c *conn, a QueueRmArgs) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	kept := d.queued[:0]
	for _, q := range d.queued {
		if (a.ID != 0 && q.ID == a.ID) || (a.ID == 0 && a.To != "" && q.To == a.To) {
			n++
			continue
		}
		kept = append(kept, q)
	}
	d.queued = kept
	if n == 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "nothing queued matched")
	}
	return map[string]int{"removed": n}, nil
}

// deliverQueued types the oldest queued text at each agent at rest, one per
// tick, and drops it once typed.
func (d *Daemon) deliverQueued(ctx context.Context) {
	d.mu.Lock()
	pending := append([]queued(nil), d.queued...)
	d.mu.Unlock()
	done := map[store.Address]bool{}
	for _, q := range pending {
		if done[q.To] {
			continue
		}
		state, known := d.stateOf(q.To)
		if !known || !state.AtRest() {
			continue
		}
		if err := d.adapter.Type(ctx, q.To, q.Text); err != nil {
			d.log.Printf("queue %d to %s: %v", q.ID, q.To, err)
			continue
		}
		done[q.To] = true
		d.mu.Lock()
		for i, x := range d.queued {
			if x.ID == q.ID {
				d.queued = append(d.queued[:i], d.queued[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
		d.publishEvent("queued_typed", map[string]any{"id": q.ID, "to": q.To})
	}
}

func (d *Daemon) publishAgent(r agentRec) {
	d.publishEvent("agent_state", map[string]any{"principal": r.Principal, "state": r.State, "harness": r.Harness})
}

func (d *Daemon) publishEvent(name string, v any) {
	data, _ := json.Marshal(v)
	d.fanout(proto.Event{Event: name, Data: data})
}
