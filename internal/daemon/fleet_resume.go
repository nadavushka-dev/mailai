package daemon

import (
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Resuming a spoke's ask-human (SPEC §13a). Harnesses kill a tool on their
// own timeout, so an agent asks with --timeout below it and comes back with
// --wait-for <id>. The id is the inbox machine's item id when the question
// reached it at once, else a local handle (localHandleBase + the queued
// send's id, so it never meets an item id). The spoke remembers, in memory,
// which thread each id waits on.

const localHandleBase uint64 = 1 << 40

type spokeAsk struct {
	from   store.Address
	thread string
	// err is the inbox machine's refusal of the queued question.
	err error
}

// comeBack turns a timeout into the hint that names the id to resume with.
func comeBack(err error, id uint64) error {
	if pe, ok := err.(*proto.Error); ok && pe.Code == proto.CodeTimeout {
		return proto.Errorf(proto.CodeTimeout, "no answer yet; come back with --wait-for %d", id)
	}
	return err
}

// answerIn is the first reply to from in thread, read or not.
func (d *Daemon) answerIn(thread string, from store.Address) (store.Message, bool) {
	for _, m := range d.st.Thread(thread) {
		if m.Kind == store.KindReply && m.To == from {
			return m, true
		}
	}
	return store.Message{}, false
}

func (d *Daemon) resumeAskHuman(from store.Address, a AskHumanArgs) (any, error) {
	d.mu.Lock()
	sa := d.spokeAsks[a.WaitFor]
	var refused error
	if sa != nil {
		refused = sa.err
	}
	d.mu.Unlock()
	if sa == nil || sa.from != from {
		return nil, proto.Errorf(proto.CodeNotFound, "no ask-human #%d from %s on this machine", a.WaitFor, from)
	}
	if refused != nil {
		return nil, refused
	}
	answered := func(m store.Message) (any, error) {
		d.st.Read(store.ReadQuery{Mailbox: from, IDs: []uint64{m.ID}, Mark: true})
		it := store.Item{ID: a.WaitFor, Kind: store.ItemAskHuman, ThreadID: sa.thread, From: from, ClosedBy: "answered", Answer: m.MID}
		return AskHumanResult{Item: it, Reply: &m}, nil
	}
	if m, ok := d.answerIn(sa.thread, from); ok {
		return answered(m)
	}
	ch := d.registerWait(sa.thread)
	defer d.unwait(sa.thread, ch)
	fail := d.registerFail(sa.thread)
	defer d.unregisterFail(sa.thread, fail)
	// The answer may have landed while the wait was being registered.
	if m, ok := d.answerIn(sa.thread, from); ok {
		return answered(m)
	}
	reply, err := d.waitThread(ch, fail, sa.thread, from, time.Duration(a.TimeoutMS)*time.Millisecond, "from the person")
	if err != nil {
		return nil, comeBack(err, a.WaitFor)
	}
	return answered(reply)
}

// spokeAskRefused records the inbox machine's refusal of a queued question,
// so a later --wait-for gets it.
func (d *Daemon) spokeAskRefused(thread string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, sa := range d.spokeAsks {
		if sa.thread == thread {
			sa.err = err
		}
	}
}
