package daemon

import (
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Found in the sandbox: `mailai reply <mid>` finds whom to answer by reading
// the thread named <mid>, so replying to any message but a thread's first
// failed with "no message ... is held". read --thread takes any message in
// the thread (SPEC §12: --thread MID).
func TestRead_ThreadByAnyMessageInIt(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "planner"}, Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	planner := dialAs(t, sock, "planner")
	worker := dialAs(t, sock, "worker")
	var root, rep SendResult
	planner.Call("send", SendArgs{To: "worker", Text: "estimate?"}, &root)
	worker.Call("send", SendArgs{To: "planner", Text: "2 days", ReplyTo: root.Message.MID}, &rep)

	var rr ReadResult
	if err := planner.Call("read", ReadArgs{Thread: rep.Message.MID}, &rr); err != nil {
		t.Fatal(err)
	}
	if len(rr.Messages) != 2 || rr.Messages[0].MID != root.Message.MID || rr.Messages[1].MID != rep.Message.MID {
		t.Fatalf("thread by its reply: %+v", rr.Messages)
	}
}
