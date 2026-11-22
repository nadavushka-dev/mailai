package daemon

import (
	"net"

	"github.com/nadavushka-dev/mailai/internal/identity"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Placement is what the daemon knows about a connection's caller (SPEC §9).
type Placement = identity.Placement

// Placer places a connection, once at connect and again when the client
// says hello with a token. The default treats every caller as
// outside_pane, which on a single-user box is the person.
type Placer interface {
	Place(conn net.Conn, token string) Placement
}

type outsidePlacer struct{}

func (outsidePlacer) Place(net.Conn, string) Placement {
	return Placement{Proof: store.ProofOutsidePane}
}

// isPerson says whether a placement may act as the person (SPEC §9):
// outside every pane, with no token. Milestone 5 adds the attached client.
func isPerson(pl Placement) bool { return pl.Proof == store.ProofOutsidePane }

// resolveFrom decides the sender address for a request: a placed principal
// wins over the claim; an outside_pane caller may claim any address, "human"
// included; a claim of nothing from outside is "human".
func resolveFrom(pl Placement, claimed store.Address) (store.Address, error) {
	if pl.Principal != "" {
		if claimed != "" && claimed != pl.Principal {
			return "", errForbidden("--from %q: this connection is %q", claimed, pl.Principal)
		}
		return pl.Principal, nil
	}
	if pl.Proof == store.ProofDetached {
		// No terminal: not the person, so it must say who it is (§9).
		if claimed == "" || claimed == store.Human {
			return "", errForbidden("this caller has no terminal and is not the person; pass --from <name>")
		}
		return claimed, nil
	}
	if claimed == "" {
		return store.Human, nil
	}
	return claimed, nil
}
