// Package adapter is how mailai learns about agents and types one line at
// one of them (SPEC §8). It answers three questions: which agents exist and
// in which session, what state each is in, and how to put one line at an
// agent's prompt. Everything else about panes belongs to the multiplexer.
package adapter

import (
	"context"
	"errors"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// State is the five-word vocabulary every adapter and harness maps onto.
type State string

const (
	Idle    State = "idle"
	Working State = "working"
	Blocked State = "blocked" // waiting on an approval or question in its own UI
	Done    State = "done"
	Unknown State = "unknown"
)

// AtRest says a line may be typed at the agent (SPEC §7).
func (s State) AtRest() bool { return s == Idle || s == Done }

// Agent is what an adapter knows about one.
type Agent struct {
	Principal store.Address `json:"principal"`
	// Name is the multiplexer's own name for the agent, when one is set.
	// It is the principal unless a token holds it (SPEC §9).
	Name    string `json:"name,omitempty"`
	Label   string `json:"label,omitempty"`
	Session string `json:"session,omitempty"`
	State   State  `json:"state"`
	Harness string `json:"harness,omitempty"`
	PaneID  string `json:"pane_id,omitempty"`
}

var (
	// ErrCannotType is the none adapter's answer: nothing can type.
	ErrCannotType = errors.New("this adapter cannot type into an agent")
	// ErrNotAtRest refuses to type at a working or blocked agent.
	ErrNotAtRest = errors.New("agent is not at rest")
	// ErrNoAgent names an unknown principal.
	ErrNoAgent = errors.New("no such agent")
)

// Adapter is implemented by herdr and none.
type Adapter interface {
	Name() string
	// Agents lists the agents the multiplexer knows right now.
	Agents(ctx context.Context) ([]Agent, error)
	// Type puts one line and Enter at an agent that is at rest.
	Type(ctx context.Context, principal store.Address, line string) error
	// PaneOfPID says which pane a process belongs to, for identity (§9).
	PaneOfPID(pid int) (store.Address, bool)
}

// Reserver is an adapter that names agents itself and can be told which
// names are taken by mailai tokens (SPEC §9). An agent whose name is taken
// is placed under its pane id instead; logf says so once.
type Reserver interface {
	Reserve(taken func(store.Address) bool, logf func(format string, args ...any))
}

// None is the adapter for no multiplexer: agents are known through hooks and
// tokens only, nothing can type, nothing is a pane.
type None struct{}

func (None) Name() string                                      { return "none" }
func (None) Agents(context.Context) ([]Agent, error)           { return nil, nil }
func (None) Type(context.Context, store.Address, string) error { return ErrCannotType }
func (None) PaneOfPID(int) (store.Address, bool)               { return "", false }

// RunsAgent: none places nothing in a pane, so it is never asked; if it is,
// the answer that cannot make anyone the person.
func (None) RunsAgent(store.Address) bool { return true }
