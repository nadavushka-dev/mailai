package daemon

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Found on UbCl (2026-11-24): the person dismissed an ask-human in the
// Inbox and the asking agent stayed blocked. Dismiss ends a blocking wait
// as withdraw does (SPEC §7a), and leaves one fixed line from mailai in the
// ask's thread, so a --no-wait asker learns of it too.

// dismissedNote is the asker's own mail in the ask's thread from the
// daemon, read or not.
func dismissedNote(t *testing.T, c *client.Client, thread string) []store.Message {
	t.Helper()
	var rr ReadResult
	if err := c.Call("read", ReadArgs{Peek: true, Thread: thread}, &rr); err != nil {
		t.Fatal(err)
	}
	var out []store.Message
	for _, m := range rr.Messages {
		if m.From == Bouncer {
			out = append(out, m)
		}
	}
	return out
}

// A blocking ask-human ends when the person dismisses it: not_found, one
// line, the same as a withdrawn one. Its ask slot is freed, and the note
// it would otherwise be woken by is already read: the error told it.
func TestDismiss_EndsTheWaitingAskHuman(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	waiter := rawDialAs(t, sock, "worker")
	defer waiter.Close()
	errc := make(chan error, 1)
	var r AskHumanResult
	go func() {
		errc <- waiter.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}}, &r)
	}()
	items := waitItems(t, person, 1)
	id := items.Items[0].ID
	waitOpen(t, d, "worker", 1)
	if err := person.Call("dismiss", DismissArgs{Item: id}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		want := fmt.Sprintf("not_found: #%d was dismissed by the person", id)
		if client.Code(err) != proto.CodeNotFound || err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q (reply %+v)", err, want, r.Reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting ask-human never ended")
	}
	waitOpen(t, d, "worker", 0)
	if it := itemByID(t, person, id); it.ClosedBy != "dismissed" || it.Answer != "" {
		t.Fatalf("%+v", it)
	}
	worker := dialAs(t, sock, "worker")
	notes := dismissedNote(t, worker, items.Items[0].ThreadID)
	if len(notes) != 1 || notes[0].ReadAt == 0 {
		t.Fatalf("want one note, already read: %+v", notes)
	}
	var u UnreadResult
	if err := worker.Call("unread", nil, &u); err != nil || u.Count != 0 {
		t.Fatalf("unread %+v %v", u, err)
	}
}

// A blocking `ask human` (a free-text ask to the person) is an Inbox item
// too; dismissing it ends the wait the same way.
func TestDismiss_EndsTheWaitingAskToHuman(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	waiter := rawDialAs(t, sock, "worker")
	defer waiter.Close()
	errc := make(chan error, 1)
	go func() { errc <- waiter.Call("ask", AskArgs{To: "human", Text: "free text question"}, nil) }()
	items := waitItems(t, person, 1)
	id := items.Items[0].ID
	waitOpen(t, d, "worker", 1)
	if err := person.Call("dismiss", DismissArgs{Item: id}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		want := fmt.Sprintf("not_found: #%d was dismissed by the person", id)
		if client.Code(err) != proto.CodeNotFound || err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting ask never ended")
	}
	waitOpen(t, d, "worker", 0)
}

// A --no-wait asker gets one fixed line from mailai in the ask's thread,
// as mail: a reply to the ask, never verified, never the person's words,
// and never an answer (the item stays dismissed; --wait-for still says it
// ended without one).
func TestDismiss_NoWaitAskerGetsOneLineFromMailai(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Proceed?", Options: []string{"yes", "no"}, NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	// It waits for mail, as an agent at rest is woken by it.
	got := make(chan ReadResult, 1)
	go func() {
		var rr ReadResult
		dialAs(t, sock, "worker").Call("wait", WaitArgs{TimeoutMS: 3000}, &rr)
		got <- rr
	}()
	time.Sleep(50 * time.Millisecond)
	if err := person.Call("dismiss", DismissArgs{Item: r.Item.ID}, nil); err != nil {
		t.Fatal(err)
	}
	rr := <-got
	if len(rr.Messages) != 1 {
		t.Fatalf("wait got %+v", rr.Messages)
	}
	n := rr.Messages[0]
	want := fmt.Sprintf("#%d dismissed by the person", r.Item.ID)
	if n.From != Bouncer || n.To != "worker" || n.Kind != store.KindReply || n.ReplyTo != r.Item.MID ||
		n.ThreadID != r.Item.ThreadID || n.Text != want || n.Verified != "" || n.ClaimedHuman || n.SenderProof != "" {
		t.Fatalf("note %+v", n)
	}
	if it := itemByID(t, person, r.Item.ID); it.ClosedBy != "dismissed" || it.Answer != "" {
		t.Fatalf("the note answered it: %+v", it)
	}
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: r.Item.ID}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("--wait-for after a dismiss: %v", err)
	}
	if notes := dismissedNote(t, worker, r.Item.ThreadID); len(notes) != 1 {
		t.Fatalf("want exactly one note: %+v", notes)
	}
}

// An asker on another machine gets no note from here: the spoke would take
// any reply in the thread as the answer (a known gap, SPEC §7a).
func TestDismiss_NoNoteForAnAskerOnAnotherMachine(t *testing.T) {
	sock, _ := startDaemon(t, placed())
	person := dial(t, sock)
	var r AskHumanResult
	if err := person.Call("ask-human", AskHumanArgs{From: "builder@s2", Text: "Proceed?", NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	if err := person.Call("dismiss", DismissArgs{Item: r.Item.ID}, nil); err != nil {
		t.Fatal(err)
	}
	var rr ReadResult
	if err := person.Call("read", ReadArgs{Mailbox: "builder@s2", Peek: true}, &rr); err != nil {
		t.Fatal(err)
	}
	for _, m := range rr.Messages {
		if m.From == Bouncer {
			t.Fatalf("a note went to the spoke's asker: %+v", m)
		}
	}
}

// The web thread shows mailai's line as a system note, like the item
// notes: no bubble, no sender header, no trust mark (SPEC §7a, §12a
// parity with the TUI). The person's thread carries it, with no proof.
func TestWeb_DismissNoteIsASystemNote(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Proceed?", NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	if err := person.Call("dismiss", DismissArgs{Item: r.Item.ID}, nil); err != nil {
		t.Fatal(err)
	}
	cl, base := webClient(t, person)
	_, page := get(t, cl, base+"/")
	for _, want := range []string{"function isDaemonNote(m)", "m.from === 'mailai' && !m.sender_proof", "if (isDaemonNote(m)) return `<p class=\"sysnote\">"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	_, body := get(t, cl, base+"/api/thread?id="+url.QueryEscape(r.Item.ThreadID))
	var th ReadResult
	if err := json.Unmarshal([]byte(body), &th); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range th.Messages {
		if m.From == Bouncer {
			found = m.SenderProof == "" && m.Verified == "" && IsDaemonNote(m)
		}
	}
	if !found {
		t.Fatalf("the person's thread lacks the note: %s", body)
	}
}
