package daemon

import (
	"net"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Placement is what the daemon knows about a connection's caller (SPEC §9).
type Placement struct {
	Proof store.Proof
	// Principal is the caller's own address when the proof fixes it (a pane
	// or a token); empty for outside_pane, where --from is honoured.
	Principal store.Address
	PID       int
}

// Placer places a connection. Milestone 2 brings peer pids and tokens; the
// default treats every caller as outside_pane, which on a single-user box
// is the person.
type Placer interface {
	Place(conn net.Conn) Placement
}

type outsidePlacer struct{}

func (outsidePlacer) Place(net.Conn) Placement { return Placement{Proof: store.ProofOutsidePane} }

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
	if claimed == "" {
		return store.Human, nil
	}
	return claimed, nil
}
