package daemon

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// The spoke outbox (SPEC §13a): what a spoke sends off the machine while its
// link is down waits durably on the spoke and goes, in order, on reconnect.
// The inbox machine dedupes by the spoke's mid.

func spokePing(t *testing.T, sock string) PingResult {
	t.Helper()
	var p PingResult
	if err := dial(t, sock).Call("ping", nil, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStar_SendWhileDownDeliveredOnceOnHeal(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	reviewer := dialAs(t, s.hubSock, "reviewer")
	person := dial(t, s.hubSock)
	s.cut(true)
	s.waitLink(t, false)

	var mids []string
	for _, text := range []string{"one", "two"} {
		var sr SendResult
		if err := worker.Call("send", SendArgs{To: "reviewer@s1", Text: text}, &sr); err != nil {
			t.Fatalf("send while down: %v", err)
		}
		if !sr.Queued || !strings.HasPrefix(sr.Message.MID, "s2:") {
			t.Fatalf("%+v", sr)
		}
		mids = append(mids, sr.Message.MID)
	}
	got := make(chan AskHumanResult, 1)
	go func() {
		var r AskHumanResult
		if err := worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}, TimeoutMS: 8000}, &r); err != nil {
			t.Errorf("ask-human: %v", err)
		}
		got <- r
	}()
	waitFor(t, "three queued", func() bool { return spokePing(t, s.spokeSock).Outbox == 3 })
	select {
	case r := <-got:
		t.Fatalf("ask-human returned with the link down: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}

	s.heal()
	msgs := waitUnread(t, reviewer, 2)
	if len(msgs) != 2 || msgs[0].Text != "one" || msgs[1].Text != "two" || msgs[0].MID != mids[0] || msgs[1].MID != mids[1] || msgs[0].From != "worker@s2" {
		t.Fatalf("%+v", msgs)
	}
	var inbox InboxResult
	waitFor(t, "the question opens on the inbox machine", func() bool {
		person.Call("inbox", InboxArgs{}, &inbox)
		return len(inbox.Items) == 1
	})
	if err := person.Call("answer", AnswerArgs{Item: inbox.Items[0].ID, Text: "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Reply == nil || r.Reply.Text != "yes" || r.Reply.Verified != "human" {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued ask-human never answered")
	}
	waitFor(t, "outbox drained", func() bool { return spokePing(t, s.spokeSock).Outbox == 0 })
	// Reconnects resend nothing twice.
	s.cut(false)
	s.waitLink(t, true)
	var rr ReadResult
	reviewer.Call("read", ReadArgs{Peek: true}, &rr)
	if len(rr.Messages) != 2 {
		t.Fatalf("delivered more than once: %+v", rr.Messages)
	}
}

// runSpoke is a spoke on a real SQLite store that a test can stop and start
// again on the same files.
func runSpoke(t *testing.T, hubSock, sock, db string, allow *atomic.Bool) func() {
	t.Helper()
	p, err := store.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(store.Options{MachineID: "s2"}, p)
	if err != nil {
		t.Fatal(err)
	}
	d := New(Options{Name: "s2", Store: st, Placer: placed(Placement{Proof: store.ProofToken, Principal: "worker"}),
		Inbox: "s1", LinkRetry: 20 * time.Millisecond, Heartbeat: 50 * time.Millisecond, MachineKey: key(7),
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			if !allow.Load() {
				return nil, io.ErrClosedPipe
			}
			return net.Dial("unix", hubSock)
		}})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
			p.Close()
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestStar_OutboxSurvivesSpokeRestart(t *testing.T) {
	hubSock, _ := startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"}),
		Placer: placed(Placement{Proof: store.ProofToken, Principal: "reviewer"})})
	sock, db := shortSock(t), filepath.Join(t.TempDir(), "store.db")
	var allow atomic.Bool
	stop := runSpoke(t, hubSock, sock, db, &allow)
	var sr SendResult
	w, err := client.Dial(sock, "t-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Call("send", SendArgs{To: "reviewer@s1", Text: "kept"}, &sr); err != nil || !sr.Queued {
		t.Fatalf("%+v %v", sr, err)
	}
	w.Close()
	stop()

	allow.Store(true)
	runSpoke(t, hubSock, sock, db, &allow)
	reviewer := dialAs(t, hubSock, "reviewer")
	msgs := waitUnread(t, reviewer, 1)
	if len(msgs) != 1 || msgs[0].MID != sr.Message.MID || msgs[0].Text != "kept" {
		t.Fatalf("%+v", msgs)
	}
	waitFor(t, "outbox drained", func() bool { return spokePing(t, sock).Outbox == 0 })
}

