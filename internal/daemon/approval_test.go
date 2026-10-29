package daemon

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §8: a hook holding a permission request blocks until the person
// decides from the Inbox; the harness gets allow or deny; a timeout closes
// the item and returns no decision so the harness falls back to its prompt.
func TestHoldApproval_DecidedFromInbox(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofPane, Principal: "w1:p3"})
	sock, _ := startDaemon(t, pl)
	hook := dialAs(t, sock, "w1:p3")
	person := dial(t, sock)
	type res struct {
		r   HoldApprovalResult
		err error
	}
	got := make(chan res, 1)
	go func() {
		var r HoldApprovalResult
		err := hook.Call("hold-approval", HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "rm -rf node_modules", Risky: true, TimeoutMS: 5000}, &r)
		got <- res{r, err}
	}()
	var inbox InboxResult
	deadline := time.Now().Add(2 * time.Second)
	for len(inbox.Items) == 0 && time.Now().Before(deadline) {
		person.Call("inbox", nil, &inbox)
		time.Sleep(5 * time.Millisecond)
	}
	it := inbox.Items[0]
	if it.Kind != store.ItemApproval || it.From != "w1:p3" || it.Subject != "Bash" || it.Question != "rm -rf node_modules" || !it.Risky || it.Harness != "claude-code" || len(it.Options) != 2 {
		t.Fatalf("%+v", it)
	}
	if err := person.Call("answer", AnswerArgs{Item: it.ID, Text: "maybe"}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("%v", err)
	}
	if err := hook.Call("answer", AnswerArgs{Item: it.ID, Text: "allow"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("the asking pane approving itself: %v", err)
	}
	if err := person.Call("answer", AnswerArgs{Item: it.ID, Text: "deny", Message: "not on main"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.r.Decision != "deny" || r.r.Message != "not on main" || r.r.By != "human" || r.r.Item.ClosedBy != "answered" || r.r.Item.Answer != "deny" {
			t.Fatalf("%+v %v", r.r, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not return")
	}
	// Timeout: no decision, item closed as timeout.
	var r HoldApprovalResult
	if err := hook.Call("hold-approval", HoldApprovalArgs{Tool: "Edit", Summary: "x", TimeoutMS: 30}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Decision != "" || r.Item.ClosedBy != "timeout" {
		t.Fatalf("%+v", r)
	}
	person.Call("inbox", nil, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("%+v", inbox)
	}
	// Outside callers cannot hold approvals: only an agent's own hook.
	if err := person.Call("hold-approval", HoldApprovalArgs{Tool: "Bash", Summary: "x", TimeoutMS: 10}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
}
