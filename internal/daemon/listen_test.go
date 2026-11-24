package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §7 (decided 2026-11-23): an agent whose harness holds a listen
// connection gets its wake line there, and nothing is typed into its pane.
// The daemon knows no harness: anything that keeps `mailai listen` open
// takes the agent's wakes.

func listenRig(t *testing.T, states map[store.Address]adapter.State) (*Daemon, string, *fakeAdapter, *testClock) {
	t.Helper()
	fa := &fakeAdapter{states: states}
	clk := &testClock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	pl := placed(Placement{Proof: store.ProofPane, Principal: "worker"})
	sock, d := startDaemonWith(t, Options{Placer: pl, Adapter: fa, Wake: true, Now: clk.now, Store: store.New(store.Options{Now: clk.now})})
	d.refreshAgents(context.Background())
	return d, sock, fa, clk
}

func listenAs(t *testing.T, sock string, who store.Address) *client.Client {
	t.Helper()
	c := dialAs(t, sock, who)
	var r ListenResult
	if err := c.Call("listen", nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.Mailbox != who {
		t.Fatalf("listening on %q", r.Mailbox)
	}
	return c
}

// nextLine is the next wake line the listener got, or "" after a while.
func nextLine(c *client.Client, within time.Duration) string {
	tm := time.After(within)
	for {
		select {
		case f, ok := <-c.Events():
			if !ok {
				return ""
			}
			if f.Event != "listen" {
				continue
			}
			var w ListenEvent
			json.Unmarshal(f.Data, &w)
			return w.Line
		case <-tm:
			return ""
		}
	}
}

func typedCount(fa *fakeAdapter) int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return len(fa.typed)
}

func TestListen_WakeGoesToTheListenerNotThePane(t *testing.T) {
	d, sock, fa, clk := listenRig(t, map[store.Address]adapter.State{"worker": adapter.Idle})
	c := listenAs(t, sock, "worker")
	d.st.Send(store.SendRequest{From: "reviewer", To: "worker", Text: "hi"})
	clk.add(6 * time.Second)
	d.wakeOnce(context.Background())
	line := nextLine(c, 2*time.Second)
	if !strings.Contains(line, "reviewer") || strings.Contains(line, "\n") {
		t.Fatalf("no wake line: %q", line)
	}
	if n := typedCount(fa); n != 0 {
		t.Fatalf("typed into the pane %d times while listened", n)
	}
	// Announced once, as a typed wake is.
	clk.add(time.Minute)
	d.wakeOnce(context.Background())
	if again := nextLine(c, 300*time.Millisecond); again != "" {
		t.Fatalf("woken twice for the same mail: %q", again)
	}
}

// The listener's harness queues the line until its agent is free, so an
// agent the adapter does not know (no Herdr) is still woken; a busy one
// waits, as a typed wake does.
func TestListen_UnknownStateIsWokenBusyIsNot(t *testing.T) {
	d, sock, _, clk := listenRig(t, map[store.Address]adapter.State{})
	c := listenAs(t, sock, "worker")
	d.st.Send(store.SendRequest{From: "reviewer", To: "worker", Text: "hi"})
	clk.add(6 * time.Second)
	d.wakeOnce(context.Background())
	if nextLine(c, 2*time.Second) == "" {
		t.Fatal("an agent with no known state and a listener was not woken")
	}

	d2, sock2, _, clk2 := listenRig(t, map[store.Address]adapter.State{"worker": adapter.Working})
	c2 := listenAs(t, sock2, "worker")
	d2.st.Send(store.SendRequest{From: "reviewer", To: "worker", Text: "hi"})
	clk2.add(6 * time.Second)
	d2.wakeOnce(context.Background())
	if l := nextLine(c2, 300*time.Millisecond); l != "" {
		t.Fatalf("a working agent was woken: %q", l)
	}
}

// When the listener goes, typing comes back (the fallback).
func TestListen_GoneListenerTypingResumes(t *testing.T) {
	d, sock, fa, clk := listenRig(t, map[store.Address]adapter.State{"worker": adapter.Idle})
	c := listenAs(t, sock, "worker")
	c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.mu.Lock()
		n := len(d.listeners["worker"])
		d.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener not dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.st.Send(store.SendRequest{From: "reviewer", To: "worker", Text: "hi"})
	clk.add(6 * time.Second)
	d.wakeOnce(context.Background())
	if typedCount(fa) != 1 {
		t.Fatalf("typed %d", typedCount(fa))
	}
}

// The add-on is installed for every Pi session and switched on per agent
// in the config (decided 2026-11-23): [addon] agents, glob patterns over
// the agent's name. An agent not listed is refused, and --check says so
// without listening.
func TestListen_OnlyAgentsSwitchedOn(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofPane, Principal: "lane-a"}, Placement{Proof: store.ProofPane, Principal: "brain"})
	sock, d := startDaemonWith(t, Options{Placer: pl, ListenAgents: []string{"conductor", "lane-*"}})
	if err := dialAs(t, sock, "brain").Call("listen", nil, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("brain is not switched on: %v", err)
	}
	if err := dialAs(t, sock, "brain").Call("listen", ListenArgs{Check: true}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("check for brain: %v", err)
	}
	var r ListenResult
	if err := dialAs(t, sock, "lane-a").Call("listen", ListenArgs{Check: true}, &r); err != nil || r.Mailbox != "lane-a" {
		t.Fatalf("check for lane-a: %v %+v", err, r)
	}
	d.mu.Lock()
	n := len(d.listeners["lane-a"])
	d.mu.Unlock()
	if n != 0 {
		t.Fatal("a check registered a listener")
	}
	if err := dialAs(t, sock, "lane-a").Call("listen", nil, nil); err != nil {
		t.Fatalf("lane-a listens: %v", err)
	}
	// An empty list switches nobody on.
	sock2, _ := startDaemonWith(t, Options{Placer: pl, ListenAgents: []string{}})
	if err := dialAs(t, sock2, "lane-a").Call("listen", nil, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("empty list: %v", err)
	}
}

// Only an agent listens: the person has the TUI and the page, and a
// detached caller has no mailbox.
func TestListen_OnlyAnAgent(t *testing.T) {
	_, sock, _, _ := listenRig(t, map[store.Address]adapter.State{})
	err := dial(t, sock).Call("listen", nil, nil)
	if client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("person listen: %v", err)
	}
}
