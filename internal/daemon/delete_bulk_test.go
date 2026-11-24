package daemon

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §6 (decided 2026-11-24): the person deletes, in one go, every read
// thread between agents (no message to or from the person) on this
// machine. A dry run counts them first, for the confirm. Inbox items stay.
func TestDelete_ReadThreadsBetweenAgents(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	send := func(c *client.Client, to, text, replyTo string) SendResult {
		t.Helper()
		var r SendResult
		if err := c.Call("send", SendArgs{To: store.Address(to), Text: text, ReplyTo: replyTo}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	readAll := func(c *client.Client) {
		t.Helper()
		if err := c.Call("read", ReadArgs{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	gone := send(worker, "reviewer", "read between agents", "")
	gone2 := send(reviewer, "worker", "also read", "")
	send(reviewer, "worker", "its answer", gone.Message.MID)
	joined := send(worker, "reviewer", "the person steps in later", "")
	toMe := send(worker, "human", "to the person", "")
	var ask AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "ok?", NoWait: true}, &ask); err != nil {
		t.Fatal(err)
	}
	readAll(reviewer)
	readAll(worker)
	readAll(person)
	send(dialAs(t, sock, "reviewer"), "worker", "unread between agents", "")
	if err := person.Call("send", SendArgs{To: "reviewer", Text: "me too", ReplyTo: joined.Message.MID}, nil); err != nil {
		t.Fatal(err)
	}
	readAll(reviewer)

	if err := worker.Call("delete", DeleteArgs{ReadBetweenAgents: true}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("an agent bulk deleting: %v", err)
	}
	var dry DeleteResult
	if err := person.Call("delete", DeleteArgs{ReadBetweenAgents: true, DryRun: true}, &dry); err != nil || dry.Threads != 2 || dry.Deleted != 3 {
		t.Fatalf("dry run %v %+v", err, dry)
	}
	if len(d.st.Thread(gone.Message.ThreadID)) != 2 {
		t.Fatal("a dry run deleted")
	}
	var res DeleteResult
	if err := person.Call("delete", DeleteArgs{ReadBetweenAgents: true}, &res); err != nil || res.Threads != 2 || res.Deleted != 3 {
		t.Fatalf("%v %+v", err, res)
	}
	for _, id := range []string{gone.Message.ThreadID, gone2.Message.ThreadID} {
		if len(d.st.Thread(id)) != 0 {
			t.Fatalf("thread %s kept", id)
		}
	}
	for _, id := range []string{joined.Message.ThreadID, toMe.Message.ThreadID, ask.Item.ThreadID} {
		if len(d.st.Thread(id)) == 0 {
			t.Fatalf("thread %s deleted", id)
		}
	}
	var tr ThreadsResult
	person.Call("threads", nil, &tr)
	unread := 0
	for _, r := range tr.Threads {
		if r.First == "unread between agents" {
			unread++
		}
	}
	if unread != 1 {
		t.Fatalf("the unread thread went: %+v", tr.Threads)
	}
	var ir InboxResult
	person.Call("inbox", InboxArgs{}, &ir)
	if len(ir.Items) != 1 || ir.Items[0].ID != ask.Item.ID {
		t.Fatalf("inbox %+v", ir.Items)
	}
	// Nothing left to take is not an error: it says 0.
	res = DeleteResult{}
	if err := person.Call("delete", DeleteArgs{ReadBetweenAgents: true}, &res); err != nil || res.Threads != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	if err := person.Call("delete", DeleteArgs{ReadBetweenAgents: true, Thread: toMe.Message.ThreadID}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("bulk with a thread: %v", err)
	}
}

// The web page has one button for it, on the Between agents tab, which
// counts first (a dry run) and asks before it deletes.
func TestWeb_DeleteReadBetweenAgentsButton(t *testing.T) {
	page := webPage(t)
	for _, need := range []string{
		`id="bulk-del" data-a="delete-read-agents"`,
		"{read_between_agents: true, dry_run: true}",
		"if (!confirm(`Delete ${n} read thread",
		"act('/api/delete', {read_between_agents: true}",
		".scopes > button[data-scope]",
		".scopes > button[hidden] { display: none; }",
	} {
		if !strings.Contains(page, need) {
			t.Fatalf("page missing %s", need)
		}
	}
}
