package daemon

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
)

// Token verbs (SPEC §9). Only the person issues or revokes tokens: a pane
// that could mint a token could mint an identity.
func (d *Daemon) tokenVerb(c *conn, verb string, raw []byte) (any, error) {
	if d.tokens == nil {
		return nil, proto.Errorf(proto.CodeInternal, "this daemon has no token file")
	}
	if !isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person, from outside every pane, manages tokens")
	}
	switch verb {
	case "token-new":
		a, err := decode[TokenNewArgs](raw)
		if err != nil {
			return nil, err
		}
		pane, live, err := d.liveAgentNamed(a.Name)
		if err != nil {
			return nil, err
		}
		if live {
			return nil, proto.Errorf(proto.CodeBadRequest, "%q is the name of a live Herdr agent (%s); pick another name", a.Name, pane)
		}
		tok, err := d.tokens.New(a.Name, a.Label)
		if errors.Is(err, tokens.ErrBadName) {
			return nil, proto.Errorf(proto.CodeBadRequest, "%v", err)
		}
		if err != nil {
			return nil, err
		}
		return TokenNewResult{Name: a.Name, Token: tok}, nil
	case "token-list":
		list := d.tokens.List()
		if list == nil {
			list = []tokens.Record{}
		}
		return map[string]any{"tokens": list}, nil
	case "token-revoke":
		a, err := decode[TokenRevokeArgs](raw)
		if err != nil {
			return nil, err
		}
		n, err := d.tokens.Revoke(a.Name)
		if errors.Is(err, tokens.ErrNotFound) {
			return nil, proto.Errorf(proto.CodeNotFound, "no token for %s", a.Name)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"revoked": n}, nil
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", verb)
}

// liveAgentNamed asks the adapter whether a live agent has this name now,
// and in which pane (SPEC §9: token names are reserved). When the adapter
// cannot say, token-new fails closed: no token is issued, and one line
// names the cause.
func (d *Daemon) liveAgentNamed(name store.Address) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	list, err := d.adapter.Agents(ctx)
	if err != nil {
		d.log.Printf("token-new %s: adapter %s: %v", name, d.adapter.Name(), err)
		return "", false, proto.Errorf(proto.CodeOffline, "no token issued: adapter %s cannot say whether a live agent is named %q: %s",
			d.adapter.Name(), name, strings.Join(strings.Fields(err.Error()), " "))
	}
	for _, a := range list {
		if a.Name != "" && store.Address(a.Name) == name {
			return a.PaneID, true, nil
		}
	}
	return "", false, nil
}
