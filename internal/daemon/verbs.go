package daemon

import "github.com/nadavushka-dev/mailai/internal/store"

// Argument and result shapes for the verbs (SPEC §12). Field names are the
// wire format; the CLI flags map onto them.

type SendArgs struct {
	From        store.Address      `json:"from,omitempty"`
	FromLabel   string             `json:"from_label,omitempty"`
	To          store.Address      `json:"to,omitempty"`
	Session     string             `json:"session,omitempty"`
	Subject     string             `json:"subject,omitempty"`
	Text        string             `json:"text"`
	ReplyTo     string             `json:"reply_to,omitempty"`
	Attachments []store.Attachment `json:"attachments,omitempty"`
}

type SendResult struct {
	Message store.Message `json:"message"`
}

type ReadArgs struct {
	// Mailbox to read; the caller's own when empty.
	Mailbox store.Address `json:"mailbox,omitempty"`
	Unread  bool          `json:"unread,omitempty"`
	Peek    bool          `json:"peek,omitempty"`
	IDs     []uint64      `json:"ids,omitempty"`
	Thread  string        `json:"thread,omitempty"`
}

type ReadResult struct {
	Messages []store.Message `json:"messages"`
	// PeekForced says a marking read was served as a peek (SPEC §9).
	PeekForced bool `json:"peek_forced,omitempty"`
}

type UnreadArgs struct {
	Mailbox store.Address `json:"mailbox,omitempty"`
}

type UnreadResult struct {
	Count int             `json:"count"`
	From  []store.Address `json:"from,omitempty"`
}

type AskArgs struct {
	From      store.Address `json:"from,omitempty"`
	To        store.Address `json:"to"`
	Subject   string        `json:"subject,omitempty"`
	Text      string        `json:"text"`
	TimeoutMS int64         `json:"timeout_ms,omitempty"`
}

type AskResult struct {
	Ask   store.Message `json:"ask"`
	Reply store.Message `json:"reply"`
}

type WhoamiResult struct {
	Principal store.Address `json:"principal"`
	Proof     store.Proof   `json:"proof"`
	PID       int           `json:"pid,omitempty"`
}

// MailEvent is the data of an "mail" event: enough to know something
// arrived, never the body (D12 applies to agents reading events too).
type MailEvent struct {
	MID     string        `json:"mid"`
	Kind    store.Kind    `json:"kind"`
	Mailbox store.Address `json:"mailbox"`
	From    store.Address `json:"from"`
	Thread  string        `json:"thread_id"`
	ReplyTo string        `json:"reply_to,omitempty"`
}
