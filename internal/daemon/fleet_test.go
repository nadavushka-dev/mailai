package daemon

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// star is an inbox machine s1 and a spoke s2 whose link dials s1's socket
// directly (the ssh bridge's job in production). Each has one placed agent:
// reviewer on s1, worker on s2.
type star struct {
	hubSock, spokeSock string
	hub, spoke         *Daemon
	mu                 sync.Mutex
	links              []net.Conn
	refuse             bool
}

func newStar(t *testing.T) *star {
	t.Helper()
	s := &star{}
	s.hubSock, s.hub = startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"}),
		Placer: placed(Placement{Proof: store.ProofToken, Principal: "reviewer"})})
	s.spokeSock, s.spoke = startDaemonWith(t, Options{Name: "s2", Store: store.New(store.Options{MachineID: "s2"}),
		Placer: placed(Placement{Proof: store.ProofToken, Principal: "worker"}),
		Inbox:  "s1", LinkRetry: 20 * time.Millisecond, Heartbeat: 50 * time.Millisecond,
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			s.mu.Lock()
			refuse := s.refuse
			s.mu.Unlock()
			if refuse {
				return nil, io.ErrClosedPipe
			}
			c, err := net.Dial("unix", s.hubSock)
			if err == nil {
				s.mu.Lock()
				s.links = append(s.links, c)
				s.mu.Unlock()
			}
			return c, err
		}})
	s.waitLink(t, true)
	return s
}

// cut drops the link; with refuse, the spoke cannot redial until heal.
func (s *star) cut(refuse bool) {
	s.mu.Lock()
	s.refuse = refuse
	for _, c := range s.links {
		c.Close()
	}
	s.links = nil
	s.mu.Unlock()
}

func (s *star) heal() { s.mu.Lock(); s.refuse = false; s.mu.Unlock() }

func (s *star) waitLink(t *testing.T, up bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.spoke.linkUp() == up && s.hub.hasLink("s2") == up {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("link never %v", up)
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole point: an agent on a spoke asks the person, the question shows
// in the inbox machine's Inbox, the person answers there, and the agent on
// the spoke gets the verified answer and nothing left unread.
func TestStar_AskHumanFromSpokeAnsweredOnInbox(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	person := dial(t, s.hubSock)

	got := make(chan AskHumanResult, 1)
	errs := make(chan error, 1)
	go func() {
		var r AskHumanResult
		if err := worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}, TimeoutMS: 5000}, &r); err != nil {
			errs <- err
			return
		}
		got <- r
	}()
	var inbox InboxResult
	waitFor(t, "item on the inbox machine", func() bool {
		person.Call("inbox", InboxArgs{}, &inbox)
		return len(inbox.Items) == 1
	})
	it := inbox.Items[0]
	if it.From != "worker@s2" || it.Question != "Deploy?" || len(it.Options) != 2 {
		t.Fatalf("%+v", it)
	}
	// The spoke has no Inbox of its own for this.
	var spokeInbox InboxResult
	dial(t, s.spokeSock).Call("inbox", InboxArgs{}, &spokeInbox)
	if len(spokeInbox.Items) != 0 {
		t.Fatalf("spoke opened its own item: %+v", spokeInbox.Items)
	}
	if err := person.Call("answer", AnswerArgs{Item: it.ID, Text: "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Reply == nil || r.Reply.Text != "yes" || r.Reply.Verified != "human" {
			t.Fatalf("%+v", r)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("ask-human on the spoke never returned")
	}
	var ur UnreadResult
	worker.Call("unread", UnreadArgs{}, &ur)
	if ur.Count != 0 {
		t.Fatalf("answer left unread on the spoke: %+v", ur)
	}
	person.Call("inbox", InboxArgs{}, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("item still open: %+v", inbox.Items)
	}
}

// Agent to agent across machines, both ways, threaded.
func TestStar_MailBothWays(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	reviewer := dialAs(t, s.hubSock, "reviewer")

	var sr SendResult
	if err := reviewer.Call("send", SendArgs{To: "worker@s2", Subject: "nit", Text: "log the attempt"}, &sr); err != nil {
		t.Fatal(err)
	}
	var rr ReadResult
	waitFor(t, "mail on the spoke", func() bool {
		worker.Call("read", ReadArgs{Unread: true, Peek: true}, &rr)
		return len(rr.Messages) == 1
	})
	m := rr.Messages[0]
	if m.From != "reviewer@s1" || m.MID != sr.Message.MID || m.Text != "log the attempt" || m.Origin != "link" {
		t.Fatalf("%+v", m)
	}
	var nr NoticeResult
	worker.Call("notice", NoticeArgs{}, &nr)
	if !nr.New || !strings.Contains(nr.Line, "reviewer@s1") || strings.Contains(nr.Line, "attempt") {
		t.Fatalf("notice: %+v", nr)
	}
	if err := worker.Call("send", SendArgs{To: "reviewer@s1", Text: "done", ReplyTo: m.MID}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply on the inbox machine", func() bool {
		reviewer.Call("read", ReadArgs{Unread: true, Peek: true}, &rr)
		return len(rr.Messages) == 1
	})
	back := rr.Messages[0]
	if back.From != "worker@s2" || back.ThreadID != sr.Message.MID || back.Kind != store.KindReply || back.SenderProof != store.ProofLink || back.Verified != "" {
		t.Fatalf("%+v", back)
	}
	// Sending to the machine by its own name is local.
	if err := reviewer.Call("send", SendArgs{To: "reviewer@s1", Text: "note to self"}, nil); err != nil {
		t.Fatal(err)
	}
}

