package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// fakeAdapter records what gets typed and answers states from a map.
type fakeAdapter struct {
	mu     sync.Mutex
	states map[store.Address]adapter.State
	typed  []string // "principal: line"
	fail   bool
}

func (f *fakeAdapter) Name() string { return "fake" }
func (f *fakeAdapter) Agents(context.Context) ([]adapter.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []adapter.Agent
	for p, s := range f.states {
		out = append(out, adapter.Agent{Principal: p, State: s, Session: "ws1", Harness: "claude"})
	}
	return out, nil
}
func (f *fakeAdapter) Type(_ context.Context, p store.Address, line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return adapter.ErrNotAtRest
	}
	f.typed = append(f.typed, string(p)+": "+line)
	return nil
}
func (f *fakeAdapter) PaneOfPID(int) (store.Address, bool) { return "", false }
func (f *fakeAdapter) set(p store.Address, s adapter.State) {
	f.mu.Lock()
	f.states[p] = s
	f.mu.Unlock()
}
func (f *fakeAdapter) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.typed...)
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newDeliveryDaemon makes a daemon with a fake adapter and clock, not
// served: tests drive wakeOnce and deliverQueued by hand.
func newDeliveryDaemon(t *testing.T, wake bool) (*Daemon, *fakeAdapter, *testClock) {
	t.Helper()
	fa := &fakeAdapter{states: map[store.Address]adapter.State{}}
	clk := &testClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	st := store.New(store.Options{Now: clk.now})
	d := New(Options{Store: st, Adapter: fa, Wake: wake, Now: clk.now})
	d.refreshAgents(context.Background())
	return d, fa, clk
}

// SPEC §7: an agent at rest with mail unread and unannounced for 5 s gets
// the one line typed; never while working or blocked; two wakes 30 s apart;
// a wake whose mail was read meanwhile is dropped; once per message.
func TestWake_RulesAndTiming(t *testing.T) {
	d, fa, clk := newDeliveryDaemon(t, true)
	fa.set("worker", adapter.Idle)
	d.refreshAgents(context.Background())
	ctx := context.Background()
	d.st.Send(store.SendRequest{From: "reviewer", To: "worker", Text: "secret body", Subject: "secret subject"})
	d.wakeOnce(ctx)
	if len(fa.lines()) != 0 {
		t.Fatal("woke before 5 s")
	}
	clk.add(4 * time.Second)
	d.wakeOnce(ctx)
	if len(fa.lines()) != 0 {
		t.Fatal("woke at 4 s")
	}
	clk.add(2 * time.Second)
	d.wakeOnce(ctx)
	lines := fa.lines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "worker: You have 1 unread mailai message from reviewer.") {
		t.Fatalf("%q", lines)
	}
	if strings.Contains(lines[0], "secret") {
		t.Fatal("wake carried sender text")
	}
	// Once per message: no second wake for the same mail.
	clk.add(time.Minute)
	d.wakeOnce(ctx)
	if len(fa.lines()) != 1 {
		t.Fatal("woke twice for one message")
	}
	// New mail soon after: the 30 s spacing holds it, then it goes with the new count.
	d.st.Send(store.SendRequest{From: "a", To: "worker", Text: "2"})
	clk.add(6 * time.Second)
	d.wakeOnce(ctx)
	if len(fa.lines()) != 2 {
		t.Fatalf("spacing: %q", fa.lines())
	}
	d.st.Send(store.SendRequest{From: "b", To: "worker", Text: "3"})
	clk.add(6 * time.Second)
	d.wakeOnce(ctx)
	if len(fa.lines()) != 2 {
		t.Fatal("woke within 30 s of the last wake")
	}
	clk.add(30 * time.Second)
	d.wakeOnce(ctx)
	lines = fa.lines()
	if len(lines) != 3 || !strings.Contains(lines[2], "You have 3 unread") {
		t.Fatalf("%q", lines)
	}
	// Read meanwhile: nothing to wake for.
	d.st.Send(store.SendRequest{From: "c", To: "worker", Text: "4"})
	clk.add(time.Minute)
	d.st.Read(store.ReadQuery{Mailbox: "worker", Mark: true})
	d.wakeOnce(ctx)
	if len(fa.lines()) != 3 {
		t.Fatal("woke for mail already read")
	}
	// Working or blocked: never.
	for _, s := range []adapter.State{adapter.Working, adapter.Blocked, adapter.Unknown} {
		fa.set("worker", s)
		d.refreshAgents(ctx)
		d.st.Send(store.SendRequest{From: "d", To: "worker", Text: "5"})
		clk.add(time.Minute)
		d.wakeOnce(ctx)
		if len(fa.lines()) != 3 {
			t.Fatalf("woke a %s agent", s)
		}
		d.st.Read(store.ReadQuery{Mailbox: "worker", Mark: true})
	}
	// Done counts as at rest; an unknown mailbox (no agent) is never woken.
	fa.set("worker", adapter.Done)
	d.refreshAgents(ctx)
	d.st.Send(store.SendRequest{From: "e", To: "worker", Text: "6"})
	d.st.Send(store.SendRequest{From: "e", To: "ghost", Text: "7"})
	clk.add(time.Minute)
	d.wakeOnce(ctx)
	lines = fa.lines()
	if len(lines) != 4 || !strings.HasPrefix(lines[3], "worker:") {
		t.Fatalf("%q", lines)
	}
}

