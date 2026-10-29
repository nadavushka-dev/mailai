package store

import (
	"errors"
	"sort"
	"strings"
)

// Grants (SPEC §10): what a principal may do through mailai beyond its own
// mailbox. A supervisor is an agent holding respond over a scope.

// Right is one of the four.
type Right string

const (
	RightRead    Right = "read"
	RightWrite   Right = "write"
	RightRespond Right = "respond"
	RightAdmin   Right = "admin"
)

// Grant is one record.
type Grant struct {
	ID        uint64  `json:"id"`
	Principal Address `json:"principal"`
	Rights    []Right `json:"rights"`
	// Scope: "all", "session:<name>", or a principal's address.
	Scope     string  `json:"scope"`
	GrantedBy Address `json:"granted_by"`
	// Parent is the grant this one was delegated from; 0 when the person
	// gave it. A delegated grant cannot be delegated again (depth 2).
	Parent uint64 `json:"parent,omitempty"`
	// Once removes the grant after its first use to answer.
	Once      bool  `json:"once,omitempty"`
	CreatedAt int64 `json:"created_at"`
}

var (
	ErrBadRight = errors.New("rights are read, write, respond, admin")
	ErrNoGrant  = errors.New("no such grant")
)

// ParseRights parses "read,respond".
func ParseRights(s string) ([]Right, error) {
	var out []Right
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		switch Right(p) {
		case RightRead, RightWrite, RightRespond, RightAdmin:
			out = append(out, Right(p))
		case "":
		default:
			return nil, ErrBadRight
		}
	}
	if len(out) == 0 {
		return nil, ErrBadRight
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Has says whether a right is in the set.
func (g Grant) Has(r Right) bool {
	for _, x := range g.Rights {
		if x == r {
			return true
		}
	}
	return false
}

// SessionOf answers which session a principal is in; the daemon's agent
// registry provides it. Unknown is "".
type SessionOf func(Address) string

// Covers says whether a scope includes a principal.
func Covers(scope string, p Address, sessionOf SessionOf) bool {
	switch {
	case scope == "all":
		return true
	case strings.HasPrefix(scope, "session:"):
		return sessionOf != nil && sessionOf(p) != "" && sessionOf(p) == scope[len("session:"):]
	default:
		return Address(scope) == p
	}
}

// Narrower says whether child is within parent (SPEC §10: delegation is
// narrower only). Equal counts as within.
func Narrower(child, parent Grant, sessionOf SessionOf) bool {
	for _, r := range child.Rights {
		if !parent.Has(r) {
			return false
		}
	}
	switch {
	case parent.Scope == "all":
		return true
	case strings.HasPrefix(parent.Scope, "session:"):
		if child.Scope == parent.Scope {
			return true
		}
		if strings.HasPrefix(child.Scope, "session:") || child.Scope == "all" {
			return false
		}
		return Covers(parent.Scope, Address(child.Scope), sessionOf)
	default:
		return child.Scope == parent.Scope
	}
}

// GrantPersister stores grants; the SQLite persister implements it too.
type GrantPersister interface {
	LoadGrants() ([]Grant, uint64, error)
	PutGrant(g *Grant) error
	DeleteGrant(id uint64) error
}

type grants struct {
	byID   map[uint64]*Grant
	nextID uint64
	p      GrantPersister
}

func newGrants() *grants { return &grants{byID: map[uint64]*Grant{}} }

func (g *grants) load(p GrantPersister) error {
	list, last, err := p.LoadGrants()
	if err != nil {
		return err
	}
	for i := range list {
		x := list[i]
		g.byID[x.ID] = &x
		if x.ID > g.nextID {
			g.nextID = x.ID
		}
	}
	if last > g.nextID {
		g.nextID = last
	}
	g.p = p
	return nil
}

// AddGrant stores a grant and returns it with its id.
func (s *Store) AddGrant(g Grant) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants.nextID++
	g.ID = s.grants.nextID
	g.CreatedAt = s.now().UnixNano()
	if s.grants.p != nil {
		if err := s.grants.p.PutGrant(&g); err != nil {
			s.grants.nextID--
			return Grant{}, err
		}
	}
	s.grants.byID[g.ID] = &g
	return g, nil
}

// RemoveGrant deletes a grant and everything delegated from it.
func (s *Store) RemoveGrant(id uint64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.grants.byID[id]; !ok {
		return 0, ErrNoGrant
	}
	n := 0
	var rm func(uint64) error
	rm = func(id uint64) error {
		for cid, c := range s.grants.byID {
			if c.Parent == id {
				if err := rm(cid); err != nil {
					return err
				}
			}
		}
		if s.grants.p != nil {
			if err := s.grants.p.DeleteGrant(id); err != nil {
				return err
			}
		}
		delete(s.grants.byID, id)
		n++
		return nil
	}
	return n, rm(id)
}

// Grants lists every grant by id.
func (s *Store) Grants() []Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Grant, 0, len(s.grants.byID))
	for _, g := range s.grants.byID {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Grant returns one grant.
func (s *Store) Grant(id uint64) (Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants.byID[id]
	if !ok {
		return Grant{}, false
	}
	return *g, true
}

// GrantsOf lists a principal's grants.
func (s *Store) GrantsOf(p Address) []Grant {
	var out []Grant
	for _, g := range s.Grants() {
		if g.Principal == p {
			out = append(out, g)
		}
	}
	return out
}

// Allowed says whether principal holds right r over target, and returns the
// grant that allows it.
func (s *Store) Allowed(principal Address, r Right, target Address, sessionOf SessionOf) (Grant, bool) {
	for _, g := range s.GrantsOf(principal) {
		if g.Has(r) && Covers(g.Scope, target, sessionOf) {
			return g, true
		}
	}
	return Grant{}, false
}
