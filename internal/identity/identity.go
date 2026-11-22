// Package identity places a connection (SPEC §9): the kernel says which
// process is calling, the adapter says which processes belong to panes, the
// token file says which tokens name which agents. The request never gets a
// say.
package identity

import (
	"net"

	"github.com/nadavushka-dev/mailai/internal/procinfo"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Panes is what an adapter knows about panes; nil means no panes exist.
type Panes interface {
	// PaneOfPID returns the principal of the pane whose shell (or any
	// process in it) has this pid, when the pid is one the adapter has
	// placed. The identity package walks ancestry, so it asks about every
	// ancestor of the caller.
	PaneOfPID(pid int) (store.Address, bool)
}

// Tokens is what the token file answers.
type Tokens interface {
	Lookup(token string) (store.Address, bool)
}

// Placement is the result.
type Placement struct {
	Proof     store.Proof
	Principal store.Address // fixed by the proof; empty for outside_pane
	PID       int
}

// Placer places connections.
type Placer struct {
	Proc   procinfo.Reader
	Panes  Panes
	Tokens Tokens
	// SelfPID is the daemon's own pid; the walk stops there, since every
	// process the daemon starts would otherwise look like a pane's.
	SelfPID int
	// MaxDepth bounds the ancestry walk; 64 when zero.
	MaxDepth int
	// TTY, when set, says whether a process has a controlling terminal.
	// Outside every pane, only a caller at a terminal is the person; one
	// without is detached (§9). nil keeps every outside caller the person.
	TTY Terminals
}

// Terminals is what the kernel says about a process's controlling terminal.
type Terminals interface {
	HasTTY(pid int) bool
}

// Place decides for one connection. The order is the trust order: a pane
// the kernel can prove beats a token, which beats the absence of both.
func (p *Placer) Place(conn net.Conn, token string) Placement {
	pid := 0
	if p.Proc != nil {
		pid = p.Proc.PeerPID(conn)
	}
	if pid > 0 && p.Panes != nil {
		if pane, ok := p.ancestorPane(pid); ok {
			return Placement{Proof: store.ProofPane, Principal: pane, PID: pid}
		}
	}
	if token != "" && p.Tokens != nil {
		if name, ok := p.Tokens.Lookup(token); ok {
			return Placement{Proof: store.ProofToken, Principal: name, PID: pid}
		}
	}
	if p.TTY != nil && (pid <= 0 || !p.TTY.HasTTY(pid)) {
		return Placement{Proof: store.ProofDetached, PID: pid}
	}
	return Placement{Proof: store.ProofOutsidePane, PID: pid}
}

func (p *Placer) ancestorPane(pid int) (store.Address, bool) {
	depth := p.MaxDepth
	if depth == 0 {
		depth = 64
	}
	seen := map[int]bool{}
	for cur := pid; cur > 1 && depth > 0 && !seen[cur]; depth-- {
		seen[cur] = true
		if cur == p.SelfPID {
			return "", false
		}
		if pane, ok := p.Panes.PaneOfPID(cur); ok {
			return pane, true
		}
		parent, ok := p.Proc.ParentPID(cur)
		if !ok {
			return "", false
		}
		cur = parent
	}
	return "", false
}
