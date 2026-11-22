package daemon

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Found in the UbCl trial: an agent waiting for mail (not a reply to an ask)
// had no way to block, so it polled `mailai unread` every 5 s forever.
// wait blocks until the caller's own mailbox holds unread mail and returns
// it read, as read --unread does.
func TestWait_ReturnsWhenMailArrives(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemon(t, pl)
	builder := dialAs(t, sock, "builder")

	got := make(chan ReadResult, 1)
	errc := make(chan error, 1)
	go func() {
		var rr ReadResult
		if err := builder.Call("wait", WaitArgs{TimeoutMS: 5000}, &rr); err != nil {
			errc <- err
			return
		}
		got <- rr
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case rr := <-got:
		t.Fatalf("wait returned with nothing unread: %+v", rr)
	case err := <-errc:
		t.Fatal(err)
	default:
	}
	// Mail to someone else does not end it.
	dialAs(t, sock, "reviewer").Call("send", SendArgs{To: "human", Text: "not for builder"}, nil)
	time.Sleep(100 * time.Millisecond)
	if len(got) != 0 {
		t.Fatal("mail to another mailbox ended the wait")
	}
	dial(t, sock).Call("send", SendArgs{To: "builder", Text: "rate is 0.92"}, nil)
	select {
	case rr := <-got:
		if len(rr.Messages) != 1 || rr.Messages[0].Text != "rate is 0.92" {
			t.Fatalf("wait returned %+v", rr.Messages)
		}
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not return when mail arrived")
	}
	// It came back read.
	var ur UnreadResult
	builder.Call("unread", UnreadArgs{}, &ur)
	if ur.Count != 0 {
		t.Fatalf("still unread: %d", ur.Count)
	}
}

// Unread mail already there: wait returns at once. Nothing: it times out
// with the timeout code (the CLI exits 2).
func TestWait_AlreadyUnreadTimeoutAndNobody(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"})
	sock, _ := startDaemon(t, pl)
	builder := dialAs(t, sock, "builder")
	dial(t, sock).Call("send", SendArgs{To: "builder", Text: "waiting already"}, nil)
	start := time.Now()
	var rr ReadResult
	if err := builder.Call("wait", WaitArgs{TimeoutMS: 5000}, &rr); err != nil || len(rr.Messages) != 1 {
		t.Fatalf("%v %+v", err, rr)
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait with mail already unread should return at once")
	}
	if err := builder.Call("wait", WaitArgs{TimeoutMS: 150}, nil); client.Code(err) != proto.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
}
