package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Errors are a closed set; the protocol maps each to an error code.
var (
	ErrNoSender          = errors.New("no sender")
	ErrNoRecipient       = errors.New("no recipient and no session for a notice")
	ErrEmptyText         = errors.New("empty text")
	ErrSubjectTooLong    = fmt.Errorf("subject longer than %d characters", MaxSubject)
	ErrSubjectNotOneLine = errors.New("subject must be one line")
	ErrTextTooLong       = fmt.Errorf("text longer than %d bytes", MaxText)
	ErrMailboxFull       = errors.New("mailbox_full")
	ErrKindMismatch      = errors.New("a reply cannot carry another kind")
)

// Limits bound one mailbox (SPEC §6).
type Limits struct {
	MaxMessages int
	MaxBytes    int
}

// DefaultLimits is the spec's bound: 2,000 messages or 16 MB per mailbox.
var DefaultLimits = Limits{MaxMessages: 2000, MaxBytes: 16 << 20}

// Options configure a Store.
type Options struct {
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Limits per mailbox; DefaultLimits when zero.
	Limits Limits
	// MachineID prefixes every MID; "local" when empty.
	MachineID string
}

// SendRequest is what a caller asks for. Trust fields are not here: the
// daemon sets them on the Message after the store accepts the request.
type SendRequest struct {
	From        Address
	FromLabel   string
	To          Address
	Session     string
	Subject     string
	Text        string
	Kind        Kind
	ReplyTo     string
	Attachments []Attachment
	// Remote says the send arrived over a link; the daemon sets it from the
	// connection. It picks the rate-limit bucket (SPEC §6).
	Remote bool
	// Proof, Origin and OriginHost are what the daemon knows about the
	// connection (SPEC §9); never taken from a client's request.
	Proof      Proof
	Origin     string
	OriginHost string
	Verified   string
}

// ReadQuery selects messages from one mailbox, oldest first.
type ReadQuery struct {
	Mailbox Address
	// Unread returns only messages with ReadAt == 0.
	Unread bool
	// IDs restricts to these ids when non-empty.
	IDs []uint64
	// Mark stamps ReadAt on what is returned.
	Mark bool
}

// Store is every mailbox on this machine.
type Store struct {
	mu      sync.Mutex
	now     func() time.Time
	limits  Limits
	machine string
	nextID  uint64
	boxes   map[Address]*mailbox
	// byMID finds a message in whatever box holds it, for threads.
	byMID map[string]*Message
	lim   *limiter
	p     Persister
}

type mailbox struct {
	msgs    []*Message
	bytes   int
	evicted uint64
}

// New makes an empty, memory-only Store.
func New(o Options) *Store {
	s, _ := Load(o, nil)
	return s
}

// Load makes a Store that writes through p and starts from what p holds.
// A nil p is memory only.
func Load(o Options, p Persister) (*Store, error) {
	s := newStore(o)
	if p == nil {
		return s, nil
	}
	msgs, evicted, last, err := p.LoadAll()
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		m := msgs[i]
		mb := s.box(m.box)
		mb.msgs = append(mb.msgs, &m)
		mb.bytes += m.size()
		s.byMID[m.MID] = &m
		if m.ID > s.nextID {
			s.nextID = m.ID
		}
	}
	for box, n := range evicted {
		s.box(box).evicted = n
	}
	if last > s.nextID {
		s.nextID = last
	}
	s.p = p
	return s, nil
}

func newStore(o Options) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Limits == (Limits{}) {
		o.Limits = DefaultLimits
	}
	if o.MachineID == "" {
		o.MachineID = "local"
	}
	return &Store{now: o.Now, limits: o.Limits, machine: o.MachineID, boxes: map[Address]*mailbox{}, byMID: map[string]*Message{}, lim: newLimiter()}
}

