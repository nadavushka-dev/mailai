package store

import (
	"errors"
	"sort"
)

// Attention items are the Human Inbox (D13): everything waiting on the
// person. One item per open question; it closes when answered or
// dismissed. Items are derived from messages and kept in their own table so
// a dismissal survives and a list is one query.

// ItemKind is the closed set of what can wait on the person.
type ItemKind string

const (
	ItemAsk      ItemKind = "ask"       // an ask addressed to human
	ItemAskHuman ItemKind = "ask_human" // ask-human: a question with fixed answers
	ItemErrored  ItemKind = "errored"   // an agent the adapter reports errored (M3)
	ItemApproval ItemKind = "approval"  // a harness permission request held by its hook
)

// Item is one entry in the Inbox.
type Item struct {
	ID       uint64   `json:"id"`
	Kind     ItemKind `json:"kind"`
	MID      string   `json:"mid,omitempty"`
	ThreadID string   `json:"thread_id,omitempty"`
	From     Address  `json:"from"`
	Session  string   `json:"session,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	// Question is the text shown in the list; for mail kinds it is the body.
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	OpenedAt int64    `json:"opened_at"`
	ClosedAt int64    `json:"closed_at,omitempty"`
	// ClosedBy says how: "answered", "dismissed", "resolved" (the agent moved on).
	ClosedBy string `json:"closed_by,omitempty"`
	// Answer is the mid of the reply that closed it, when answered; for an
	// approval, the decision ("allow" or "deny").
	Answer string `json:"answer,omitempty"`
	// Approval fields (SPEC §8): what the harness said.
	Harness string `json:"harness,omitempty"`
	Risky   bool   `json:"risky,omitempty"`
	// DecidedBy says who answered: human or supervisor:<name> (M4).
	DecidedBy string `json:"decided_by,omitempty"`
}

var ErrNoItem = errors.New("no such item")

// AttentionPersister stores items; the SQLite persister implements it too.
type AttentionPersister interface {
	LoadItems() ([]Item, uint64, error)
	PutItem(it *Item) error
}

type attention struct {
	items  map[uint64]*Item
	byMID  map[string]*Item
	nextID uint64
	p      AttentionPersister
}

func newAttention() *attention {
	return &attention{items: map[uint64]*Item{}, byMID: map[string]*Item{}}
}

func (a *attention) load(p AttentionPersister) error {
	items, last, err := p.LoadItems()
	if err != nil {
		return err
	}
	for i := range items {
		it := items[i]
		a.items[it.ID] = &it
		if it.MID != "" {
			a.byMID[it.MID] = &it
		}
		if it.ID > a.nextID {
			a.nextID = it.ID
		}
	}
	if last > a.nextID {
		a.nextID = last
	}
	a.p = p
	return nil
}

func (a *attention) put(it *Item) error {
	if a.p != nil {
		return a.p.PutItem(it)
	}
	return nil
}

// Open opens an item; called by the Store under its lock.
func (s *Store) openItem(it Item) (Item, error) {
	a := s.att
	a.nextID++
	it.ID = a.nextID
	it.OpenedAt = s.now().UnixNano()
	if err := a.put(&it); err != nil {
		a.nextID--
		return Item{}, err
	}
	a.items[it.ID] = &it
	if it.MID != "" {
		a.byMID[it.MID] = &it
	}
	return it, nil
}

// OpenApproval opens an approval item for a held permission request.
func (s *Store) OpenApproval(it Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openItem(it)
}

// Items returns open items oldest first, or every item when all is set.
func (s *Store) Items(all bool) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(s.att.items))
	for _, it := range s.att.items {
		if all || it.ClosedAt == 0 {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Item returns one item by id.
func (s *Store) Item(id uint64) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.att.items[id]
	if !ok {
		return Item{}, false
	}
	return *it, true
}

// CloseItem closes an open item with a reason; ErrNoItem for an unknown or
// already closed one.
func (s *Store) CloseItem(id uint64, by, answer string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeItem(id, by, answer)
}

func (s *Store) closeItem(id uint64, by, answer string) (Item, error) {
	it, ok := s.att.items[id]
	if !ok || it.ClosedAt != 0 {
		return Item{}, ErrNoItem
	}
	it.ClosedAt = s.now().UnixNano()
	it.ClosedBy = by
	it.Answer = answer
	if err := s.att.put(it); err != nil {
		return Item{}, err
	}
	return *it, nil
}

// itemFor is the open item a message opened, if any.
func (s *Store) itemFor(mid string) *Item {
	it := s.att.byMID[mid]
	if it == nil || it.ClosedAt != 0 {
		return nil
	}
	return it
}
