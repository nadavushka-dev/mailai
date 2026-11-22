package daemon

import (
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Link asks (SPEC §13a): an ask from name@spoke waits on the spoke, but its
// edge lives in the inbox machine's ask graph, where every machine's asks
// meet, so a cycle through the inbox machine is refused with ask_cycle.
// The edge goes when a reply in the ask's thread reaches the asker, or when
// the ask's timeout (24 h at most) passes.

// linkAskMax bounds how long a link ask holds its edge.
const linkAskMax = 24 * time.Hour

type linkAsk struct {
	mid      string
	from, to store.Address
	deadline time.Time
	timer    *time.Timer
}

func linkAskTimeout(ms int64) time.Duration {
	t := time.Duration(ms) * time.Millisecond
	if ms <= 0 || t > linkAskMax {
		return linkAskMax
	}
	return t
}

// openLinkAsk refuses a closing edge, else adds it. The caller registers it
// with holdLinkAsk once the ask is stored, or drops it.
func (d *Daemon) openLinkAsk(from, to store.Address) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.asks.reaches(from, to) {
		return proto.Errorf(proto.CodeAskCycle, "%s is already waiting on %s", to, from)
	}
	d.asks.add(from, to)
	return nil
}

// holdLinkAsk keeps a stored link ask's edge until its reply or timeout.
func (d *Daemon) holdLinkAsk(m store.Message, timeout time.Duration) {
	la := &linkAsk{mid: m.MID, from: m.From, to: m.To, deadline: time.Now().Add(timeout)}
	d.mu.Lock()
	d.linkAsks[m.ThreadID] = append(d.linkAsks[m.ThreadID], la)
	la.timer = time.AfterFunc(timeout, func() { d.closeLinkAsk(m.ThreadID, la) })
	d.mu.Unlock()
}

func (d *Daemon) closeLinkAsk(thread string, la *linkAsk) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.linkAsks[thread]
	for i, x := range list {
		if x != la {
			continue
		}
		la.timer.Stop()
		d.asks.remove(la.from, la.to)
		list = append(list[:i], list[i+1:]...)
		if len(list) == 0 {
			delete(d.linkAsks, thread)
		} else {
			d.linkAsks[thread] = list
		}
		return
	}
}

// replyReleases lets go of link asks a reply answers: in their thread,
// addressed to the asker (what ends the wait on the spoke).
func (d *Daemon) replyReleases(m store.Message) {
	if m.Kind != store.KindReply {
		return
	}
	d.mu.Lock()
	var done []*linkAsk
	for _, la := range d.linkAsks[m.ThreadID] {
		if la.from == m.To {
			done = append(done, la)
		}
	}
	d.mu.Unlock()
	for _, la := range done {
		d.closeLinkAsk(m.ThreadID, la)
	}
}

// linkAskDeadline is when the edge of the link ask mid goes by timeout.
func (d *Daemon) linkAskDeadline(mid string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, list := range d.linkAsks {
		for _, la := range list {
			if la.mid == mid {
				return la.deadline
			}
		}
	}
	return time.Time{}
}
