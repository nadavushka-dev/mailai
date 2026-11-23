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
	WaitFor uint64 `json:"wait_for,omitempty"`
	// Replaces withdraws the asker's own open question #Replaces as this
	// one opens (SPEC §7a).
	Replaces  uint64 `json:"replaces,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	// Attachments are files for the person to look at: references, shown
	// on the Inbox item (SPEC §6).
	Attachments []store.Attachment `json:"attachments,omitempty"`
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
	// Closed lists what closed in the last hour instead, newest first
	// (SPEC §7a).
	Closed bool `json:"closed,omitempty"`
	// Thread lists every item, open or closed, opened in that thread.
	Thread string `json:"thread,omitempty"`
}

// RecentWindow is how far back "recently closed" looks.
const RecentWindow = time.Hour

type InboxResult struct {
	Items []store.Item `json:"items"`
	// NotPerson: as on whoami, for a caller that is not the person.
	NotPerson string `json:"not_person,omitempty"`
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
		if len(a.Attachments) > 0 {
			return nil, proto.Errorf(proto.CodeBadRequest, "attachments do not cross machines yet")
		}
		if a.Replaces != 0 {
			return nil, proto.Errorf(proto.CodeBadRequest, "--replaces from a spoke comes later; ask without it")
		}
		return d.askHumanRemote(c.gone, from, a)
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
		atts, err := d.prepareAttachments(a.Attachments)
		if err != nil {
			return nil, err
		}
		if a.Replaces != 0 {
			if err := d.withdrawable(from, a.Replaces); err != nil {
				return nil, err
			}
			d.closeWithdrawn(a.Replaces)
		}
		if err := d.checkAsk(from, store.Human); err != nil {
			return nil, err
		}
		defer d.releaseAsk(from, store.Human)
		ch := make(chan store.Message, 1)
		d.mu.Lock()
		m, err := d.st.Send(store.SendRequest{From: from, To: store.Human, Subject: a.Subject, Text: a.Text, Options: a.Options,
			Kind: store.KindAskHuman, Proof: c.pl.Proof, SenderPane: senderPane(c.pl), Attachments: atts})
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
		// Only this blocking connection's death says the person answered
		// in the session (SPEC §7a).
		return d.waitReply(ch, from, item, timeout, c.gone)
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
	return d.waitReply(ch, from, item, timeout, nil)
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

// waitReply waits for the answer; when gone closes first, the ask dropped
// (withdrawn, or answered in the session if the person types there next,
// §7a); when the asker withdraws the item, the wait ends.
func (d *Daemon) waitReply(ch chan store.Message, from store.Address, item store.Item, timeout time.Duration, gone <-chan struct{}) (any, error) {
	withdrawn, unwatch := d.watchItem(item.ID)
	defer unwatch()
	if it, _ := d.st.Item(item.ID); it.ClosedBy == store.ClosedWithdrawn {
		return nil, proto.Errorf(proto.CodeNotFound, "#%d was withdrawn", item.ID)
	}
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
		case <-withdrawn:
			return nil, proto.Errorf(proto.CodeNotFound, "#%d was withdrawn", item.ID)
		case <-gone:
			d.askDropped(item.ID, from)
			return nil, errClientGone
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
	var list []store.Item
	switch {
	case a.Thread != "":
		list = d.st.ItemsInThread(a.Thread)
	case a.Closed:
		list = d.st.RecentlyClosed(d.now().Add(-RecentWindow).UnixNano())
	default:
		list = d.st.Items(a.All)
	}
	items := []store.Item{}
	for _, it := range list {
		if d.canSee(c, it) {
			if m, ok := d.st.Get(it.MID); ok && len(m.Attachments) > 0 {
				it.Attachments = append([]store.Attachment(nil), m.Attachments...)
				resolveFiles(it.Attachments)
			}
			items = append(items, it)
		}
	}
	return InboxResult{Items: items, NotPerson: whyNotPerson(c.pl)}, nil
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
	it, err := d.st.CloseItemBy(a.Item, "dismissed", "", by)
	if err != nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no open item #%d", a.Item)
	}
	d.spend(grant)
	d.publishItem(it)
	return map[string]any{"item": it}, nil
}
