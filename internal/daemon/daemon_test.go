package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// shortSock is a socket path short enough for macOS (104 bytes).
func shortSock(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	dir := filepath.Join(os.TempDir(), "mailai-t-"+hex.EncodeToString(b))
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// fakePlacer places by a per-connection script: the first N connections get
// the given placements, the rest are outside_pane.
type fakePlacer struct {
	mu   sync.Mutex
	next []Placement
}

func (f *fakePlacer) Place(net.Conn) Placement {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.next) == 0 {
		return Placement{Proof: store.ProofOutsidePane}
	}
	p := f.next[0]
	f.next = f.next[1:]
	return p
}

func startDaemon(t *testing.T, pl Placer) (string, *Daemon) {
	t.Helper()
	sock := shortSock(t)
	d := New(Options{Store: store.New(store.Options{}), Placer: pl})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sock, d
}

func dial(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestPingAndUnknownVerb(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	c := dial(t, sock)
	var pong map[string]string
	if err := c.Call("ping", nil, &pong); err != nil || pong["pong"] == "" {
		t.Fatal(err, pong)
	}
	err := c.Call("nope", nil, nil)
	if client.Code(err) != proto.CodeUnknownVerb {
		t.Fatalf("got %v", err)
	}
}

func TestSendAndReadFromOutsidePane(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	c := dial(t, sock)
	var sr SendResult
	if err := c.Call("send", SendArgs{From: "worker", To: "human", Text: "hi", Subject: "s"}, &sr); err != nil {
		t.Fatal(err)
	}
	if sr.Message.From != "worker" || sr.Message.ID != 1 {
		t.Fatalf("%+v", sr.Message)
	}
	// Default mailbox for an outside caller is human; marking read works.
	var rr ReadResult
	if err := c.Call("read", ReadArgs{Unread: true}, &rr); err != nil {
		t.Fatal(err)
	}
	if len(rr.Messages) != 1 || !rr.Messages[0].WasUnread || rr.PeekForced {
		t.Fatalf("%+v", rr)
	}
	var ur UnreadResult
	c.Call("unread", UnreadArgs{Mailbox: "human"}, &ur)
	if ur.Count != 0 {
		t.Fatalf("unread after read: %d", ur.Count)
	}
	// From defaults to human for an outside caller.
	c.Call("send", SendArgs{To: "worker", Text: "yo"}, &sr)
	if sr.Message.From != store.Human {
		t.Fatalf("%+v", sr.Message)
	}
}

// SPEC §9: a placed principal is the sender whatever --from says, and a pane
// reading the person's inbox is served a peek.
func TestPlacedPrincipalOverridesClaimAndCannotClearHuman(t *testing.T) {
	pl := &fakePlacer{next: []Placement{{Proof: store.ProofPane, Principal: "w1:p3", PID: 42}}}
	sock, _ := startDaemon(t, pl)
	pane := dial(t, sock)
	person := dial(t, sock)

	var who WhoamiResult
	pane.Call("whoami", nil, &who)
	if who.Principal != "w1:p3" || who.Proof != store.ProofPane || who.PID != 42 {
		t.Fatalf("%+v", who)
	}
	err := pane.Call("send", SendArgs{From: "human", To: "w1:p9", Text: "approve"}, nil)
	if client.Code(err) != proto.CodeForbidden {
		t.Fatalf("pane claiming human: %v", err)
	}
	var sr SendResult
	pane.Call("send", SendArgs{To: "human", Text: "need help"}, &sr)
	if sr.Message.From != "w1:p3" || sr.Message.SenderProof != store.ProofPane {
		t.Fatalf("%+v", sr.Message)
	}
	person.Call("send", SendArgs{To: "w1:p3", Text: "x"}, &sr)
	if sr.Message.SenderProof != store.ProofOutsidePane {
		t.Fatalf("%+v", sr.Message)
	}
	pane.Call("read", ReadArgs{}, nil) // clear it so the seen check below sees one message
	var rr ReadResult
	if err := pane.Call("read", ReadArgs{Mailbox: "human"}, &rr); err != nil {
		t.Fatal(err)
	}
	if !rr.PeekForced || len(rr.Messages) != 1 {
		t.Fatalf("%+v", rr)
	}
	var ur UnreadResult
	person.Call("unread", nil, &ur)
	if ur.Count != 1 {
		t.Fatalf("pane cleared the person's unread mail: %d", ur.Count)
	}
	// Peeking its own inbox marks seen, so the sender can tell.
	person.Call("send", SendArgs{To: "w1:p3", Text: "answer"}, nil)
	pane.Call("read", ReadArgs{Peek: true}, &rr)
	var own ReadResult
	pane.Call("read", ReadArgs{}, &own)
	last := own.Messages[len(own.Messages)-1]
	if last.Text != "answer" || last.SeenAt == 0 || !last.WasSeen {
		t.Fatalf("%+v", last)
	}
}

// SPEC §7: ask blocks in the connection until a reply in its thread reaches
// the asker; the model spends nothing meanwhile.
func TestAskBlocksUntilReply(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	asker := dial(t, sock)
	answerer := dial(t, sock)

	type res struct {
		r   AskResult
		err error
	}
	got := make(chan res, 1)
	go func() {
		var r AskResult
		err := asker.Call("ask", AskArgs{From: "worker", To: "reviewer", Text: "ok to merge?", TimeoutMS: 5000}, &r)
		got <- res{r, err}
	}()
	// Wait for the ask to land, then reply in its thread.
	var rr ReadResult
	deadline := time.Now().Add(2 * time.Second)
	for len(rr.Messages) == 0 && time.Now().Before(deadline) {
		answerer.Call("read", ReadArgs{Mailbox: "reviewer", Peek: true}, &rr)
		time.Sleep(5 * time.Millisecond)
	}
	if len(rr.Messages) != 1 || rr.Messages[0].Kind != store.KindAsk {
		t.Fatalf("ask not stored: %+v", rr)
	}
	select {
	case r := <-got:
		t.Fatalf("ask returned before any reply: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	// A reply to a third party in the same thread does not end the wait.
	answerer.Call("send", SendArgs{From: "reviewer", To: "human", Text: "fyi", ReplyTo: rr.Messages[0].MID}, nil)
	select {
	case r := <-got:
		t.Fatalf("ask ended on a reply to someone else: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	if err := answerer.Call("send", SendArgs{From: "reviewer", To: "worker", Text: "yes", ReplyTo: rr.Messages[0].MID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.r.Reply.Text != "yes" || r.r.Reply.ThreadID != r.r.Ask.MID {
			t.Fatalf("%+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not return after the reply")
	}
}

// waitOpen blocks until from has n asks open in the daemon's graph.
func waitOpen(t *testing.T, d *Daemon, from store.Address, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		got := d.asks.open[from]
		d.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s never had %d asks open", from, n)
}

func TestAskTimeoutAndCycle(t *testing.T) {
	sock, d := startDaemon(t, nil)
	a := dial(t, sock)
	err := a.Call("ask", AskArgs{From: "a", To: "b", Text: "?", TimeoutMS: 30}, nil)
	if client.Code(err) != proto.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
	// a asks b (open). b asking a would close a cycle.
	go a.Call("ask", AskArgs{From: "a", To: "b", Text: "?", TimeoutMS: 3000}, nil)
	waitOpen(t, d, "a", 1)
	b := dial(t, sock)
	if cerr := b.Call("ask", AskArgs{From: "b", To: "a", Text: "?", TimeoutMS: 10}, nil); client.Code(cerr) != proto.CodeAskCycle {
		t.Fatalf("want ask_cycle, got %v", cerr)
	}
	// Transitive: a->b open, b->c open, c->a is a cycle too.
	go b.Call("ask", AskArgs{From: "b", To: "c", Text: "?", TimeoutMS: 3000}, nil)
	waitOpen(t, d, "b", 1)
	cc := dial(t, sock)
	if cerr := cc.Call("ask", AskArgs{From: "c", To: "a", Text: "?", TimeoutMS: 10}, nil); client.Code(cerr) != proto.CodeAskCycle {
		t.Fatalf("want transitive ask_cycle, got %v", cerr)
	}
	if err := a.Call("ask", AskArgs{From: "a", To: "a", Text: "?"}, nil); client.Code(err) != proto.CodeAskCycle {
		t.Fatalf("self ask: %v", err)
	}
}

func TestAskLimit(t *testing.T) {
	sock := shortSock(t)
	d := New(Options{Store: store.New(store.Options{}), MaxAsks: 2})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx)
	c := dial(t, sock)
	for i := 0; i < 2; i++ {
		go c.Call("ask", AskArgs{From: "a", To: store.Address("t" + string(rune('0'+i))), Text: "?", TimeoutMS: 3000}, nil)
	}
	waitOpen(t, d, "a", 2)
	err := c.Call("ask", AskArgs{From: "a", To: "t9", Text: "?", TimeoutMS: 10}, nil)
	if client.Code(err) != proto.CodeAskLimit {
		t.Fatalf("want ask_limit, got %v", err)
	}
}

func TestSubscribeGetsMailEventsWithoutBodies(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	sub := dial(t, sock)
	if err := sub.Call("subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	sender := dial(t, sock)
	sender.Call("send", SendArgs{From: "a", To: "b", Text: "secret body", Subject: "secret subject"}, nil)
	select {
	case f := <-sub.Events():
		if f.Event != "mail" {
			t.Fatalf("%+v", f)
		}
		s := string(f.Data)
		if contains(s, "secret") {
			t.Fatalf("event leaked sender text: %s", s)
		}
		if !contains(s, `"mailbox":"b"`) || !contains(s, `"from":"a"`) {
			t.Fatalf("%s", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
}

func TestStoreErrorsMapToCodes(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	c := dial(t, sock)
	for i := 0; i < store.RateBurst; i++ {
		c.Call("send", SendArgs{From: "a", To: "b", Text: "x"}, nil)
	}
	err := c.Call("send", SendArgs{From: "a", To: "b", Text: "x"}, nil)
	if client.Code(err) != proto.CodeRateLimited {
		t.Fatalf("%v", err)
	}
	err = c.Call("send", SendArgs{From: "z", To: "b"}, nil)
	if client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("%v", err)
	}
}

func TestListenRefusesLiveSocketAndReplacesStale(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	d2 := New(Options{Store: store.New(store.Options{})})
	if err := d2.Listen(sock); err == nil {
		t.Fatal("second daemon on a live socket must be refused")
	}
	stale := shortSock(t)
	os.MkdirAll(filepath.Dir(stale), 0o700)
	ln, _ := net.Listen("unix", stale)
	ln.Close() // Go removes the file on Close; recreate a stale one by hand.
	os.WriteFile(stale, nil, 0o600)
	d3 := New(Options{Store: store.New(store.Options{})})
	if err := d3.Listen(stale); err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	d3.ln.Close()
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
