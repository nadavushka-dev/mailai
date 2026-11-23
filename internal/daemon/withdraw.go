package daemon

import (
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Withdrawn questions (SPEC §7a, decided 2026-11-23). An agent takes back
// its own open question with withdraw, or with ask-human --replaces when it
// asks again. A blocking ask whose connection drops is withdrawn too, unless
// the person then types in that agent's session within InSessionWindow:
// Esc-and-answer kills the ask first and types second, while an agent that
// cancels its own ask does so mid-turn with nobody typing after.

// DefaultInSessionWindow is how long after a dropped ask the person's
// typing in that session still means they answered it there.
const DefaultInSessionWindow = 2 * time.Minute

type WithdrawArgs struct {
	Item uint64 `json:"item"`
}

type WithdrawResult struct {
	Item store.Item `json:"item"`
}

// dropped is an ask whose connection dropped, kept for the window in which
// the person's typing turns it from withdrawn into answered in the session.
type dropped struct {
	id uint64
	at time.Time
}

// withdraw is the verb: the asker's own open ask or ask-human, nobody
// else's; the person dismisses instead.
func (d *Daemon) withdraw(c *conn, a WithdrawArgs) (any, error) {
	if d.isSpoke() {
		return nil, proto.Errorf(proto.CodeBadRequest, "withdrawing from a spoke comes later; the question stays open until the person answers or dismisses it")
	}
	from, err := resolveFrom(c.pl, "")
	if err != nil {
		return nil, err
	}
	if err := d.withdrawable(from, a.Item); err != nil {
		return nil, err
	}
	it, ok := d.closeWithdrawn(a.Item)
	if !ok {
		return nil, proto.Errorf(proto.CodeNotFound, "no open question #%d", a.Item)
	}
	return WithdrawResult{Item: it}, nil
}

// withdrawable says whether from may take item id back.
func (d *Daemon) withdrawable(from store.Address, id uint64) error {
	if from == store.Human {
		return errForbidden("the person dismisses a question; withdraw is for the agent that asked it")
	}
	it, ok := d.st.Item(id)
	if !ok || it.ClosedAt != 0 || (it.Kind != store.ItemAsk && it.Kind != store.ItemAskHuman) {
		return proto.Errorf(proto.CodeNotFound, "no open question #%d", id)
	}
	if it.From != from {
		return errForbidden("only the agent that asked a question withdraws it")
	}
	return nil
}

// closeWithdrawn closes an open item as withdrawn, tells subscribers, and
// ends a blocking ask still waiting on it.
func (d *Daemon) closeWithdrawn(id uint64) (store.Item, bool) {
	it, err := d.st.CloseItem(id, store.ClosedWithdrawn, "")
	if err != nil {
		return store.Item{}, false
	}
	d.publishItem(it)
	d.mu.Lock()
	for _, ch := range d.itemWaits[id] {
		close(ch)
	}
	delete(d.itemWaits, id)
	d.mu.Unlock()
	return it, true
}

// watchItem returns a channel closed when item id is withdrawn, and a
// func that stops watching.
func (d *Daemon) watchItem(id uint64) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	d.mu.Lock()
	if d.itemWaits == nil {
		d.itemWaits = map[uint64][]chan struct{}{}
	}
	d.itemWaits[id] = append(d.itemWaits[id], ch)
	d.mu.Unlock()
	return ch, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		list := d.itemWaits[id]
		for i, c := range list {
			if c == ch {
				d.itemWaits[id] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(d.itemWaits[id]) == 0 {
			delete(d.itemWaits, id)
		}
	}
}

// askDropped: who's blocking ask on item id lost its connection. It is
// withdrawn now and remembered for the window.
func (d *Daemon) askDropped(id uint64, who store.Address) {
	if _, ok := d.closeWithdrawn(id); !ok {
		return
	}
	d.mu.Lock()
	if d.drops == nil {
		d.drops = map[store.Address][]dropped{}
	}
	d.drops[who] = append(d.drops[who], dropped{id: id, at: d.now()})
	d.mu.Unlock()
}

// askDroppedMID is askDropped for the item an ask opened, by its mid.
func (d *Daemon) askDroppedMID(mid string, who store.Address) {
	for _, it := range d.st.Items(false) {
		if it.MID == mid {
			d.askDropped(it.ID, who)
		}
	}
}

// sessionTyped: the person typed in who's session (a prompt from its hook,
// or Herdr saw it go from rest to work). Its asks dropped within the
// window were answered there.
func (d *Daemon) sessionTyped(who store.Address) {
	d.mu.Lock()
	list := d.drops[who]
	delete(d.drops, who)
	d.mu.Unlock()
	now := d.now()
	for _, dr := range list {
		if now.Sub(dr.at) > d.inSessionWindow {
			continue
		}
		if it, ok := d.st.Relabel(dr.id, store.ClosedWithdrawn, store.ClosedInSession); ok {
			d.publishItem(it)
		}
	}
}