func TestWake_OffByConfigAndHookNoticeSharesTheMark(t *testing.T) {
	d, fa, clk := newDeliveryDaemon(t, false)
	fa.set("worker", adapter.Idle)
	d.refreshAgents(context.Background())
	d.st.Send(store.SendRequest{From: "a", To: "worker", Text: "x"})
	clk.add(time.Minute)
	d.wakeOnce(context.Background())
	if len(fa.lines()) != 0 {
		t.Fatal("wake = false still typed")
	}
	// A hook notice announces the mail; a later wake has nothing new.
	d.wake = true
	c := &conn{pl: Placement{Proof: store.ProofToken, Principal: "worker"}}
	res, _ := d.notice(c, NoticeArgs{})
	nr := res.(NoticeResult)
	if !nr.New || nr.Count != 1 || !strings.Contains(nr.Line, "mailai read --unread") {
		t.Fatalf("%+v", nr)
	}
	res, _ = d.notice(c, NoticeArgs{})
	if res.(NoticeResult).New {
		t.Fatal("second notice for the same mail must not be new")
	}
	d.wakeOnce(context.Background())
	if len(fa.lines()) != 0 {
		t.Fatal("woke for mail a hook already announced")
	}
	// Someone else asking for worker's notice does not move worker's mark.
	d.st.Send(store.SendRequest{From: "b", To: "worker", Text: "y"})
	other := &conn{pl: Placement{Proof: store.ProofToken, Principal: "other"}}
	res, _ = d.notice(other, NoticeArgs{Mailbox: "worker"})
	if !res.(NoticeResult).New {
		t.Fatal("other should see it as new")
	}
	res, _ = d.notice(c, NoticeArgs{})
	if !res.(NoticeResult).New {
		t.Fatal("a foreign notice moved the mark")
	}
}

