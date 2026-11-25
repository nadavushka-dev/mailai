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
	// Queued says a spoke holds it in its outbox until the link is back.
	Queued bool `json:"queued,omitempty"`
}

type ReadArgs struct {
	// Mailbox to read; the caller's own when empty.
	Mailbox store.Address `json:"mailbox,omitempty"`
	Unread  bool          `json:"unread,omitempty"`
	Peek    bool          `json:"peek,omitempty"`
	IDs     []uint64      `json:"ids,omitempty"`
	Thread  string        `json:"thread,omitempty"`
	// Message is one message: the number in its fence header (54 or #54)
	// or its mid, resolved as reply resolves it (SPEC §12).
	Message string `json:"message,omitempty"`
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

// HelloArgs is sent once after connecting; the token, if any, re-places
// the connection. The client sends it from MAILAI_TOKEN.
type HelloArgs struct {
	Token string `json:"token,omitempty"`
}

type TokenNewArgs struct {
	Name  store.Address `json:"name"`
	Label string        `json:"label,omitempty"`
}

type TokenNewResult struct {
	Name  store.Address `json:"name"`
	Token string        `json:"token"`
}

type TokenRevokeArgs struct {
	Name store.Address `json:"name"`
}

type WhoamiResult struct {
	Principal store.Address `json:"principal"`
	Proof     store.Proof   `json:"proof"`
	PID       int           `json:"pid,omitempty"`
	// NotPerson, for a caller that is not the person, says why and what to
	// do: one line for the CLI and the TUI to show instead of an empty Inbox.
	NotPerson string `json:"not_person,omitempty"`
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
