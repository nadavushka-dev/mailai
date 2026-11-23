package daemon

import (
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// askGraph records the asks open right now, so a cycle is refused before it
// is created rather than noticed after two agents wait on each other.
type askGraph struct {
	edges map[store.Address]map[store.Address]int
	open  map[store.Address]int
}

func newAskGraph() *askGraph {
	return &askGraph{edges: map[store.Address]map[store.Address]int{}, open: map[store.Address]int{}}
}

// reaches says whether to can already reach from through open asks.
func (g *askGraph) reaches(from, to store.Address) bool {
	seen := map[store.Address]bool{}
	var walk func(store.Address) bool
	walk = func(n store.Address) bool {
		if n == from {
			return true
		}
		if seen[n] {
			return false
		}
		seen[n] = true
		for next := range g.edges[n] {
			if walk(next) {
				return true
			}
		}
		return false
	}
	return walk(to)
}

func (g *askGraph) add(from, to store.Address) {
	if g.edges[from] == nil {
		g.edges[from] = map[store.Address]int{}
	}
	g.edges[from][to]++
	g.open[from]++
}

func (g *askGraph) remove(from, to store.Address) {
	if g.edges[from] != nil {
		g.edges[from][to]--
		if g.edges[from][to] <= 0 {
			delete(g.edges[from], to)
		}
		if len(g.edges[from]) == 0 {
			delete(g.edges, from)
		}
	}
	g.open[from]--
	if g.open[from] <= 0 {
		delete(g.open, from)
	}
}

// ask sends a message of kind ask and blocks the request until a reply in
// its thread reaches the asker, or the timeout passes (SPEC §7).
func (d *Daemon) ask(c *conn, a AskArgs) (any, error) {
	from, err := resolveFrom(c.pl, a.From)
	if err != nil {
		return nil, err
	}
	if a.To == "" {
		return nil, proto.Errorf(proto.CodeBadRequest, "ask needs a recipient")
	}
	a.To = d.localize(a.To)
	if a.To == from {
		return nil, proto.Errorf(proto.CodeAskCycle, "cannot ask yourself")
	}
	if d.isSpoke() && d.offMachine(a.To) {
		return d.askRemote(c.gone, from, a)
	}
	if err := d.checkRoute(a.To); err != nil {
		return nil, err
	}
	if a.To == from {
		return nil, proto.Errorf(proto.CodeAskCycle, "cannot ask yourself")
	}
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond

	if err := d.checkAsk(from, a.To); err != nil {
		return nil, err
	}
	defer d.releaseAsk(from, a.To)

	// Register the waiter before storing, so a reply that races the ask's
	// own publish cannot be missed.
	ch := make(chan store.Message, 1)
	var m store.Message
	d.mu.Lock()
	m, err = d.st.Send(store.SendRequest{From: from, To: a.To, Subject: a.Subject, Text: a.Text, Kind: store.KindAsk, Proof: c.pl.Proof, SenderPane: senderPane(c.pl), Verified: verifiedFor(c.pl, from)})
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	d.waits[m.ThreadID] = append(d.waits[m.ThreadID], ch)
	d.mu.Unlock()
	defer d.unwait(m.ThreadID, ch)
	d.publish(m)

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	for {
		select {
		case r := <-ch:
			// Only a reply addressed to the asker ends the wait; a reply
			// the recipient sends to someone else in the thread does not.
			if r.To == from {
				// SPEC §7: pushed down this connection, so it is not also
				// an unread notice. Mark before the RPC returns.
				if _, err := d.st.Read(store.ReadQuery{Mailbox: from, IDs: []uint64{r.ID}, Mark: true}); err != nil {
					return nil, err
				}
				return AskResult{Ask: m, Reply: r}, nil
			}
		case <-timer:
			return nil, proto.Errorf(proto.CodeTimeout, "no reply from %s within %s", a.To, timeout)
		case <-c.gone:
			// The asker died waiting (SPEC §7a): an ask to the person is
			// withdrawn, or answered in the session if the person types
			// there next; the deferred release frees the graph edge either
			// way.
			if a.To == store.Human {
				d.askDroppedMID(m.MID, from)
			}
			return nil, errClientGone
		}
	}
}

// checkAsk refuses an ask that would exceed the sender's limit or close a
// cycle, and records it in the graph.
func (d *Daemon) checkAsk(from, to store.Address) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.asks.open[from] >= d.maxAsk {
		return proto.Errorf(proto.CodeAskLimit, "%d asks already open from %s", d.maxAsk, from)
	}
	if d.asks.reaches(from, to) {
		return proto.Errorf(proto.CodeAskCycle, "%s is already waiting on %s", to, from)
	}
	d.asks.add(from, to)
	return nil
}

func (d *Daemon) releaseAsk(from, to store.Address) {
	d.mu.Lock()
	d.asks.remove(from, to)
	d.mu.Unlock()
}

func (d *Daemon) unwait(thread string, ch chan store.Message) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := d.waits[thread]
	for i, w := range ws {
		if w == ch {
			ws = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	if len(ws) == 0 {
		delete(d.waits, thread)
	} else {
		d.waits[thread] = ws
	}
}
