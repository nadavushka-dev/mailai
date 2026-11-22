package adapter

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Recorded from herdr 0.9.1.
const agentList = `{"id":"cli:agent:list","result":{"agents":[
{"agent":"claude","agent_status":"idle","cwd":"/Users/n","pane_id":"w2Y:p1","tab_id":"w2Y:t1","terminal_title_stripped":"Claude Code","workspace_id":"w2Y"},
{"agent":"codex","agent_status":"working","name":"reviewer","pane_id":"w2Y:pT","tab_id":"w2Y:tF","terminal_title_stripped":"review","workspace_id":"w2Y"},
{"agent":"gemini","agent_status":"blocked","pane_id":"w3:p2","workspace_id":"w3"}
],"type":"agent_list"}}`

type fakeRunner struct {
	status map[string]string // principal -> agent_status for agent get
	calls  []string
	fail   bool
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.fail {
		return nil, errors.New("herdr: server not running")
	}
	switch args[0] + " " + args[1] {
	case "agent list":
		return []byte(agentList), nil
	case "agent get":
		st, ok := f.status[args[2]]
		if !ok {
			return nil, errors.New("agent_not_found")
		}
		return []byte(`{"id":"x","result":{"agent":{"agent":"claude","agent_status":"` + st + `","pane_id":"` + args[2] + `"},"type":"agent_info"}}`), nil
	case "agent prompt":
		return []byte(`{"id":"x","result":{"type":"agent_prompt"}}`), nil
	}
	return nil, errors.New("unexpected: " + strings.Join(args, " "))
}

type fakeProc struct {
	parent map[int]int
	env    map[int]string
	peer   int
}

func (f fakeProc) PeerPID(net.Conn) int { return f.peer }
func (f fakeProc) ParentPID(pid int) (int, bool) {
	p, ok := f.parent[pid]
	return p, ok
}
func (f fakeProc) EnvVar(pid int, name string) (string, bool) {
	v, ok := f.env[pid]
	return v, ok && name == "HERDR_PANE_ID"
}

func TestHerdr_AgentsMapsRowsToPrincipalsAndStates(t *testing.T) {
	h := &Herdr{Run: &fakeRunner{}}
	got, err := h.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].Principal != "w2Y:p1" || got[0].State != Idle || got[0].Session != "w2Y" || got[0].Harness != "claude" || got[0].Label != "Claude Code" {
		t.Fatalf("%+v", got[0])
	}
	if got[1].Principal != "reviewer" || got[1].State != Working || got[1].PaneID != "w2Y:pT" {
		t.Fatalf("named agent: %+v", got[1])
	}
	if got[2].State != Blocked {
		t.Fatalf("%+v", got[2])
	}
	h2 := &Herdr{Run: &fakeRunner{fail: true}}
	if _, err := h2.Agents(context.Background()); err == nil {
		t.Fatal("herdr down must error")
	}
}

// SPEC §7: Type checks the live state first and never types at a working or
// blocked agent.
func TestHerdr_TypeOnlyAtRest(t *testing.T) {
	r := &fakeRunner{status: map[string]string{"a": "idle", "b": "working", "c": "blocked", "d": "done"}}
	h := &Herdr{Run: r}
	ctx := context.Background()
	if err := h.Type(ctx, "a", "You have 1 unread"); err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "agent prompt a You have 1 unread" {
		t.Fatal(last)
	}
	if err := h.Type(ctx, "b", "x"); !errors.Is(err, ErrNotAtRest) {
		t.Fatal(err)
	}
	if err := h.Type(ctx, "c", "x"); !errors.Is(err, ErrNotAtRest) {
		t.Fatal(err)
	}
	if err := h.Type(ctx, "d", "x"); err != nil {
		t.Fatal(err)
	}
	if err := h.Type(ctx, "zzz", "x"); !errors.Is(err, ErrNoAgent) {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "agent prompt b") || strings.HasPrefix(c, "agent prompt c") {
			t.Fatal("typed at a busy agent")
		}
	}
}

