// Package store holds mailboxes: the daemon's record of every message, who
// has read it, and what is waiting for an answer. It is pure: no sockets, no
// clocks of its own, no files. Persistence is a Persister the daemon plugs in.
package store

// Address names a principal: "reviewer", "w1:p3", "human", or with a machine,
// "reviewer@devbox". The store does not interpret it beyond equality, except
// for NoticeBox keys.
type Address string

// Kind is the closed set of message kinds (SPEC §6).
type Kind string

const (
	KindMail     Kind = "mail"
	KindNotice   Kind = "notice"
	KindAsk      Kind = "ask"
	KindAskHuman Kind = "ask_human"
	KindReply    Kind = "reply"
	KindReview   Kind = "review"
)

// Bounds on what a sender may put in a message (SPEC §6, D12).
const (
	MaxSubject = 120
	MaxText    = 64 << 10
)

// Proof is how the daemon placed the sender's connection (SPEC §9). The store
// records it; the identity package decides it.
type Proof string

const (
	ProofPane        Proof = "pane"
	ProofOutsidePane Proof = "outside_pane"
	ProofLink        Proof = "link"
	ProofToken       Proof = "token"
)

// Attachment is a reference to a file, never its bytes (SPEC §6).
type Attachment struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	MediaType string `json:"media_type,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Stashed   bool   `json:"stashed,omitempty"`
	// Missing is resolved at read time, never trusted from send time.
	Missing bool `json:"missing,omitempty"`
}

// Message is one immutable record (SPEC §6). Fields after SentAt are set by
// the daemon and never taken from a request.
type Message struct {
	ID          uint64       `json:"id"`
	MID         string       `json:"mid"`
	Kind        Kind         `json:"kind"`
	Session     string       `json:"session,omitempty"`
	From        Address      `json:"from"`
	FromLabel   string       `json:"from_label,omitempty"`
	To          Address      `json:"to,omitempty"`
	Subject     string       `json:"subject,omitempty"`
	Text        string       `json:"text"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	ThreadID    string       `json:"thread_id"`
	Attachments []Attachment `json:"attachments,omitempty"`
	SentAt      int64        `json:"sent_at"`

	Origin        string `json:"origin,omitempty"`
	OriginHost    string `json:"origin_host,omitempty"`
	SenderProof   Proof  `json:"sender_proof,omitempty"`
	Verified      string `json:"verified,omitempty"`
	ClaimedHuman  bool   `json:"claimed_human,omitempty"`
	ReadAt        int64  `json:"read_at,omitempty"`
	SeenAt        int64  `json:"seen_at,omitempty"`
	Undeliverable bool   `json:"undeliverable,omitempty"`

	// ReplyToMissing says ReplyTo named a message the mailbox no longer held
	// when this one was threaded, so the thread was rooted at the parent's id.
	ReplyToMissing bool `json:"reply_to_missing,omitempty"`

	// WasUnread and WasSeen describe the state before the read that returned
	// this copy. ReadAt cannot say it on the call that sets it.
	WasUnread bool `json:"was_unread,omitempty"`
	WasSeen   bool `json:"was_seen,omitempty"`

	// box is the mailbox holding this message; set by the store, not stored
	// in the JSON body (the row has its own column).
	box Address
}

// size is what a message costs against a mailbox's byte bound: the bytes
// the sender chose. Fixed fields are not counted; MaxMessages bounds those.
func (m *Message) size() int {
	n := len(m.Text) + len(m.Subject)
	for _, a := range m.Attachments {
		n += len(a.Path)
	}
	return n
}

// NoticeBox is the mailbox key that holds a session's notices.
func NoticeBox(session string) Address { return Address("session:" + session) }
