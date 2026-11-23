package identity

import (
	"net"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// fakeProc is a process tree: child -> parent, plus the pid each conn is.
type fakeProc struct {
	parent map[int]int
	peer   int
}

func (f fakeProc) PeerPID(net.Conn) int { return f.peer }
func (f fakeProc) ParentPID(pid int) (int, bool) {
	p, ok := f.parent[pid]
	return p, ok
}
func (f fakeProc) EnvVar(int, string) (string, bool) { return "", false }

type fakePanes map[int]store.Address

func (f fakePanes) PaneOfPID(pid int) (store.Address, bool) {
	a, ok := f[pid]
	return a, ok
}

type fakeTokens map[string]store.Address

func (f fakeTokens) Lookup(t string) (store.Address, bool) {
	a, ok := f[t]
	return a, ok
}

// Tree: 1 -> 100 (herdr) -> 200 (shell of pane w1:p3) -> 300 (claude) -> 400 (mailai cli)
//
//	                 -> 210 (shell of pane w1:p4) -> 310 (codex)
//	1 -> 500 (terminal) -> 600 (zsh) -> 700 (mailai cli, the person)
//	1 -> 900 (daemon) -> 910 (hook the daemon ran)
var tree = map[int]int{100: 1, 200: 100, 300: 200, 400: 300, 210: 100, 310: 210, 500: 1, 600: 500, 700: 600, 900: 1, 910: 900}
var panes = fakePanes{200: "w1:p3", 210: "w1:p4"}

func placer(peer int) *Placer {
	return &Placer{Proc: fakeProc{parent: tree, peer: peer}, Panes: panes, Tokens: fakeTokens{"mt_ok": "helper"}, SelfPID: 900}
}

// D4/§9: a caller whose ancestry reaches a pane's shell is that pane,
// whatever token or claim it carries.
func TestD4_PaneByAncestryBeatsToken(t *testing.T) {
	pl := placer(400).Place(nil, "mt_ok")
	if pl.Proof != store.ProofPane || pl.Principal != "w1:p3" || pl.PID != 400 {
		t.Fatalf("%+v", pl)
	}
	if pl := placer(310).Place(nil, ""); pl.Principal != "w1:p4" {
		t.Fatalf("%+v", pl)
	}
	// The pane's shell itself is the pane.
	if pl := placer(200).Place(nil, ""); pl.Principal != "w1:p3" {
		t.Fatalf("%+v", pl)
	}
}

func TestToken_WhenNoPane(t *testing.T) {
	pl := placer(700).Place(nil, "mt_ok")
	if pl.Proof != store.ProofToken || pl.Principal != "helper" || pl.PID != 700 {
		t.Fatalf("%+v", pl)
	}
	if pl := placer(700).Place(nil, "mt_bad"); pl.Proof != store.ProofOutsidePane || pl.Principal != "" {
		t.Fatalf("bad token must fall through: %+v", pl)
	}
}

func TestOutsidePane_PersonAndDaemonChildren(t *testing.T) {
	if pl := placer(700).Place(nil, ""); pl.Proof != store.ProofOutsidePane || pl.PID != 700 {
		t.Fatalf("%+v", pl)
	}
	// A process the daemon itself started stops the walk at the daemon.
	if pl := placer(910).Place(nil, ""); pl.Proof != store.ProofOutsidePane {
		t.Fatalf("%+v", pl)
	}
}

func TestNoKernelPID_TokensOnly(t *testing.T) {
	p := placer(0)
	if pl := p.Place(nil, "mt_ok"); pl.Proof != store.ProofToken || pl.Principal != "helper" {
		t.Fatalf("%+v", pl)
	}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofOutsidePane || pl.PID != 0 {
		t.Fatalf("%+v", pl)
	}
	// No Panes at all (the none adapter): never a pane.
	p2 := &Placer{Proc: fakeProc{parent: tree, peer: 400}, Tokens: fakeTokens{}}
	if pl := p2.Place(nil, ""); pl.Proof != store.ProofOutsidePane {
		t.Fatalf("%+v", pl)
	}
}

type fakeTTY map[int]bool

func (f fakeTTY) HasTTY(pid int) bool { return f[pid] }

// §9: outside every pane, the person is at a terminal. A caller with no
// controlling terminal (cron, launchd, a setsid child an agent left behind)
// is detached, never the person. Panes and tokens do not care.
func TestNoTerminalIsDetached(t *testing.T) {
	p := placer(700)
	p.TTY = fakeTTY{700: true}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofOutsidePane {
		t.Fatalf("at a terminal: %+v", pl)
	}
	p.TTY = fakeTTY{}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofDetached || pl.Principal != "" || pl.PID != 700 {
		t.Fatalf("no terminal: %+v", pl)
	}
	if pl := p.Place(nil, "mt_ok"); pl.Proof != store.ProofToken {
		t.Fatalf("token: %+v", pl)
	}
	p = placer(400)
	p.TTY = fakeTTY{}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofPane {
		t.Fatalf("pane: %+v", pl)
	}
	// No kernel pid: nothing to ask about, so not the person either.
	p = placer(0)
	p.TTY = fakeTTY{0: true}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofDetached {
		t.Fatalf("no pid: %+v", pl)
	}
}

// namedPanes is an adapter whose panes have names: it says the pane id of
// a shell beside the principal.
type namedPanes struct {
	fakePanes
	ids map[int]string
}

func (n namedPanes) PaneIDOfPID(pid int) (string, bool) {
	id, ok := n.ids[pid]
	return id, ok
}

// SPEC §9: a pane placement carries the pane id even when the principal is
// the agent's name, so the sender's pane is shown beside it. An adapter
// that cannot say leaves it empty, and only a pane placement has one.
func TestPanePlacementCarriesThePaneID(t *testing.T) {
	p := placer(400)
	p.Panes = namedPanes{fakePanes{200: "builder", 210: "w1:p4"}, map[int]string{200: "w1:p3", 210: "w1:p4"}}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofPane || pl.Principal != "builder" || pl.Pane != "w1:p3" {
		t.Fatalf("%+v", pl)
	}
	p.Proc = fakeProc{parent: tree, peer: 310}
	if pl := p.Place(nil, ""); pl.Principal != "w1:p4" || pl.Pane != "w1:p4" {
		t.Fatalf("%+v", pl)
	}
	p.Proc = fakeProc{parent: tree, peer: 700}
	if pl := p.Place(nil, "mt_ok"); pl.Proof != store.ProofToken || pl.Pane != "" {
		t.Fatalf("a token has no pane: %+v", pl)
	}
	if pl := placer(400).Place(nil, ""); pl.Proof != store.ProofPane || pl.Pane != "" {
		t.Fatalf("an adapter that cannot say leaves it empty: %+v", pl)
	}
}

func TestWalkIsBoundedAndCycleSafe(t *testing.T) {
	loop := map[int]int{10: 11, 11: 10}
	p := &Placer{Proc: fakeProc{parent: loop, peer: 10}, Panes: fakePanes{}, MaxDepth: 5}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofOutsidePane {
		t.Fatalf("%+v", pl)
	}
	long := map[int]int{}
	for i := 2; i < 200; i++ {
		long[i] = i + 1
	}
	long[200] = 1
	p = &Placer{Proc: fakeProc{parent: long, peer: 2}, Panes: fakePanes{150: "far"}}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofOutsidePane {
		t.Fatalf("a pane past MaxDepth must not be reached: %+v", pl)
	}
}