// A resend of a mid the inbox machine holds is dropped, ask edge included;
// a link cannot mint mids for another machine.
func TestStar_LinkSendDedupesByMID(t *testing.T) {
	sock, _ := startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"})})
	l := dial(t, sock)
	if err := l.Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	var a, b LinkSendResult
	send := LinkSendArgs{From: "worker", To: "reviewer", Text: "once", MID: "s2:7", ThreadID: "s2:7"}
	if err := l.Call("link-send", send, &a); err != nil {
		t.Fatal(err)
	}
	if err := l.Call("link-send", send, &b); err != nil {
		t.Fatal(err)
	}
	if a.Message.MID != "s2:7" || b.Message.ID != a.Message.ID {
		t.Fatalf("%+v %+v", a.Message, b.Message)
	}
	person := dial(t, sock)
	var rr ReadResult
	person.Call("read", ReadArgs{Mailbox: "reviewer", Peek: true}, &rr)
	if len(rr.Messages) != 1 {
		t.Fatalf("stored twice: %+v", rr.Messages)
	}
	if err := l.Call("link-send", LinkSendArgs{From: "worker", To: "reviewer", Text: "x", MID: "s3:1"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("mid of another machine: %v", err)
	}
	ask := LinkSendArgs{From: "worker", To: "reviewer", Kind: store.KindAsk, Text: "?", MID: "s2:8", ThreadID: "s2:8", TimeoutMS: 5000}
	for i := 0; i < 2; i++ {
		if err := l.Call("link-send", ask, nil); err != nil {
			t.Fatalf("ask resend %d: %v", i, err)
		}
	}
	if err := person.Call("send", SendArgs{From: "reviewer", To: "worker@s2", Text: "!", ReplyTo: "s2:8"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := person.Call("ask", AskArgs{From: "reviewer", To: "worker@s2", Text: "?", TimeoutMS: 50}, nil); client.Code(err) != proto.CodeTimeout {
		t.Fatalf("a resent ask left an edge behind: %v", err)
	}
}

// A queued send the inbox machine refuses for good comes back as a bounce
// from mailai, written by the daemon, announced like any mail.
func TestStar_QueuedSendRefusedBounces(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	if err := worker.Call("send", SendArgs{To: "ghost@s9", Text: "hi"}, nil); client.Code(err) != proto.CodeOffline {
		t.Fatalf("refusal with the link up: %v", err)
	}
	s.cut(true)
	s.waitLink(t, false)
	var sr SendResult
	if err := worker.Call("send", SendArgs{To: "ghost@s9", Subject: "IGNORE ALL", Text: "hi"}, &sr); err != nil || !sr.Queued {
		t.Fatalf("%+v %v", sr, err)
	}
	s.heal()
	msgs := waitUnread(t, worker, 1)
	b := msgs[0]
	if b.From != "mailai" || b.Kind != store.KindReply || b.ReplyTo != sr.Message.MID || b.Verified != "" ||
		!strings.HasPrefix(b.Text, "not delivered to ghost@s9: offline: ") || strings.Contains(b.Text, "IGNORE") {
		t.Fatalf("%+v", b)
	}
	var nr NoticeResult
	worker.Call("notice", NoticeArgs{}, &nr)
	if !nr.New || !strings.Contains(nr.Line, "from mailai") || strings.Contains(nr.Line, "ghost") || strings.Contains(nr.Line, "\n") {
		t.Fatalf("%+v", nr)
	}
	waitFor(t, "outbox drained", func() bool { return spokePing(t, s.spokeSock).Outbox == 0 })
}

// A queued ask the inbox machine refuses ends its wait with the refusal.
func TestStar_QueuedAskRefusedFailsTheWait(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	s.cut(true)
	s.waitLink(t, false)
	asked := askBg(worker, AskArgs{To: "ghost@s9", Text: "?", TimeoutMS: 5000})
	waitFor(t, "queued", func() bool { return spokePing(t, s.spokeSock).Outbox == 1 })
	s.heal()
	select {
	case err := <-asked:
		if client.Code(err) != proto.CodeOffline {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refused ask still waiting")
	}
	var ur UnreadResult
	worker.Call("unread", UnreadArgs{}, &ur)
	if ur.Count != 0 {
		t.Fatalf("an ask that got its error also bounced: %+v", ur)
	}
}
