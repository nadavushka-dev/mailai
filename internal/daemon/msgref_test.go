package daemon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §12: a reply names the number in the fence header (54 or #54), a
// message in the caller's own mailbox; a full mid works too. An agent
// never sees mids, so without this it answers with a fresh send and a new
// thread (found on UbCl).
func TestReplyByHeaderNumber(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"},
		Placement{Proof: store.ProofToken, Principal: "other"})
	sock, _ := startDaemon(t, pl)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	other := dialAs(t, sock, "other")
	var task SendResult
	if err := reviewer.Call("send", SendArgs{To: "worker", Text: "please review"}, &task); err != nil {
		t.Fatal(err)
	}
	n := task.Message.ID
	for _, ref := range []string{fmt.Sprint(n), fmt.Sprintf("#%d", n), task.Message.MID} {
		var r SendResult
		if err := worker.Call("send", SendArgs{To: "reviewer", Text: "done", ReplyTo: ref}, &r); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if r.Message.ReplyTo != task.Message.MID || r.Message.ThreadID != task.Message.ThreadID || r.Message.Kind != store.KindReply {
			t.Fatalf("%s: %+v", ref, r.Message)
		}
	}
	// read --thread takes the number too.
	var rr ReadResult
	if err := worker.Call("read", ReadArgs{Thread: fmt.Sprintf("#%d", n)}, &rr); err != nil || len(rr.Messages) != 4 {
		t.Fatalf("%v %d", err, len(rr.Messages))
	}
	// The number is a message in the caller's own mailbox, or one it sent
	// (2026-11-24: reviewer's own number now works, see
	// TestReplyByOwnSentHeaderNumber): other neither holds nor sent it, and
	// an unknown number says to pass the mid.
	for _, c := range []struct {
		who interface {
			Call(string, any, any) error
		}
		ref string
	}{{other, fmt.Sprint(n)}, {worker, "#999"}, {worker, "999"}} {
		err := c.who.Call("send", SendArgs{To: "worker", Text: "x", ReplyTo: c.ref}, nil)
		num := strings.TrimPrefix(c.ref, "#")
		if err == nil || !strings.Contains(err.Error(), "#"+num) || !strings.Contains(err.Error(), "mid") {
			t.Fatalf("%s: %v", c.ref, err)
		}
	}
}

// Found on UbCl (2026-11-24): `reply 103` where #103 was the caller's own
// sent message said "no message #103 in your mailbox". The number names a
// message in the caller's mailbox or one it sent; anyone else's number is
// not found, and the hint names what works: the mid with --to.
func TestReplyByOwnSentHeaderNumber(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "conductor"}, Placement{Proof: store.ProofToken, Principal: "lane"},
		Placement{Proof: store.ProofToken, Principal: "other"})
	sock, _ := startDaemon(t, pl)
	conductor := dialAs(t, sock, "conductor")
	var own SendResult
	if err := conductor.Call("send", SendArgs{To: "lane", Text: "brief"}, &own); err != nil {
		t.Fatal(err)
	}
	n := own.Message.ID
	for _, ref := range []string{fmt.Sprint(n), fmt.Sprintf("#%d", n)} {
		var r SendResult
		if err := conductor.Call("send", SendArgs{To: "lane", Text: "more", ReplyTo: ref}, &r); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if r.Message.ReplyTo != own.Message.MID || r.Message.ThreadID != own.Message.MID {
			t.Fatalf("%s: %+v", ref, r.Message)
		}
		var rr ReadResult
		if err := conductor.Call("read", ReadArgs{Thread: ref}, &rr); err != nil || len(rr.Messages) == 0 {
			t.Fatalf("read --thread %s: %v %+v", ref, err, rr)
		}
	}
	// Neither in other's mailbox nor sent by it.
	err := dialAs(t, sock, "other").Call("send", SendArgs{To: "lane", Text: "x", ReplyTo: fmt.Sprint(n)}, nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("#%d", n)) || !strings.Contains(err.Error(), "--to") ||
		!strings.Contains(err.Error(), "mid") || !strings.Contains(err.Error(), "or sent by you") {
		t.Fatalf("%v", err)
	}
	// What the hint says works.
	var r SendResult
	if err := dialAs(t, sock, "other").Call("send", SendArgs{To: "lane", Text: "x", ReplyTo: own.Message.MID}, &r); err != nil || r.Message.ThreadID != own.Message.MID {
		t.Fatalf("%v %+v", err, r.Message)
	}
	err = dialAs(t, sock, "other").Call("read", ReadArgs{Thread: fmt.Sprint(n)}, nil)
	if err == nil || !strings.Contains(err.Error(), "mid") || strings.Contains(err.Error(), "--to") {
		t.Fatalf("read --thread hint: %v", err)
	}
}

// SPEC §12 (2026-11-25): `read <#>` reads one message by the number in its
// fence header, resolved as reply resolves it (a message in the caller's
// mailbox or one it sent), or by its mid. Found on UbCl: agents read a whole
// thread, or everything, to see one message. Reading one's own mail by
// number marks that message read (and only it); --peek does not; a sent
// message read back is not marked read for its recipient.
func TestReadOneMessageByHeaderNumber(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, d := startDaemon(t, pl)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	var a, b, sent SendResult
	reviewer.Call("send", SendArgs{To: "worker", Text: "first"}, &a)
	reviewer.Call("send", SendArgs{To: "worker", Text: "second"}, &b)
	worker.Call("send", SendArgs{To: "reviewer", Text: "mine"}, &sent)
	one := func(c interface{ Call(string, any, any) error }, args ReadArgs) store.Message {
		t.Helper()
		var rr ReadResult
		if err := c.Call("read", args, &rr); err != nil || len(rr.Messages) != 1 {
			t.Fatalf("%+v: %v %+v", args, err, rr)
		}
		return rr.Messages[0]
	}
	if m := one(worker, ReadArgs{Message: fmt.Sprint(b.Message.ID), Peek: true}); m.MID != b.Message.MID || m.Text != "second" {
		t.Fatalf("peek: %+v", m)
	}
	if n, _ := d.st.Unread("worker"); n != 2 {
		t.Fatalf("a peek marked read: %d unread", n)
	}
	if m := one(worker, ReadArgs{Message: fmt.Sprintf("#%d", b.Message.ID)}); m.MID != b.Message.MID || m.ReadAt == 0 {
		t.Fatalf("#n: %+v", m)
	}
	if n, _ := d.st.Unread("worker"); n != 1 {
		t.Fatalf("read by number marked %d unread, want 1 left", n)
	}
	if m := one(worker, ReadArgs{Message: a.Message.MID}); m.MID != a.Message.MID || m.ReadAt == 0 {
		t.Fatalf("mid: %+v", m)
	}
	// One's own sent message, by its number: shown, not marked read for
	// the recipient.
	if m := one(worker, ReadArgs{Message: fmt.Sprint(sent.Message.ID)}); m.MID != sent.Message.MID || m.Text != "mine" {
		t.Fatalf("own sent: %+v", m)
	}
	if n, _ := d.st.Unread("reviewer"); n != 1 {
		t.Fatalf("reading your own sent message marked it read for its recipient: %d", n)
	}
	// Unknown: the hint names the mid, and a mid not held says so.
	for _, ref := range []string{"#999", "999", "ubcl:999"} {
		err := worker.Call("read", ReadArgs{Message: ref}, nil)
		if err == nil || !strings.Contains(err.Error(), "999") {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	if err := worker.Call("read", ReadArgs{Message: "999"}, nil); !strings.Contains(err.Error(), "mid") {
		t.Fatalf("hint: %v", err)
	}
}
