package daemon

import (
	"errors"
	"fmt"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Grants and supervisors (SPEC §10, D5). The person grants; a holder may
// delegate narrower, once (depth 2), silently; a pane cannot grant itself
// or widen anything. A supervisor's answer is verified as supervisor:<name>.

type GrantArgs struct {
	Principal store.Address `json:"principal"`
	Rights    string        `json:"rights"`
	Scope     string        `json:"scope,omitempty"`
	Once      bool          `json:"once,omitempty"`
}

type GrantListResult struct {
	Grants []store.Grant `json:"grants"`
}

type GrantRmArgs struct {
	ID uint64 `json:"id"`
}

// sessionOf answers from the agent registry.
func (d *Daemon) sessionOf() store.SessionOf {
	return func(a store.Address) string {
		d.mu.Lock()
		defer d.mu.Unlock()
		if r, ok := d.agents.byName[a]; ok {
			return r.Session
		}
		return ""
	}
}

func (d *Daemon) grant(c *conn, a GrantArgs) (any, error) {
	rights, err := store.ParseRights(a.Rights)
	if err != nil {
		return nil, proto.Errorf(proto.CodeBadRequest, "%v", err)
	}
	if a.Principal == "" || a.Principal == store.Human {
		return nil, proto.Errorf(proto.CodeBadRequest, "grant needs an agent principal")
	}
	scope := a.Scope
	if scope == "" {
		scope = "all"
	}
	g := store.Grant{Principal: a.Principal, Rights: rights, Scope: scope, Once: a.Once}
	switch {
	case isPerson(c.pl):
		g.GrantedBy = store.Human
	case c.pl.Principal != "":
		// Delegation: narrower than one of the caller's own undelegated grants.
		if c.pl.Principal == a.Principal {
			return nil, errForbidden("a pane cannot grant itself")
		}
		for _, r := range rights {
			if r == store.RightAdmin {
				return nil, errForbidden("admin is the person's alone")
			}
		}
		var parent *store.Grant
		for _, own := range d.st.GrantsOf(c.pl.Principal) {
			own := own
			if own.Parent != 0 {
				continue // a delegated grant cannot be delegated again
			}
			if store.Narrower(g, own, d.sessionOf()) {
				parent = &own
				break
			}
		}
		if parent == nil {
			return nil, errForbidden("%s holds no undelegated grant that covers %s over %s", c.pl.Principal, a.Rights, scope)
		}
		g.GrantedBy, g.Parent = c.pl.Principal, parent.ID
	default:
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person, or a grant holder delegating narrower, grants")
	}
	stored, err := d.st.AddGrant(g)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

func (d *Daemon) grantRm(c *conn, a GrantRmArgs) (any, error) {
	g, ok := d.st.Grant(a.ID)
	if !ok {
		return nil, proto.Errorf(proto.CodeNotFound, "no grant #%d", a.ID)
	}
	if !isPerson(c.pl) && g.GrantedBy != c.pl.Principal {
		return nil, errForbidden("only the person or the granter revokes a grant")
	}
	n, err := d.st.RemoveGrant(a.ID)
	if errors.Is(err, store.ErrNoGrant) {
		return nil, proto.Errorf(proto.CodeNotFound, "no grant #%d", a.ID)
	}
	if err != nil {
		return nil, err
	}
	return map[string]int{"removed": n}, nil
}

// responder says who the caller is when answering an item: the person, or a
// supervisor whose respond grant covers the asker. It returns the verified
// mark and the grant used. Risky items (SPEC §8) wait for the person.
func (d *Daemon) responder(c *conn, it store.Item) (string, *store.Grant, error) {
	if isPerson(c.pl) {
		return "human", nil, nil
	}
	if c.pl.Principal == "" {
		return "", nil, proto.Errorf(proto.CodeNotHuman, "only the person answers the Inbox")
	}
	if c.pl.Principal == it.From {
		return "", nil, proto.Errorf(proto.CodeNotHuman, "an agent cannot answer its own question")
	}
	g, ok := d.st.Allowed(c.pl.Principal, store.RightRespond, it.From, d.sessionOf())
	if !ok {
		return "", nil, proto.Errorf(proto.CodeNotHuman, "%s holds no respond grant over %s", c.pl.Principal, it.From)
	}
	if it.Risky {
		return "", nil, proto.Errorf(proto.CodeNotHuman, "the harness marked this request risky; it waits for the person")
	}
	return "supervisor:" + string(c.pl.Principal), &g, nil
}

// spend removes a --once grant after it answered.
func (d *Daemon) spend(g *store.Grant) {
	if g != nil && g.Once {
		d.st.RemoveGrant(g.ID)
	}
}

// canSee says whether a caller may list an item (SPEC §10: read over the
// asker, or the person). An agent with no grants sees items from its own
// session, which is the default read scope.
func (d *Daemon) canSee(c *conn, it store.Item) bool {
	if isPerson(c.pl) {
		return true
	}
	if c.pl.Principal == "" {
		return false
	}
	if c.pl.Principal == it.From {
		return true
	}
	if _, ok := d.st.Allowed(c.pl.Principal, store.RightRead, it.From, d.sessionOf()); ok {
		return true
	}
	so := d.sessionOf()
	mine := so(c.pl.Principal)
	return mine != "" && mine == so(it.From)
}

func describeGrant(g store.Grant) string {
	s := fmt.Sprintf("#%d %s: %v over %s (by %s", g.ID, g.Principal, g.Rights, g.Scope, g.GrantedBy)
	if g.Parent != 0 {
		s += fmt.Sprintf(", delegated from #%d", g.Parent)
	}
	if g.Once {
		s += ", once"
	}
	return s + ")"
}
