package daemon

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Asks across machines (SPEC §13a): the inbox machine keeps link asks in
// its ask graph, so a cycle through it is refused instead of deadlocking.

// addSpoke links one more spoke to the star's inbox machine, with one
// placed agent.
func (s *star) addSpoke(t *testing.T, name string, agent store.Address) string {
	t.Helper()
	sock, d := startDaemonWith(t, Options{Name: name, Store: store.New(store.Options{MachineID: name}),
		Placer: placed(Placement{Proof: store.ProofToken, Principal: agent}),
		Inbox:  "s1", LinkRetry: 20 * time.Millisecond, Heartbeat: 50 * time.Millisecond,
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) { return net.Dial("unix", s.hubSock) }})
	waitFor(t, name+" linked", func() bool { return d.linkUp() && s.hub.hasLink(name) })
	return sock
}

// askBg asks in the background and returns where the outcome lands.
func askBg(c *client.Client, a AskArgs) chan error {
	done := make(chan error, 1)
	go func() { done <- c.Call("ask", a, nil) }()
	return done
}

func waitUnread(t *testing.T, c *client.Client, n int) []store.Message {
	t.Helper()
	var rr ReadResult
	waitFor(t, "mail", func() bool {
		c.Call("read", ReadArgs{Unread: true, Peek: true}, &rr)
		return len(rr.Messages) >= n
	})
	return rr.Messages
}

// worker@s2 asks reviewer on the inbox machine; reviewer asking back is the
// closing edge and is refused at once. The reply releases the edge.
func TestStar_AskCycleSpokeHubSpoke(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	reviewer := dialAs(t, s.hubSock, "reviewer")
	asked := askBg(worker, AskArgs{To: "reviewer@s1", Text: "ship?", TimeoutMS: 5000})
	ask := waitUnread(t, reviewer, 1)[0]
	if err := reviewer.Call("ask", AskArgs{To: "worker@s2", Text: "which branch?", TimeoutMS: 2000}, nil); client.Code(err) != proto.CodeAskCycle {
		t.Fatalf("closing edge: %v", err)
	}
	if err := reviewer.Call("send", SendArgs{To: "worker@s2", Text: "yes", ReplyTo: ask.MID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-asked; err != nil {
		t.Fatal(err)
	}
	// The reply released the edge: the same ask is now an ordinary wait.
	if err := reviewer.Call("ask", AskArgs{To: "worker@s2", Text: "which branch?", TimeoutMS: 100}, nil); client.Code(err) != proto.CodeTimeout {
		t.Fatalf("edge not released by the reply: %v", err)
	}
}

// The other order: reviewer waits on worker@s2, so worker@s2 asking back is
// refused on the inbox machine and the spoke's caller gets ask_cycle.
func TestStar_AskCycleRelayedToSpoke(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	reviewer := dialAs(t, s.hubSock, "reviewer")
	asked := askBg(reviewer, AskArgs{To: "worker@s2", Text: "ready?", TimeoutMS: 5000})
	ask := waitUnread(t, worker, 1)[0]
	err := worker.Call("ask", AskArgs{To: "reviewer@s1", Text: "ready for what?", TimeoutMS: 2000}, nil)
	if client.Code(err) != proto.CodeAskCycle {
		t.Fatalf("spoke's closing edge: %v", err)
	}
	if err := worker.Call("send", SendArgs{To: "reviewer@s1", Text: "yes", ReplyTo: ask.MID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-asked; err != nil {
		t.Fatal(err)
	}
}

// Two spokes asking each other meet in the inbox machine's graph.
func TestStar_AskCycleSpokeToSpoke(t *testing.T) {
	s := newStar(t)
	s3 := s.addSpoke(t, "s3", "tester")
	worker := dialAs(t, s.spokeSock, "worker")
	tester := dialAs(t, s3, "tester")
	asked := askBg(worker, AskArgs{To: "tester@s3", Text: "green?", TimeoutMS: 5000})
	ask := waitUnread(t, tester, 1)[0]
	if ask.From != "worker@s2" {
		t.Fatalf("%+v", ask)
	}
	if err := tester.Call("ask", AskArgs{To: "worker@s2", Text: "which suite?", TimeoutMS: 2000}, nil); client.Code(err) != proto.CodeAskCycle {
		t.Fatalf("spoke-to-spoke closing edge: %v", err)
	}
	if err := tester.Call("send", SendArgs{To: "worker@s2", Text: "green", ReplyTo: ask.MID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-asked; err != nil {
		t.Fatal(err)
	}
	if err := tester.Call("ask", AskArgs{To: "worker@s2", Text: "which suite?", TimeoutMS: 100}, nil); client.Code(err) != proto.CodeTimeout {
		t.Fatalf("edge not released by the reply: %v", err)
	}
}

// An unanswered link ask lets go of its edge when its timeout passes.
func TestStar_LinkAskEdgeReleasedOnTimeout(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	reviewer := dialAs(t, s.hubSock, "reviewer")
	asked := askBg(worker, AskArgs{To: "reviewer@s1", Text: "ship?", TimeoutMS: 200})
	waitUnread(t, reviewer, 1)
	if err := reviewer.Call("ask", AskArgs{To: "worker@s2", Text: "?", TimeoutMS: 50}, nil); client.Code(err) != proto.CodeAskCycle {
		t.Fatalf("closing edge: %v", err)
	}
	if err := <-asked; client.Code(err) != proto.CodeTimeout {
		t.Fatalf("spoke ask: %v", err)
	}
	waitFor(t, "edge released by its timeout", func() bool {
		err := reviewer.Call("ask", AskArgs{To: "worker@s2", Text: "?", TimeoutMS: 20}, nil)
		return client.Code(err) == proto.CodeTimeout
	})
}

// A link ask with no timeout, or a longer one, holds its edge 24 h at most.
func TestStar_LinkAskTimeoutCapped(t *testing.T) {
	sock, hub := startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"})})
	l := dial(t, sock)
	if err := l.Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	for _, ms := range []int64{0, int64(48 * time.Hour / time.Millisecond), 5000} {
		var r LinkSendResult
		before := time.Now()
		if err := l.Call("link-send", LinkSendArgs{From: "worker", To: "reviewer", Kind: store.KindAsk, Text: "?", TimeoutMS: ms}, &r); err != nil {
			t.Fatal(err)
		}
		want := 24 * time.Hour
		if ms == 5000 {
			want = 5 * time.Second
		}
		got := hub.linkAskDeadline(r.Message.MID).Sub(before)
		if got < want-time.Second || got > want+time.Second {
			t.Fatalf("timeout_ms %d: edge held %s, want %s", ms, got, want)
		}
	}
}
