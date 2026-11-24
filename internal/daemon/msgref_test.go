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
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemon(t, pl)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
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
	// The number is in the caller's own mailbox only: reviewer holds no
	// message with that id, and an unknown number says to pass the mid.
	for _, c := range []struct {
		who interface {
			Call(string, any, any) error
		}
		ref string
	}{{reviewer, fmt.Sprint(n)}, {worker, "#999"}, {worker, "999"}} {
		err := c.who.Call("send", SendArgs{To: "worker", Text: "x", ReplyTo: c.ref}, nil)
		num := strings.TrimPrefix(c.ref, "#")
		if err == nil || !strings.Contains(err.Error(), "#"+num) || !strings.Contains(err.Error(), "mid") {
			t.Fatalf("%s: %v", c.ref, err)
		}
	}
}
