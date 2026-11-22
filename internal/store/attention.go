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

// ClosedInSession is how an item closes when the person answered in the
// agent's own session instead of the Inbox (SPEC §7a).
const ClosedInSession = "in_session"

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
	// ClosedBy says how: "answered", "dismissed", "resolved" (the agent
	// moved on), "timeout" (an approval nobody decided), "in_session" (the
	// person answered in the agent's own session, §7a).
	ClosedBy string `json:"closed_by,omitempty"`
	// MaybeInSessionAt is set on an open ask when the agent moved on in a way
	// that suggests the person answered in its session (§7a). The item stays
	// open and answerable.
	MaybeInSessionAt int64 `json:"maybe_in_session_at,omitempty"`
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
	return s.closeItem(id, by, answer, "")
}

// CloseItemBy closes an item and records who decided: human or
// supervisor:<name>.
func (s *Store) CloseItemBy(id uint64, by, answer, decidedBy string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeItem(id, by, answer, decidedBy)
}

func (s *Store) closeItem(id uint64, by, answer, decidedBy string) (Item, error) {
	it, ok := s.att.items[id]
	if !ok || it.ClosedAt != 0 {
		return Item{}, ErrNoItem
	}
	it.ClosedAt = s.now().UnixNano()
	it.ClosedBy = by
	it.Answer = answer
	it.DecidedBy = decidedBy
	if err := s.att.put(it); err != nil {
		return Item{}, err
	}
	return *it, nil
}

// MarkMaybeInSession stamps the open asks of from (ask and ask_human, not
// approvals) as maybe answered in its session, once, and returns the items
// it stamped (§7a).
func (s *Store) MarkMaybeInSession(from Address) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	now := s.now().UnixNano()
	for _, it := range s.att.items {
		if it.ClosedAt != 0 || it.From != from || it.MaybeInSessionAt != 0 {
			continue
		}
		if it.Kind != ItemAsk && it.Kind != ItemAskHuman {
			continue
		}
		it.MaybeInSessionAt = now
		if err := s.att.put(it); err != nil {
			it.MaybeInSessionAt = 0
			continue
		}
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RecentlyClosed returns the items closed at or after since, newest first.
func (s *Store) RecentlyClosed(since int64) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.att.items {
		if it.ClosedAt != 0 && it.ClosedAt >= since {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClosedAt != out[j].ClosedAt {
			return out[i].ClosedAt > out[j].ClosedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// ItemsInThread returns every item, open or closed, opened in a thread.
func (s *Store) ItemsInThread(thread string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.att.items {
		if thread != "" && it.ThreadID == thread {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// itemFor is the open item a message opened, if any.
func (s *Store) itemFor(mid string) *Item {
	it := s.att.byMID[mid]
	if it == nil || it.ClosedAt != 0 {
		return nil
	}
	return it
}