func TestQueue_TypedWhenAtRestNeverTwice(t *testing.T) {
	d, fa, _ := newDeliveryDaemon(t, true)
	fa.set("worker", adapter.Working)
	d.refreshAgents(context.Background())
	c := &conn{pl: Placement{Proof: store.ProofOutsidePane}}
	if _, err := d.queue(c, QueueArgs{To: "worker", Text: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	d.queue(c, QueueArgs{To: "worker", Text: "then lint"})
	if _, err := d.queue(c, QueueArgs{To: "human", Text: "x"}); err == nil {
		t.Fatal("queue at human")
	}
	d.deliverQueued(context.Background())
	if len(fa.lines()) != 0 {
		t.Fatal("typed at a working agent")
	}
	fa.set("worker", adapter.Idle)
	d.refreshAgents(context.Background())
	d.deliverQueued(context.Background())
	if l := fa.lines(); len(l) != 1 || l[0] != "worker: run the tests" {
		t.Fatalf("one per tick, oldest first: %q", l)
	}
	if q := d.queueList(); len(q.Queued) != 1 || q.Queued[0].Text != "then lint" {
		t.Fatalf("%+v", q)
	}
	d.deliverQueued(context.Background())
	d.deliverQueued(context.Background())
	if l := fa.lines(); len(l) != 2 {
		t.Fatalf("%q", l)
	}
	if q := d.queueList(); len(q.Queued) != 0 {
		t.Fatalf("%+v", q)
	}
	d.queue(c, QueueArgs{To: "worker", Text: "a"})
	d.queue(c, QueueArgs{To: "other", Text: "b"})
	if _, err := d.queueRm(c, QueueRmArgs{To: "worker"}); err != nil {
		t.Fatal(err)
	}
	if q := d.queueList(); len(q.Queued) != 1 || q.Queued[0].To != "other" {
		t.Fatalf("%+v", q)
	}
	if _, err := d.queueRm(c, QueueRmArgs{ID: 999}); client.Code(err) != proto.CodeNotFound {
		t.Fatal(err)
	}
	// Failure to type keeps it queued.
	fa.set("other", adapter.Idle)
	d.refreshAgents(context.Background())
	fa.fail = true
	d.deliverQueued(context.Background())
	if q := d.queueList(); len(q.Queued) != 1 {
		t.Fatal("dropped on failure")
	}
}

func TestQueue_NoneAdapterRefuses(t *testing.T) {
	d := New(Options{Store: store.New(store.Options{})})
	c := &conn{pl: Placement{Proof: store.ProofOutsidePane}}
	if _, err := d.queue(c, QueueArgs{To: "w", Text: "x"}); client.Code(err) != proto.CodeBadRequest {
		t.Fatal(err)
	}
}

// Hooks report their own state; the adapter poll fills the rest; `agents`
// lists both.
func TestReportStateAndAgents(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	fa := &fakeAdapter{states: map[store.Address]adapter.State{"w1:p1": adapter.Idle}}
	sock, d := startDaemonWith(t, Options{Placer: pl, Adapter: fa, PollEvery: time.Hour})
	d.refreshAgents(context.Background())
	worker := dialAs(t, sock, "worker")
	person := dial(t, sock)
	if err := worker.Call("report-state", ReportStateArgs{State: "working", Harness: "claude-code"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.Call("report-state", ReportStateArgs{State: "sleepy"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatal(err)
	}
	if err := person.Call("report-state", ReportStateArgs{State: "idle"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("outside_pane reporting state: %v", err)
	}
	var ar AgentsResult
	person.Call("agents", nil, &ar)
	if ar.Adapter != "fake" || len(ar.Agents) != 2 || ar.Agents[0].Principal != "w1:p1" || ar.Agents[1].Principal != "worker" ||
		ar.Agents[1].State != adapter.Working || ar.Agents[1].Source != "hook" || ar.Agents[1].Harness != "claude-code" {
		t.Fatalf("%+v", ar)
	}
	// A fresh hook report outlives a poll; an agent the adapter drops goes away.
	fa.set("worker", adapter.Idle)
	fa.mu.Lock()
	delete(fa.states, "w1:p1")
	fa.mu.Unlock()
	d.refreshAgents(context.Background())
	if s, _ := d.stateOf("worker"); s != adapter.Working {
		t.Fatalf("poll overrode a fresh hook report: %s", s)
	}
	if _, ok := d.stateOf("w1:p1"); ok {
		t.Fatal("dropped agent still listed")
	}
}
