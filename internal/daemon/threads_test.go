package daemon

import (
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

func TestThreads_RowsAndVisibility(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "w1"}, Placement{Proof: store.ProofToken, Principal: "spy"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	w1 := dialAs(t, sock, "w1")
	spy := dialAs(t, sock, "spy")
	var sr SendResult
	w1.Call("send", SendArgs{To: "human", Text: "status\nmore", Subject: "report"}, &sr)
	person.Call("send", SendArgs{To: "w1", Text: "thanks", ReplyTo: sr.Message.MID}, nil)
	person.Call("send", SendArgs{To: "other", Text: "private"}, nil)
	var tr ThreadsResult
	if err := person.Call("threads", nil, &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Threads) != 2 {
		t.Fatalf("%+v", tr)
	}
	// Newest activity first: the reply to w1's thread came after... the private one is newest.
	var rep *ThreadRow
	for i := range tr.Threads {
		if tr.Threads[i].ThreadID == sr.Message.MID {
			rep = &tr.Threads[i]
		}
	}
	if rep == nil || rep.Count != 2 || rep.Subject != "report" || rep.First != "status" || rep.LastFrom != "human" || rep.Verified != "human" || rep.Unread != 2 {
		t.Fatalf("%+v", rep)
	}
	// w1 sees its own thread only; spy sees nothing.
	w1.Call("threads", nil, &tr)
	if len(tr.Threads) != 1 || tr.Threads[0].ThreadID != sr.Message.MID {
		t.Fatalf("%+v", tr)
	}
	spy.Call("threads", nil, &tr)
	if len(tr.Threads) != 0 {
		t.Fatalf("spy sees %+v", tr)
	}
}
