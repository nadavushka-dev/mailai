package daemon

import (
	"errors"

	"github.com/nadavushka-dev/mailai/internal/proto"
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
