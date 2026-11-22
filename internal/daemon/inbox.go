package daemon

import (
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// The Human Inbox (D13, SPEC §7): ask-human puts a question with fixed
// answers in front of the person; inbox lists what waits; answer and
// dismiss close items. Only the person answers or dismisses.

type AskHumanArgs struct {
	From    store.Address `json:"from,omitempty"`
	Subject string        `json:"subject,omitempty"`
	Text    string        `json:"text"`
	Options []string      `json:"options,omitempty"`
	// NoWait returns at once with the item; the answer arrives as mail.
	NoWait bool `json:"no_wait,omitempty"`
	// WaitFor resumes waiting on an earlier ask-human by its item id.
	WaitFor   uint64 `json:"wait_for,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type AskHumanResult struct {
	Item  store.Item     `json:"item"`
	Reply *store.Message `json:"reply,omitempty"`
	// Queued says a spoke holds the question until its link is back; the
	// Inbox item opens when it reaches the inbox machine.
	Queued bool `json:"queued,omitempty"`
}

type InboxArgs struct {
	All bool `json:"all,omitempty"`
}

type InboxResult struct {
	Items []store.Item `json:"items"`
}

type AnswerArgs struct {
	Item uint64 `json:"item"`
	Text string `json:"text"`
	// Message goes back to the agent with a deny, as the harness shows it.
	Message string `json:"message,omitempty"`
}

type DismissArgs struct {
	Item uint64 `json:"item"`
}

const (
	MaxOptions   = 8
	MaxQuestion  = 160
	minAskHumanT = 0
)

func (d *Daemon) askHuman(c *conn, a AskHumanArgs) (any, error) {
	from, err := resolveFrom(c.pl, a.From)
	if err != nil {
		return nil, err
	}
	if from == store.Human {
		return nil, proto.Errorf(proto.CodeBadRequest, "the person cannot ask-human")
	}
	if d.isSpoke() {
		if err := validAskHuman(a); err != nil {
			return nil, err
		}
		return d.askHumanRemote(from, a)
	}
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond

	var item store.Item
	if a.WaitFor != 0 {
		it, ok := d.st.Item(a.WaitFor)
		if !ok || it.From != from {
			return nil, proto.Errorf(proto.CodeNotFound, "no ask-human #%d from %s", a.WaitFor, from)
		}
		item = it
		if it.ClosedAt != 0 {
			return d.askHumanClosed(it)
		}
	} else {
		if err := validAskHuman(a); err != nil {
			return nil, err
		}
		if err := d.checkAsk(from, store.Human); err != nil {
			return nil, err
		}
		defer d.releaseAsk(from, store.Human)
		ch := make(chan store.Message, 1)
		d.mu.Lock()
		m, err := d.st.Send(store.SendRequest{From: from, To: store.Human, Subject: a.Subject, Text: a.Text, Options: a.Options,
			Kind: store.KindAskHuman, Proof: c.pl.Proof})
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		d.waits[m.ThreadID] = append(d.waits[m.ThreadID], ch)
		d.mu.Unlock()
		defer d.unwait(m.ThreadID, ch)
		d.publish(m)
		for _, it := range d.st.Items(false) {
			if it.MID == m.MID {
				item = it
			}
		}
		if a.NoWait {
			return AskHumanResult{Item: item}, nil
		}
		return d.waitReply(ch, from, item, timeout)
	}
	// Resuming: wait on the thread.
	ch := make(chan store.Message, 1)
	d.mu.Lock()
	d.waits[item.ThreadID] = append(d.waits[item.ThreadID], ch)
	d.mu.Unlock()
	defer d.unwait(item.ThreadID, ch)
	// The answer may have landed between Item and the wait registration.
	if it, _ := d.st.Item(item.ID); it.ClosedAt != 0 {
		return d.askHumanClosed(it)
	}
	return d.waitReply(ch, from, item, timeout)
}

func validAskHuman(a AskHumanArgs) error {
	if len(a.Text) > MaxQuestion {
		return proto.Errorf(proto.CodeBadRequest, "a question is one line of at most %d bytes; put details in mail first", MaxQuestion)
	}
	if len(a.Options) > MaxOptions {
		return proto.Errorf(proto.CodeBadRequest, "at most %d options", MaxOptions)
	}
	for _, o := range a.Options {
		if o == "" || len(o) > 40 {
			return proto.Errorf(proto.CodeBadRequest, "an option is 1 to 40 characters")
		}
	}
	return nil
}

func (d *Daemon) waitReply(ch chan store.Message, from store.Address, item store.Item, timeout time.Duration) (any, error) {
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	for {
		select {
		case r := <-ch:
			if r.To == from {
				it, _ := d.st.Item(item.ID)
				return AskHumanResult{Item: it, Reply: &r}, nil
			}
		case <-timer:
			return nil, proto.Errorf(proto.CodeTimeout, "no answer yet; come back with --wait-for %d", item.ID)
		}
	}
}

// askHumanClosed answers a resumed ask-human from the record.
func (d *Daemon) askHumanClosed(it store.Item) (any, error) {
	if it.ClosedBy != "answered" {
		return nil, proto.Errorf(proto.CodeNotFound, "ask-human #%d ended without an answer (%s)", it.ID, it.ClosedBy)
	}
	for _, m := range d.st.Thread(it.ThreadID) {
		if m.MID == it.Answer {
			return AskHumanResult{Item: it, Reply: &m}, nil
		}
	}
	return nil, proto.Errorf(proto.CodeNotFound, "the answer to #%d is no longer held", it.ID)
}

func (d *Daemon) inbox(c *conn, a InboxArgs) (any, error) {
	items := []store.Item{}
	for _, it := range d.st.Items(a.All) {
		if d.canSee(c, it) {
			items = append(items, it)
		}
	}
	return InboxResult{Items: items}, nil
}

// answer is the person replying to an item: a verified reply into the thread
// addressed to the asker, which the store closes as answered.
func (d *Daemon) answer(c *conn, a AnswerArgs) (any, error) {
	it, ok := d.st.Item(a.Item)
	if !ok || it.ClosedAt != 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "no open item #%d", a.Item)
	}
	by, grant, err := d.responder(c, it)
	if err != nil {
		return nil, err
	}
	if len(it.Options) > 0 {
		ok := false
		for _, o := range it.Options {
			if o == a.Text {
				ok = true
			}
		}
		if !ok {
			return nil, proto.Errorf(proto.CodeBadRequest, "answer must be one of %v", it.Options)
		}
	}
	if a.Text == "" {
		return nil, proto.Errorf(proto.CodeBadRequest, "an answer needs text")
	}
	if it.Kind == store.ItemApproval {
		closed, err := d.decideApproval(it, a.Text, a.Message, by)
		if err != nil {
			return nil, err
		}
		d.spend(grant)
		return map[string]any{"item": closed}, nil
	}
	from := store.Human
	if by != "human" {
		from = c.pl.Principal
	}
	m, err := d.store(c.pl, store.SendRequest{From: from, To: it.From, Text: a.Text, ReplyTo: it.MID,
		Proof: c.pl.Proof, Verified: by})
	if err != nil {
		return nil, err
	}
	d.spend(grant)
	closed, _ := d.st.Item(it.ID)
	return map[string]any{"item": closed, "reply": m}, nil
}

func (d *Daemon) dismiss(c *conn, a DismissArgs) (any, error) {
	open, ok := d.st.Item(a.Item)
	if !ok || open.ClosedAt != 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "no open item #%d", a.Item)
	}
	by, grant, err := d.responder(c, open)
	if err != nil {
		return nil, err
	}
	it, err := d.st.CloseItem(a.Item, "dismissed", "")
	if err != nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no open item #%d", a.Item)
	}
	it.DecidedBy = by
	d.spend(grant)
	d.publishItem(it)
	return map[string]any{"item": it}, nil
}
