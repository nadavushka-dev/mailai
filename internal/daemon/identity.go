package daemon

import (
	"fmt"
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

// senderPane is the pane recorded on a message, shown beside the sender's
// name (§9): only a pane placement has one.
func senderPane(pl Placement) string {
	if pl.Proof != store.ProofPane {
		return ""
	}
	return pl.Pane
}

func (d *Daemon) whoami(c *conn) WhoamiResult {
	return WhoamiResult{Principal: d.own(c.pl), Proof: c.pl.Proof, PID: c.pl.PID, NotPerson: whyNotPerson(c.pl)}
}

// whyNotPerson says, in one line, why a caller is not the person and where
// the person's Inbox is; empty for the person.
func whyNotPerson(pl Placement) string {
	const elsewhere = "open mailai in a pane with no agent or at a plain terminal, or use mailai web"
	switch pl.Proof {
	case store.ProofOutsidePane:
		return ""
	case store.ProofPane:
		return fmt.Sprintf("this client is inside a pane running an agent (%s), so mailai treats it as that agent, not the person: %s", pl.Principal, elsewhere)
	case store.ProofAgent:
		return fmt.Sprintf("%s is running this, so mailai treats it as an agent, not the person: %s", pl.Agent, elsewhere)
	case store.ProofToken:
		return fmt.Sprintf("this is agent %s (MAILAI_TOKEN is set), not the person: unset it, or use mailai web", pl.Principal)
	case store.ProofDetached:
		return "no terminal, so not the person: run it at a terminal (ssh -t), or use mailai web"
	}
	return "not the person: use mailai web"
}

// notPerson says why a caller with no name of its own is not the person,
// and what to do about it.
func notPerson(pl Placement) string {
	if pl.Proof == store.ProofAgent {
		return pl.Agent + " is running this, outside every pane and with no token, so it is an agent with no name: " +
			"pass --from <name>, or start the agent with MAILAI_TOKEN (mailai token new <name>, from the person's terminal)"
	}
	return "this caller has no terminal and is not the person; pass --from <name>"
}

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
	if pl.Proof == store.ProofDetached || pl.Proof == store.ProofAgent {
		// Not the person, so it must say who it is (§9).
		if claimed == "" || claimed == store.Human {
			return "", errForbidden("%s", notPerson(pl))
		}
		return claimed, nil
	}
	if claimed == "" {
		return store.Human, nil
	}
	return claimed, nil
}
