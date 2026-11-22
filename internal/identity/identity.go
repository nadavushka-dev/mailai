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
	// Agent names the agent program found among the caller's parents, for
	// an agent placement outside every pane (ProofAgent).
	Agent string
}

// Programs reads a process's argv, as the kernel shows it.
type Programs interface {
	Args(pid int) ([]string, bool)
}

// AgentPanes is a Panes that also knows whether an agent runs in a pane
// right now. With it, a pane that runs no agent (a plain shell) is the
// person's, not an agent's (decided 2026-11-22).
type AgentPanes interface {
	RunsAgent(pane store.Address) bool
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
	// Args, when set, finds agent programs among a caller's parents: such
	// a caller is an agent, never the person, in a pane or out of one.
	Args Programs
}

// Terminals is what the kernel says about a process's controlling terminal.
type Terminals interface {
	HasTTY(pid int) bool
}

// Place decides for one connection. The order is the trust order: a pane
// the kernel can prove beats a token, which beats the absence of both.
//
// Who is an agent is decided by what runs the caller, not where it runs
// (decided 2026-11-22): a pane whose agent the multiplexer sees, or with an
// agent program between the caller and the pane's shell, is that agent; a
// pane that runs no agent is a terminal like any other. Outside every pane,
// an agent program among the parents makes an agent with only a claimed
// name, never the person.
func (p *Placer) Place(conn net.Conn, token string) Placement {
	pid := 0
	if p.Proc != nil {
		pid = p.Proc.PeerPID(conn)
	}
	inPane := false
	if pid > 0 && p.Panes != nil {
		if pane, shell, ok := p.ancestorPane(pid); ok {
			ap, knows := p.Panes.(AgentPanes)
			if !knows || p.Args == nil || ap.RunsAgent(pane) {
				return Placement{Proof: store.ProofPane, Principal: pane, PID: pid}
			}
			if _, agent := p.agentAmong(pid, shell); agent {
				return Placement{Proof: store.ProofPane, Principal: pane, PID: pid}
			}
			inPane = true
		}
	}
	if token != "" && p.Tokens != nil {
		if name, ok := p.Tokens.Lookup(token); ok {
			return Placement{Proof: store.ProofToken, Principal: name, PID: pid}
		}
	}
	if !inPane && pid > 0 && p.Args != nil {
		if name, agent := p.agentAmong(pid, 0); agent {
			return Placement{Proof: store.ProofAgent, PID: pid, Agent: name}
		}
	}
	if p.TTY != nil && (pid <= 0 || !p.TTY.HasTTY(pid)) {
		return Placement{Proof: store.ProofDetached, PID: pid}
	}
	return Placement{Proof: store.ProofOutsidePane, PID: pid}
}

// ancestorPane finds the pane the caller runs in, and that pane's shell.
func (p *Placer) ancestorPane(pid int) (store.Address, int, bool) {
	found, shell := store.Address(""), 0
	p.walk(pid, 0, func(cur int) bool {
		if pane, ok := p.Panes.PaneOfPID(cur); ok {
			found, shell = pane, cur
			return true
		}
		return false
	})
	return found, shell, found != ""
}

// agentAmong looks for an agent program from the caller up to, not
// including, stop (a pane's shell; 0 walks to the top). What started the
// multiplexer does not make every pane an agent.
func (p *Placer) agentAmong(pid, stop int) (string, bool) {
	name := ""
	p.walk(pid, stop, func(cur int) bool {
		if args, ok := p.Args.Args(cur); ok {
			if n, agent := IsAgentProgram(args); agent {
				name = n
				return true
			}
		}
		return false
	})
	return name, name != ""
}

// walk visits pid and its ancestors until visit says stop, the walk reaches
// stop or the daemon itself, or the tree ends.
func (p *Placer) walk(pid, stop int, visit func(int) bool) {
	depth := p.MaxDepth
	if depth == 0 {
		depth = 64
	}
	seen := map[int]bool{}
	for cur := pid; cur > 1 && depth > 0 && !seen[cur]; depth-- {
		seen[cur] = true
		if cur == p.SelfPID || cur == stop {
			return
		}
		if visit(cur) {
			return
		}
		parent, ok := p.Proc.ParentPID(cur)
		if !ok {
			return
		}
		cur = parent
	}
}
