package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// The spoke outbox (SPEC §13a). Every mail, ask and ask-human a spoke sends
// off the machine is queued durably first, under a mid the spoke mints, and
// leaves the queue once the inbox machine has it (it dedupes by mid). With
// the link up and nothing older queued it goes at once, so a refusal still
// reaches the caller; otherwise it goes in order when the link is back.

// Bouncer is the sender of a bounce: the daemon itself, never an agent.
const Bouncer store.Address = "mailai"

// outMessage is the queued send as the caller sees it before delivery.
func outMessage(o store.Outgoing) store.Message {
	return store.Message{MID: o.MID, ThreadID: o.ThreadID, Kind: o.Kind, From: o.From, To: o.To, Subject: o.Subject,
		Text: o.Text, ReplyTo: o.ReplyTo, Options: o.Options, SentAt: o.QueuedAt}
}

// linkArgs is what goes over the link for o; an ask carries what is left of
// its wait.
func (d *Daemon) linkArgs(o store.Outgoing) LinkSendArgs {
	a := LinkSendArgs{From: o.From, To: o.To, Kind: o.Kind, Subject: o.Subject, Text: o.Text, ReplyTo: o.ReplyTo,
		Options: o.Options, MID: o.MID, ThreadID: o.ThreadID}
	if o.Kind == store.KindAsk && o.TimeoutMS > 0 {
		left := o.TimeoutMS - time.Since(time.Unix(0, o.QueuedAt)).Milliseconds()
		if left < 1 {
			left = 1
		}
		a.TimeoutMS = left
	}
	return a
}

func (d *Daemon) sendOut(o store.Outgoing) (LinkSendResult, error) {
	var r LinkSendResult
	cl := d.uplink()
	if cl == nil {
		// No code: transient, it waits for the link.
		return r, errors.New("the link to the inbox machine is down")
	}
	err := cl.Call("link-send", d.linkArgs(o), &r)
	return r, err
}

// transient says a send may succeed later: the link failed, or the inbox
// machine is busy (rate limit, a full mailbox).
func transient(err error) bool {
	switch client.Code(err) {
	case "", proto.CodeRateLimited, proto.CodeMailboxFull:
		return true
	}
	return false
}

// outSend queues o and sends it now when it can. queued says it is still
// waiting; err is a refusal the caller gets (and o is not queued).
func (d *Daemon) outSend(o store.Outgoing) (LinkSendResult, bool, error) {
	d.obMu.Lock()
	defer d.obMu.Unlock()
	if out := d.st.Outbox(); len(out) == 0 || out[0].ID != o.ID || d.uplink() == nil {
		d.kickOutbox()
		return LinkSendResult{}, true, nil
	}
	r, err := d.sendOut(o)
	switch {
	case err == nil:
		d.st.Dequeue(o.ID)
		return r, false, nil
	case client.Code(err) == "":
		// The link failed under the send; it goes on reconnect.
		d.kickOutbox()
		return LinkSendResult{}, true, nil
	default:
		// A refusal, the busy kinds included, goes back to the caller as
		// it did before the outbox: there is someone to tell.
		d.st.Dequeue(o.ID)
		return r, false, err
	}
}

func (d *Daemon) kickOutbox() {
	select {
	case d.obKick <- struct{}{}:
	default:
	}
}

// runOutbox drains the outbox whenever kicked: on link up, on a heartbeat,
// on a send queued behind others.
func (d *Daemon) runOutbox(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.obKick:
			d.drainOutbox()
		}
	}
}

func (d *Daemon) drainOutbox() {
	d.obMu.Lock()
	defer d.obMu.Unlock()
	for _, o := range d.st.Outbox() {
		if d.uplink() == nil {
			return
		}
		_, err := d.sendOut(o)
		if err != nil && transient(err) {
			return
		}
		d.st.Dequeue(o.ID)
		if err != nil {
			d.log.Printf("outbox: %s to %s refused: %v", o.MID, o.To, err)
			d.refused(o, err)
		}
	}
}

// refused tells whoever waits on o that the inbox machine will not take
// it: a waiting ask gets the error; otherwise the sender gets a bounce.
func (d *Daemon) refused(o store.Outgoing, err error) {
	d.spokeAskRefused(o.ThreadID, err)
	d.mu.Lock()
	fails := d.outFail[o.ThreadID]
	d.mu.Unlock()
	told := false
	for _, f := range fails {
		select {
		case f <- err:
			told = true
		default:
		}
	}
	if told {
		return
	}
	text := fmt.Sprintf("not delivered to %s: %s", o.To, err.Error())
	b, serr := d.st.Send(store.SendRequest{From: Bouncer, To: o.From, Kind: store.KindReply, ReplyTo: o.MID, Text: text, NoLimit: true})
	if serr != nil {
		d.log.Printf("outbox: bounce for %s: %v", o.MID, serr)
		return
	}
	d.publish(b)
}

func (d *Daemon) registerFail(thread string) chan error {
	ch := make(chan error, 1)
	d.mu.Lock()
	d.outFail[thread] = append(d.outFail[thread], ch)
	d.mu.Unlock()
	return ch
}

func (d *Daemon) unregisterFail(thread string, ch chan error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.outFail[thread]
	for i, x := range list {
		if x == ch {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(d.outFail, thread)
	} else {
		d.outFail[thread] = list
	}
}
