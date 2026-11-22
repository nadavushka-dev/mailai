package store

import (
	"fmt"
	"strings"
)

// The spoke outbox (SPEC §13a): what a spoke's agents send off the machine
// is queued here first, with a mid minted from this store's counter, and
// leaves the queue once the inbox machine has it. It survives a restart.

// Outgoing is one queued off-machine send.
type Outgoing struct {
	ID       uint64   `json:"id"`
	MID      string   `json:"mid"`
	ThreadID string   `json:"thread_id"`
	Kind     Kind     `json:"kind"`
	From     Address  `json:"from"`
	To       Address  `json:"to"`
	Subject  string   `json:"subject,omitempty"`
	Text     string   `json:"text"`
	ReplyTo  string   `json:"reply_to,omitempty"`
	Options  []string `json:"options,omitempty"`
	// TimeoutMS is an ask's wait, so the inbox machine can bound its edge.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	QueuedAt  int64 `json:"queued_at"`
}

// OutboxPersister is implemented by persisters that keep the outbox.
type OutboxPersister interface {
	LoadOutbox() ([]Outgoing, error)
	// PutOutgoing stores o and records o.ID as the last id handed out.
	PutOutgoing(o *Outgoing) error
	DeleteOutgoing(id uint64) error
}

// validate is what Send refuses in any message.
func validate(r SendRequest) error {
	if r.From == "" {
		return ErrNoSender
	}
	if strings.TrimSpace(r.Text) == "" {
		return ErrEmptyText
	}
	if len(r.Text) > MaxText {
		return ErrTextTooLong
	}
	if strings.ContainsAny(r.Subject, "\r\n") {
		return ErrSubjectNotOneLine
	}
	if len(r.Subject) > MaxSubject {
		return ErrSubjectTooLong
	}
	return nil
}

// Enqueue validates o, gives it the next id and a mid, resolves its thread
// from what this store holds, and queues it durably.
func (s *Store) Enqueue(o Outgoing) (Outgoing, error) {
	if err := validate(SendRequest{From: o.From, Text: o.Text, Subject: o.Subject}); err != nil {
		return Outgoing{}, err
	}
	if o.To == "" {
		return Outgoing{}, ErrNoRecipient
	}
	if o.Kind == "" {
		o.Kind = KindMail
	}
	if o.ReplyTo != "" {
		if o.Kind != KindMail && o.Kind != KindReply {
			return Outgoing{}, ErrKindMismatch
		}
		o.Kind = KindReply
	}
	o.Options = append([]string(nil), o.Options...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	o.ID = s.nextID
	o.MID = fmt.Sprintf("%s:%d", s.machine, o.ID)
	o.ThreadID = o.MID
	if o.ReplyTo != "" {
		o.ThreadID = o.ReplyTo
		if parent, ok := s.byMID[o.ReplyTo]; ok {
			o.ThreadID = parent.ThreadID
		}
	}
	o.QueuedAt = s.now().UnixNano()
	if op, ok := s.p.(OutboxPersister); ok {
		if err := op.PutOutgoing(&o); err != nil {
			s.nextID--
			return Outgoing{}, err
		}
	}
	s.outbox = append(s.outbox, o)
	return o, nil
}

// Outbox is what waits to be sent, oldest first.
func (s *Store) Outbox() []Outgoing {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Outgoing(nil), s.outbox...)
}

// Dequeue drops a sent (or refused) entry.
func (s *Store) Dequeue(id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, o := range s.outbox {
		if o.ID != id {
			continue
		}
		if op, ok := s.p.(OutboxPersister); ok {
			if err := op.DeleteOutgoing(id); err != nil {
				return err
			}
		}
		s.outbox = append(s.outbox[:i], s.outbox[i+1:]...)
		return nil
	}
	return nil
}