// Send validates a request and stores it in the recipient's mailbox, or the
// session's notice box when there is no recipient. It returns a copy.
func (s *Store) Send(r SendRequest) (Message, error) {
	if r.From == "" {
		return Message{}, ErrNoSender
	}
	if strings.TrimSpace(r.Text) == "" {
		return Message{}, ErrEmptyText
	}
	if len(r.Text) > MaxText {
		return Message{}, ErrTextTooLong
	}
	if strings.ContainsAny(r.Subject, "\r\n") {
		return Message{}, ErrSubjectNotOneLine
	}
	if len(r.Subject) > MaxSubject {
		return Message{}, ErrSubjectTooLong
	}
	box := r.To
	kind := r.Kind
	if r.To == "" {
		if r.Session == "" {
			return Message{}, ErrNoRecipient
		}
		box = NoticeBox(r.Session)
		kind = KindNotice
	} else if kind == "" {
		kind = KindMail
	}
	if r.ReplyTo != "" {
		if kind != KindMail && kind != KindReply {
			return Message{}, ErrKindMismatch
		}
		kind = KindReply
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lim.take(r.From, r.Remote, s.now()); err != nil {
		return Message{}, err
	}
	s.nextID++
	m := &Message{
		ID:          s.nextID,
		MID:         fmt.Sprintf("%s:%d", s.machine, s.nextID),
		Kind:        kind,
		Session:     r.Session,
		From:        r.From,
		FromLabel:   r.FromLabel,
		To:          r.To,
		Subject:     r.Subject,
		Text:        r.Text,
		ReplyTo:     r.ReplyTo,
		Attachments: append([]Attachment(nil), r.Attachments...),
		SentAt:      s.now().UnixNano(),
		SenderProof: r.Proof,
		Origin:      r.Origin,
		OriginHost:  r.OriginHost,
		Verified:    r.Verified,
	}
	// Thread id is resolved now, not walked at read time: the store is
	// bounded and forgets, and a thread must answer to one id forever.
	m.ThreadID = m.MID
	if r.ReplyTo != "" {
		if parent, ok := s.byMID[r.ReplyTo]; ok {
			m.ThreadID = parent.ThreadID
		} else {
			m.ThreadID = r.ReplyTo
			m.ReplyToMissing = true
		}
	}
	m.box = box
	mb := s.box(box)
	if err := s.makeRoom(box, mb, m.size()); err != nil {
		s.nextID--
		return Message{}, err
	}
	if s.p != nil {
		if err := s.p.Insert(box, m); err != nil {
			s.nextID--
			return Message{}, err
		}
	}
	mb.msgs = append(mb.msgs, m)
	mb.bytes += m.size()
	s.byMID[m.MID] = m
	return *m, nil
}

// PeekQuery reads without marking read. A peek by the mailbox's own
// principal marks its unread messages seen, so a sender can tell they were
// looked at; any other peek changes nothing (SPEC §6).
type PeekQuery struct {
	Mailbox Address
	By      Address
	Unread  bool
}

// Peek returns copies like Read does, stamping SeenAt only for an own-inbox
// peek.
func (s *Store) Peek(q PeekQuery) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.boxes[q.Mailbox]
	if !ok {
		return nil, nil
	}
	now := s.now().UnixNano()
	own := q.By != "" && q.By == q.Mailbox
	var out []Message
	for _, m := range mb.msgs {
		if q.Unread && m.ReadAt != 0 {
			continue
		}
		c := *m
		c.WasUnread = m.ReadAt == 0
		c.WasSeen = m.ReadAt == 0 && m.SeenAt != 0
		if own && m.ReadAt == 0 && m.SeenAt == 0 {
			m.SeenAt = now
			c.SeenAt = now
			if err := s.mark(m); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Thread returns copies of every stored message in a thread, oldest first,
// whatever mailbox holds each; nil when none is held.
func (s *Store) Thread(threadID string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, mb := range s.boxes {
		for _, m := range mb.msgs {
			if m.ThreadID == threadID {
				out = append(out, *m)
			}
		}
	}
	sortByID(out)
	return out
}

// Unread is the count of unread messages in a box and the distinct senders,
// in order of first appearance. It is what a notice line is built from (D12).
func (s *Store) Unread(box Address) (int, []Address) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.boxes[box]
	if !ok {
		return 0, nil
	}
	n := 0
	seen := map[Address]bool{}
	var from []Address
	for _, m := range mb.msgs {
		if m.ReadAt != 0 {
			continue
		}
		n++
		if !seen[m.From] {
			seen[m.From] = true
			from = append(from, m.From)
		}
	}
	return n, from
}

func sortByID(ms []Message) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].ID < ms[j-1].ID; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// Read returns copies of the messages a query selects, oldest first.
func (s *Store) Read(q ReadQuery) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.boxes[q.Mailbox]
	if !ok {
		return nil, nil
	}
	var want map[uint64]bool
	if len(q.IDs) > 0 {
		want = make(map[uint64]bool, len(q.IDs))
		for _, id := range q.IDs {
			want[id] = true
		}
	}
	now := s.now().UnixNano()
	var out []Message
	for _, m := range mb.msgs {
		if want != nil && !want[m.ID] {
			continue
		}
		if q.Unread && m.ReadAt != 0 {
			continue
		}
		c := *m
		c.WasUnread = m.ReadAt == 0
		c.WasSeen = m.ReadAt == 0 && m.SeenAt != 0
		if q.Mark && m.ReadAt == 0 {
			m.ReadAt = now
			c.ReadAt = now
			if err := s.mark(m); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) mark(m *Message) error {
	if s.p == nil {
		return nil
	}
	return s.p.Mark(m.MID, m.ReadAt, m.SeenAt)
}

// Evicted is how many messages a mailbox has dropped to stay within bounds.
func (s *Store) Evicted(box Address) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mb, ok := s.boxes[box]; ok {
		return mb.evicted
	}
	return 0
}

func (s *Store) box(a Address) *mailbox {
	mb, ok := s.boxes[a]
	if !ok {
		mb = &mailbox{}
		s.boxes[a] = mb
	}
	return mb
}

// makeRoom evicts the oldest read messages until a message of n bytes fits.
// Unread messages are never evicted; if they alone fill the box, the send is
// refused with ErrMailboxFull (SPEC §6).
func (s *Store) makeRoom(box Address, mb *mailbox, n int) error {
	lim := s.limits
	if n > lim.MaxBytes {
		return ErrMailboxFull
	}
	fits := func() bool {
		return len(mb.msgs)+1 <= lim.MaxMessages && mb.bytes+n <= lim.MaxBytes
	}
	for !fits() {
		i := -1
		for j, m := range mb.msgs {
			if m.ReadAt != 0 {
				i = j
				break
			}
		}
		if i < 0 {
			return ErrMailboxFull
		}
		victim := mb.msgs[i]
		if s.p != nil {
			if err := s.p.Evict(box, victim.MID, mb.evicted+1); err != nil {
				return err
			}
		}
		mb.bytes -= victim.size()
		delete(s.byMID, victim.MID)
		mb.msgs = append(mb.msgs[:i], mb.msgs[i+1:]...)
		mb.evicted++
	}
	return nil
}
