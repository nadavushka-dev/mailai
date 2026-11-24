package daemon

import (
	"fmt"
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
	d.endItemWaits(id)
	return it, true
}

// endItemWaits ends every blocking ask still waiting on item id, which
// closed without an answer (withdrawn or dismissed), and says whether any
// was waiting.
func (d *Daemon) endItemWaits(id uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.itemWaits[id]
	for _, ch := range list {
		close(ch)
	}
	delete(d.itemWaits, id)
	return len(list) > 0
}

// itemEnded is what a blocking ask gets when its item closed without an
// answer: withdrawn by its asker, or dismissed (SPEC §7a).
func (d *Daemon) itemEnded(id uint64) error {
	if it, _ := d.st.Item(id); it.ClosedBy == store.ClosedDismissed {
		return proto.Errorf(proto.CodeNotFound, "#%d was dismissed by %s", id, dismisser(it.DecidedBy))
	}
	return proto.Errorf(proto.CodeNotFound, "#%d was withdrawn", id)
}

// dismisser names who dismissed an item: the person, or supervisor:<name>.
func dismisser(by string) string {
	if by == "" || by == string(store.Human) {
		return "the person"
	}
	return by
}

// noteDismissed leaves one fixed line from mailai in a dismissed
// question's thread, addressed to its asker, so an asker that is not
// blocked (--no-wait) learns of it as mail (SPEC §7a). Written by the
// daemon: never verified, never the person's words, never an answer.
// Already read when a blocking ask was told by its error. An asker on
// another machine gets none: its spoke would take any reply in the thread
// as the answer (a known gap).
func (d *Daemon) noteDismissed(it store.Item, told bool) {
	if it.Kind != store.ItemAsk && it.Kind != store.ItemAskHuman {
		return
	}
	if _, host := splitAddr(it.From); host != "" && host != d.name {
		return
	}
	text := fmt.Sprintf("#%d dismissed by %s", it.ID, dismisser(it.DecidedBy))
	m, err := d.st.Send(store.SendRequest{From: Bouncer, To: it.From, Kind: store.KindReply, ReplyTo: it.MID, Text: text, NoLimit: true})
	if err != nil {
		d.log.Printf("dismiss #%d: note to %s: %v", it.ID, it.From, err)
		return
	}
	if told {
		if _, err := d.st.Read(store.ReadQuery{Mailbox: it.From, IDs: []uint64{m.ID}, Mark: true}); err != nil {
			d.log.Printf("dismiss #%d: marking the note read: %v", it.ID, err)
		}
	}
	d.publish(m)
}

// watchItem returns a channel closed when item id closes without an answer
// (withdrawn or dismissed), and a func that stops watching.
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
