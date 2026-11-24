package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
)

// listAdapter answers Agents with a list the test sets.
type listAdapter struct {
	mu   sync.Mutex
	list []adapter.Agent
}

func (l *listAdapter) Name() string { return "herdr" }
func (l *listAdapter) Agents(context.Context) ([]adapter.Agent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]adapter.Agent(nil), l.list...), nil
}
func (l *listAdapter) Type(context.Context, store.Address, string) error { return nil }
func (l *listAdapter) PaneOfPID(int) (store.Address, bool)               { return "", false }
func (l *listAdapter) set(as ...adapter.Agent) {
	l.mu.Lock()
	l.list = as
	l.mu.Unlock()
}

// SPEC §12a: the web Agents list shows liveness. The daemon remembers when
// it last saw each agent live (memory only); token-held names and mailai's
// own bounces cannot be known; the page gets it from /api/agents.
func TestWebAgents_LiveAndLastSeen(t *testing.T) {
	clk := &testClock{t: time.Unix(1_800_000_000, 0)}
	t0 := clk.now().UnixNano()
	la := &listAdapter{}
	builder := adapter.Agent{Principal: "builder", Name: "builder", PaneID: "w4W:p1", State: adapter.Idle}
	lane := adapter.Agent{Principal: "w4W:p3", PaneID: "w4W:p3", State: adapter.Working}
	la.set(builder, lane)
	tk, err := tokens.Open(filepath.Join(t.TempDir(), "tokens.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.New("headless", ""); err != nil {
		t.Fatal(err)
	}
	sock, d := startDaemonWith(t, Options{Placer: placed(), Adapter: la, PollEvery: time.Hour, Now: clk.now, Tokens: tk, Name: "ubcl"})
	d.refreshAgents(context.Background())
	clk.add(2 * time.Hour)
	la.set(builder)
	d.refreshAgents(context.Background())

	res := d.agentsSeen()
	if !res.Known || res.Machine != "ubcl" {
		t.Fatalf("%+v", res)
	}
	by := map[store.Address]AgentSeen{}
	for _, a := range res.Agents {
		by[a.Principal] = a
	}
	if b := by["builder"]; !b.Live || b.PaneID != "w4W:p1" || b.LastSeen != clk.now().UnixNano() {
		t.Fatalf("builder %+v", b)
	}
	if l, ok := by["w4W:p3"]; !ok || l.Live || l.LastSeen != t0 || l.PaneID != "w4W:p3" {
		t.Fatalf("closed lane %+v %v", l, ok)
	}
	if !slices.Contains(res.Unknown, "headless") || !slices.Contains(res.Unknown, Bouncer) {
		t.Fatalf("unknown %v", res.Unknown)
	}

	// The page reads it from a person-only endpoint.
	person := dial(t, sock)
	var ws WebStartResult
	if err := person.Call("web-start", WebStartArgs{}, &ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { person.Call("web-stop", nil, nil) })
	if r, _ := http.Get("http://" + ws.Addr + "/api/agents"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.StatusCode)
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	r, err := cl.Get(ws.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := readAll(r)
	r, err = cl.Get("http://" + ws.Addr + "/api/agents")
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("%v %v", err, r)
	}
	var got AgentsSeenResult
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil || len(got.Agents) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	// And marks them: a live dot, closed rows dimmed with when.
	for _, need := range []string{"/api/agents", `class="live-dot"`, "row agent-row closed", "closed ${ago(", "'closed'"} {
		if !strings.Contains(page, need) {
			t.Fatalf("page missing %s", need)
		}
	}
}

// A daemon without an adapter cannot know who is live: nothing is closed.
func TestWebAgents_NoneAdapterKnowsNothing(t *testing.T) {
	_, d := startDaemon(t, placed())
	if res := d.agentsSeen(); res.Known {
		t.Fatalf("%+v", res)
	}
}