// SPEC §9 / Q4: a process is in a pane when an ancestor is a direct child
// of the herdr server; that ancestor's recorded HERDR_PANE_ID names the pane.
func TestHerdr_PaneOfPIDByServerChildAndRecordedEnv(t *testing.T) {
	// 1 -> 900 (herdr server) -> 200 (zsh of w2Y:pT) -> 300 (claude) -> 400 (mailai)
	//   -> 500 (terminal) -> 600 (zsh with a *forged* HERDR_PANE_ID) -> 700 (mailai)
	proc := fakeProc{
		parent: map[int]int{900: 1, 200: 900, 300: 200, 400: 300, 500: 1, 600: 500, 700: 600},
		env:    map[int]string{200: "w2Y:pT", 600: "w2Y:pT", 300: "w9:p9"},
		peer:   900,
	}
	dialed := 0
	h := &Herdr{Run: &fakeRunner{}, Proc: proc, Socket: "/fake.sock", Dial: func(string) (net.Conn, error) {
		dialed++
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}}
	h.Agents(context.Background()) // learn that w2Y:pT is "reviewer"
	// Only the shell (200) is a pane; identity reaches it by walking from 400.
	if p, ok := h.PaneOfPID(200); !ok || p != "reviewer" {
		t.Fatalf("%q %v", p, ok)
	}
	if _, ok := h.PaneOfPID(300); ok {
		t.Fatal("a grandchild of the server with its own env must not count")
	}
	if _, ok := h.PaneOfPID(400); ok {
		t.Fatal("the caller itself is not the shell")
	}
	// The forged env outside herdr is ignored: 600's parent is not the server.
	if _, ok := h.PaneOfPID(600); ok {
		t.Fatal("forged HERDR_PANE_ID outside herdr accepted")
	}
	// An unnamed pane is its pane id.
	proc.env[200] = "w2Y:p1"
	if p, _ := h.PaneOfPID(200); p != "w2Y:p1" {
		t.Fatal(p)
	}
	// A pane herdr has not listed as an agent is still a pane.
	proc.env[200] = "w7:p7"
	if p, ok := h.PaneOfPID(200); !ok || p != "w7:p7" {
		t.Fatal(p, ok)
	}
	if dialed != 1 {
		t.Fatalf("server pid should be cached, dialed %d times", dialed)
	}
	// No socket, no placement.
	h2 := &Herdr{Run: &fakeRunner{}, Proc: proc, Socket: "", Dial: h.Dial}
	t.Setenv("HERDR_SOCKET_PATH", "")
	if _, ok := h2.PaneOfPID(200); ok {
		t.Fatal("placed without a server")
	}
}

// Decided 2026-11-22 (SPEC §9): a pane runs an agent when herdr lists one
// there. A pane it has not listed is asked about afresh before it counts as
// a plain shell, and when herdr cannot say, the pane is an agent's.
func TestHerdr_RunsAgent(t *testing.T) {
	r := &fakeRunner{}
	h := &Herdr{Run: r}
	h.Agents(context.Background())
	for _, p := range []store.Address{"reviewer", "w2Y:p1"} {
		if !h.RunsAgent(p) {
			t.Fatalf("%s runs an agent", p)
		}
	}
	before := len(r.calls)
	if h.RunsAgent("w7:p7") {
		t.Fatal("a pane herdr does not list is a plain shell")
	}
	if len(r.calls) != before+1 || r.calls[before] != "agent list" {
		t.Fatalf("an unlisted pane must be checked afresh: %q", r.calls[before:])
	}
	r.fail = true
	if !h.RunsAgent("w7:p7") {
		t.Fatal("herdr not answering must not make a pane the person's")
	}
	if !(None{}).RunsAgent("x") {
		t.Fatal("none has no panes; anything it is asked about is an agent's")
	}
}

func TestNone(t *testing.T) {
	var a Adapter = None{}
	if ag, err := a.Agents(context.Background()); err != nil || ag != nil {
		t.Fatal(ag, err)
	}
	if err := a.Type(context.Background(), "x", "y"); !errors.Is(err, ErrCannotType) {
		t.Fatal(err)
	}
	if _, ok := a.PaneOfPID(1); ok {
		t.Fatal("none has panes?")
	}
	_ = store.Address("")
}
