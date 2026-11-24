package daemon

import (
	"fmt"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// personSees says whether a thread shows in the person's thread list.
func personSees(t *testing.T, person *client.Client, thread string) bool {
	t.Helper()
	var tr ThreadsResult
	if err := person.Call("threads", nil, &tr); err != nil {
		t.Fatal(err)
	}
	for _, r := range tr.Threads {
		if r.ThreadID == thread {
			return true
		}
	}
	return false
}

// readThread is read --thread as c.
func readThread(t *testing.T, c *client.Client, thread string) []store.Message {
	t.Helper()
	var rr ReadResult
	if err := c.Call("read", ReadArgs{Thread: thread}, &rr); err != nil {
		t.Fatal(err)
	}
	return rr.Messages
}

// SPEC §6 (decided 2026-11-24, option A): the person's delete hides a
// thread from the person's views only. The agents keep their mail (their
// mailbox, reply <#>, read --thread) until expiry.
func TestDelete_HidesFromThePersonAgentsKeepTheirMail(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	var brief, toMe SendResult
	worker.Call("send", SendArgs{To: "reviewer", Text: "the brief"}, &brief)
	worker.Call("send", SendArgs{To: "human", Text: "for you"}, &toMe)
	th := brief.Message.ThreadID

	if err := person.Call("delete", DeleteArgs{Thread: th}, nil); err != nil {
		t.Fatal(err)
	}
	if personSees(t, person, th) || len(readThread(t, person, th)) != 0 {
		t.Fatal("the person still sees a deleted thread")
	}
	// The reviewer's mailbox still has the brief, by number too.
	var rr ReadResult
	if err := reviewer.Call("read", ReadArgs{Peek: true}, &rr); err != nil || len(rr.Messages) != 1 || rr.Messages[0].MID != brief.Message.MID {
		t.Fatalf("the reviewer's mailbox: %v %+v", err, rr.Messages)
	}
	if err := reviewer.Call("send", SendArgs{ReplyTo: fmt.Sprint(rr.Messages[0].ID), To: "worker", Text: "on it"}, nil); err != nil {
		t.Fatalf("reply <#>: %v", err)
	}
	if got := readThread(t, worker, th); len(got) != 2 {
		t.Fatalf("the worker's read --thread: %+v", got)
	}
	var wt ThreadsResult
	worker.Call("threads", nil, &wt)
	if len(wt.Threads) != 2 {
		t.Fatalf("the worker's threads: %+v", wt.Threads)
	}
	// The reply between agents stays hidden from the person.
	if personSees(t, person, th) {
		t.Fatal("mail between agents brought the thread back")
	}

	// One of the person's own messages: hidden from the person's mailbox
	// and unread count; the worker who sent it still has it in the thread.
	if err := person.Call("delete", DeleteArgs{MIDs: []string{toMe.Message.MID}}, nil); err != nil {
		t.Fatal(err)
	}
	var ur UnreadResult
	if err := person.Call("unread", nil, &ur); err != nil || ur.Count != 0 {
		t.Fatalf("person's unread %v %+v", err, ur)
	}
	if err := person.Call("read", ReadArgs{}, &rr); err != nil || len(rr.Messages) != 0 {
		t.Fatalf("person's mailbox %v %+v", err, rr.Messages)
	}
	if got := readThread(t, worker, toMe.Message.ThreadID); len(got) != 1 {
		t.Fatalf("the worker lost its message: %+v", got)
	}

	// Mail to the person in the hidden thread brings it back.
	if err := worker.Call("send", SendArgs{To: "human", Text: "need you here", ReplyTo: brief.Message.MID}, nil); err != nil {
		t.Fatal(err)
	}
	if !personSees(t, person, th) || len(readThread(t, person, th)) != 3 {
		t.Fatalf("the thread did not come back: %+v", readThread(t, person, th))
	}
}