// An ask from a spoke waits on the spoke, so the link can drop and come
// back while the other side takes its time.
func TestStar_AskSurvivesLinkDrop(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	reviewer := dialAs(t, s.hubSock, "reviewer")
	got := make(chan AskResult, 1)
	go func() {
		var r AskResult
		if err := worker.Call("ask", AskArgs{To: "reviewer@s1", Text: "ship?", TimeoutMS: 8000}, &r); err != nil {
			t.Errorf("ask: %v", err)
		}
		got <- r
	}()
	var rr ReadResult
	waitFor(t, "ask on the inbox machine", func() bool {
		reviewer.Call("read", ReadArgs{Unread: true, Peek: true}, &rr)
		return len(rr.Messages) == 1
	})
	s.cut(true)
	s.waitLink(t, false)
	if err := reviewer.Call("send", SendArgs{To: "worker@s2", Text: "ship it", ReplyTo: rr.Messages[0].MID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		t.Fatalf("ask returned with the link down: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	s.heal()
	select {
	case r := <-got:
		if r.Reply.Text != "ship it" || r.Reply.From != "reviewer@s1" {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ask never got the reply after the link came back")
	}
	var ur UnreadResult
	worker.Call("unread", UnreadArgs{}, &ur)
	if ur.Count != 0 {
		t.Fatalf("delivered reply unread: %+v", ur)
	}
}

// A spoke's held permission request is decided from the inbox machine.
func TestStar_ApprovalFromSpoke(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	person := dial(t, s.hubSock)
	got := make(chan HoldApprovalResult, 1)
	go func() {
		var r HoldApprovalResult
		if err := worker.Call("hold-approval", HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "git push", TimeoutMS: 5000}, &r); err != nil {
			t.Errorf("hold: %v", err)
		}
		got <- r
	}()
	var inbox InboxResult
	waitFor(t, "approval on the inbox machine", func() bool {
		person.Call("inbox", InboxArgs{}, &inbox)
		return len(inbox.Items) == 1
	})
	if inbox.Items[0].From != "worker@s2" || inbox.Items[0].Kind != store.ItemApproval {
		t.Fatalf("%+v", inbox.Items[0])
	}
	if err := person.Call("answer", AnswerArgs{Item: inbox.Items[0].ID, Text: "allow"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Decision != "allow" || r.By != "human" {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hold never decided")
	}
}

// A link is never the person, and cannot speak for anyone but its own
// machine's agents.
func TestStar_LinkIsNeverThePerson(t *testing.T) {
	sock, _ := startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"})})
	l := dial(t, sock)
	if err := l.Call("link-hello", LinkHelloArgs{Machine: "s2"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		verb string
		args any
	}{
		{"send", SendArgs{To: "worker", Text: "hi"}},
		{"answer", AnswerArgs{Item: 1, Text: "yes"}},
		{"inbox", InboxArgs{}},
		{"web-start", WebStartArgs{}},
	} {
		if err := l.Call(v.verb, v.args, nil); client.Code(err) != proto.CodeForbidden {
			t.Fatalf("%s over a link: %v", v.verb, err)
		}
	}
	for _, from := range []store.Address{"", "human", "x@s3"} {
		err := l.Call("link-send", LinkSendArgs{From: from, To: "reviewer", Text: "hi"}, nil)
		if client.Code(err) != proto.CodeForbidden {
			t.Fatalf("link-send from %q: %v", from, err)
		}
	}
	var r LinkSendResult
	if err := l.Call("link-send", LinkSendArgs{From: "worker", To: "human", Text: "hi"}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Message.From != "worker@s2" || r.Message.Verified != "" || r.Message.SenderProof != store.ProofLink {
		t.Fatalf("%+v", r.Message)
	}
	// An agent placed by a token cannot turn its connection into a link.
	sock2, _ := startDaemonWith(t, Options{Name: "s1", Placer: placed(Placement{Proof: store.ProofToken, Principal: "worker"})})
	if err := dialAs(t, sock2, "worker").Call("link-hello", LinkHelloArgs{Machine: "s9"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("agent became a link: %v", err)
	}
}

// Off the machine with no route: refused, nothing stored.
func TestStar_OfflineAndUnknownMachine(t *testing.T) {
	s := newStar(t)
	reviewer := dialAs(t, s.hubSock, "reviewer")
	if err := reviewer.Call("send", SendArgs{To: "ghost@s9", Text: "hi"}, nil); client.Code(err) != proto.CodeOffline {
		t.Fatalf("unknown machine: %v", err)
	}
	s.cut(true)
	s.waitLink(t, false)
	worker := dialAs(t, s.spokeSock, "worker")
	// A send waits in the spoke's outbox now (fleet_outbox_test.go); an
	// approval is never queued, so it is still refused.
	if err := worker.Call("hold-approval", HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "ls", TimeoutMS: 1000}, nil); client.Code(err) != proto.CodeOffline {
		t.Fatalf("spoke with the link down: %v", err)
	}
	// The inbox machine still takes mail for the spoke and delivers it later.
	if err := reviewer.Call("send", SendArgs{To: "worker@s2", Text: "later"}, nil); err != nil {
		t.Fatal(err)
	}
	s.heal()
	var rr ReadResult
	waitFor(t, "held mail delivered on reconnect", func() bool {
		worker.Call("read", ReadArgs{Unread: true, Peek: true}, &rr)
		return len(rr.Messages) == 1 && rr.Messages[0].Text == "later"
	})
}
