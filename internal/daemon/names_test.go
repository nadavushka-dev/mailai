package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/identity"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
)

// Two named Herdr agents: "builder" in w4Y:p1 and "reviewer" in w2Y:pT.
const namedAgents = `{"id":"cli:agent:list","result":{"agents":[
{"agent":"claude","agent_status":"working","name":"builder","pane_id":"w4Y:p1","workspace_id":"w4Y"},
{"agent":"codex","agent_status":"working","name":"reviewer","pane_id":"w2Y:pT","workspace_id":"w2Y"}
],"type":"agent_list"}}`

type herdrCLI struct{}

func (herdrCLI) Run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "agent" && args[1] == "list" {
		return []byte(namedAgents), nil
	}
	return nil, errors.New("unexpected: " + strings.Join(args, " "))
}

// herdrTree is the process tree: 900 is the herdr server, 200 and 210 the
// shells of w4Y:p1 and w2Y:pT, 400 and 410 a mailai client in each, 700 the
// person's at a plain terminal. A connection to the daemon is whichever pid
// peer says; one to herdr's socket is the server.
type herdrTree struct{ peer *atomic.Int64 }

func (h herdrTree) PeerPID(c net.Conn) int {
	if _, unix := c.(*net.UnixConn); unix {
		return int(h.peer.Load())
	}
	return 900
}
func (herdrTree) ParentPID(pid int) (int, bool) {
	p, ok := map[int]int{900: 1, 200: 900, 210: 900, 400: 200, 410: 210, 700: 1}[pid]
	return p, ok
}
func (herdrTree) EnvVar(pid int, name string) (string, bool) {
	v, ok := map[int]string{200: "w4Y:p1", 210: "w2Y:pT"}[pid]
	return v, ok && name == "HERDR_PANE_ID"
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// startUnderHerdr serves a daemon whose adapter is Herdr over a fake CLI and
// process tree, with a real identity.Placer and token file.
func startUnderHerdr(t *testing.T, tk *tokens.Tokens, logs *syncBuf) (string, *atomic.Int64) {
	t.Helper()
	peer := &atomic.Int64{}
	peer.Store(700)
	tree := herdrTree{peer: peer}
	h := &adapter.Herdr{Run: herdrCLI{}, Proc: tree, Socket: "/fake.sock", Dial: func(string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}}
	pl := &identity.Placer{Proc: tree, Panes: h, Tokens: tk}
	sock, _ := startDaemonWith(t, Options{Placer: pl, Tokens: tk, Adapter: h, PollEvery: 10 * time.Millisecond,
		Log: log.New(logs, "", 0)})
	return sock, peer
}

// dialPID connects as a process and waits until the daemon has placed it.
func dialPID(t *testing.T, sock string, peer *atomic.Int64, pid int) (*client.Client, WhoamiResult) {
	t.Helper()
	peer.Store(int64(pid))
	c := dial(t, sock)
	var who WhoamiResult
	if err := c.Call("whoami", nil, &who); err != nil {
		t.Fatal(err)
	}
	return c, who
}

func newTokens(t *testing.T) *tokens.Tokens {
	t.Helper()
	tk, err := tokens.Open(filepath.Join(t.TempDir(), "tokens.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// Parity with the TUI (§12a): the page shows a sender's pane beside its
// name, in the thread and in the thread list.
func TestWeb_PageShowsTheSendersPane(t *testing.T) {
	sock, _ := startDaemon(t, placed())
	cl, base := webClient(t, dial(t, sock))
	_, page := get(t, cl, base+"/")
	for _, want := range []string{"m.sender_pane", "t.from_pane", "withPane("} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}

// SPEC §9: token names are reserved. The person cannot issue a token for a
// name a live Herdr agent has.
func TestNames_TokenNewRefusesALiveHerdrAgentsName(t *testing.T) {
	sock, peer := startUnderHerdr(t, newTokens(t), &syncBuf{})
	person, who := dialPID(t, sock, peer, 700)
	if who.Proof != store.ProofOutsidePane {
		t.Fatalf("%+v", who)
	}
	err := person.Call("token-new", TokenNewArgs{Name: "builder"}, nil)
	if client.Code(err) != proto.CodeBadRequest || !strings.Contains(err.Error(), `"builder" is the name of a live Herdr agent (w4Y:p1); pick another name`) {
		t.Fatalf("%v", err)
	}
	if err := person.Call("token-new", TokenNewArgs{Name: "headless"}, nil); err != nil {
		t.Fatal(err)
	}
}

// SPEC §9: a Herdr agent whose name holds a token is placed under its pane
// id, by identity and by the poller alike, and the log says why once. Mail
// from a pane carries the pane beside the name.
func TestNames_TokenNameGivesWayToThePaneAndSenderPaneIsRecorded(t *testing.T) {
	tk := newTokens(t)
	if _, err := tk.New("builder", "issued before the agent took the name"); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	sock, peer := startUnderHerdr(t, tk, logs)
	person, _ := dialPID(t, sock, peer, 700)

	var ar AgentsResult
	deadline := time.Now().Add(5 * time.Second)
	for len(ar.Agents) < 2 && time.Now().Before(deadline) {
		person.Call("agents", nil, &ar)
		time.Sleep(10 * time.Millisecond)
	}
	byPane := map[string]agentRec{}
	for _, a := range ar.Agents {
		byPane[a.PaneID] = a
		if a.Principal == "builder" {
			t.Fatalf("the token's name was given to a Herdr agent: %+v", ar.Agents)
		}
	}
	if a := byPane["w4Y:p1"]; a.Principal != "w4Y:p1" || a.Name != "builder" {
		t.Fatalf("%+v", ar.Agents)
	}
	if a := byPane["w2Y:pT"]; a.Principal != "reviewer" {
		t.Fatalf("%+v", ar.Agents)
	}

	b, who := dialPID(t, sock, peer, 400)
	if who.Proof != store.ProofPane || who.Principal != "w4Y:p1" {
		t.Fatalf("identity disagrees with the poller: %+v", who)
	}
	var sr SendResult
	if err := b.Call("send", SendArgs{To: "human", Text: "from the pane"}, &sr); err != nil {
		t.Fatal(err)
	}
	if sr.Message.From != "w4Y:p1" || sr.Message.SenderPane != "w4Y:p1" {
		t.Fatalf("%+v", sr.Message)
	}
	r, who := dialPID(t, sock, peer, 410)
	if who.Principal != "reviewer" {
		t.Fatalf("%+v", who)
	}
	sr = SendResult{}
	if err := r.Call("send", SendArgs{To: "human", Text: "named"}, &sr); err != nil {
		t.Fatal(err)
	}
	if sr.Message.From != "reviewer" || sr.Message.SenderPane != "w2Y:pT" {
		t.Fatalf("a named sender's pane: %+v", sr.Message)
	}
	// An ask-human and an ask from a pane carry it too.
	var ah AskHumanResult
	if err := r.Call("ask-human", AskHumanArgs{Text: "ok?", NoWait: true}, &ah); err != nil {
		t.Fatal(err)
	}
	r.Call("ask", AskArgs{To: "builder", Text: "there?", TimeoutMS: 20}, nil)
	var rr ReadResult
	person.Call("read", ReadArgs{Thread: ah.Item.MID}, &rr)
	if len(rr.Messages) != 1 || rr.Messages[0].SenderPane != "w2Y:pT" {
		t.Fatalf("ask-human: %+v", rr.Messages)
	}
	var rb ReadResult
	person.Call("read", ReadArgs{Mailbox: "builder", Peek: true}, &rb)
	if len(rb.Messages) != 1 || rb.Messages[0].Kind != store.KindAsk || rb.Messages[0].SenderPane != "w2Y:pT" {
		t.Fatalf("ask: %+v", rb.Messages)
	}
	// The person's own mail has no pane.
	var mine SendResult
	person.Call("send", SendArgs{To: "reviewer", Text: "hi"}, &mine)
	if mine.Message.Text != "hi" || mine.Message.SenderPane != "" {
		t.Fatalf("%+v", mine.Message)
	}

	// Thread rows carry the starter's and the last sender's pane.
	var tr ThreadsResult
	person.Call("threads", nil, &tr)
	found := false
	for _, row := range tr.Threads {
		if row.First == "named" {
			found = true
			if row.FromPane != "w2Y:pT" || row.LastFromPane != "w2Y:pT" {
				t.Fatalf("%+v", row)
			}
		}
		if row.First == "hi" && (row.FromPane != "" || row.LastFromPane != "") {
			t.Fatalf("%+v", row)
		}
	}
	if !found {
		t.Fatalf("%+v", tr.Threads)
	}

	time.Sleep(100 * time.Millisecond) // several polls
	n := 0
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, `"builder"`) && strings.Contains(l, "w4Y:p1") && strings.Contains(l, "token") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one line saying why, got:\n%s", logs.String())
	}
}

// downAdapter cannot list agents: Herdr is not answering.
type downAdapter struct{ listAdapter }

func (*downAdapter) Agents(context.Context) ([]adapter.Agent, error) {
	return nil, errors.New("herdr agent list: connect: no such file or directory\nis the server running?")
}

// SPEC §9: token names are reserved. When the adapter cannot say whether a
// live agent has the name, token-new fails closed: no token is issued, and
// one line names the cause.
func TestNames_TokenNewFailsClosedWhenTheAdapterErrs(t *testing.T) {
	tk := newTokens(t)
	pl := &identity.Placer{Proc: realProc{}, Tokens: tk}
	sock, _ := startDaemonWith(t, Options{Placer: pl, Tokens: tk, Adapter: &downAdapter{}, PollEvery: time.Hour,
		Log: log.New(&syncBuf{}, "", 0)})
	person := dial(t, sock)
	var tn TokenNewResult
	err := person.Call("token-new", TokenNewArgs{Name: "builder"}, &tn)
	if err == nil || tn.Token != "" {
		t.Fatalf("a token was issued while the adapter erred: %+v", tn)
	}
	if client.Code(err) != proto.CodeOffline {
		t.Fatalf("code: %v", err)
	}
	for _, want := range []string{"no token issued", `"builder"`, "herdr", "no such file or directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("not one line: %q", err)
	}
	if list := tk.List(); len(list) != 0 {
		t.Fatalf("token file holds %+v", list)
	}
}
