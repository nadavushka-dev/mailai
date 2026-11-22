package store

import (
	"strings"
	"testing"
	"time"
)

// SPEC §13a: a spoke's off-machine sends wait in an outbox, durable, with a
// mid minted from the store's own counter, until the inbox machine has them.
func TestOutbox_MintsMIDsDurablyInOrder(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	db := openDB(t, dir)
	s, err := Load(Options{Now: c.now, MachineID: "s2"}, db)
	if err != nil {
		t.Fatal(err)
	}
	local := send(t, s, "a", "worker", "local")
	parent := send(t, s, "reviewer@s1", "worker", "from the hub")
	o1, err := s.Enqueue(Outgoing{From: "worker", To: "human", Kind: KindAskHuman, Text: "deploy?", Options: []string{"yes", "no"}})
	if err != nil {
		t.Fatal(err)
	}
	o2, err := s.Enqueue(Outgoing{From: "worker", To: "reviewer@s1", Text: "done", ReplyTo: parent.MID})
	if err != nil {
		t.Fatal(err)
	}
	if o1.MID != "s2:3" || o1.ThreadID != o1.MID || o1.ID <= parent.ID || o1.QueuedAt != c.now().UnixNano() {
		t.Fatalf("%+v", o1)
	}
	if o2.ThreadID != parent.ThreadID || o2.Kind != KindReply {
		t.Fatalf("reply thread: %+v", o2)
	}
	next := send(t, s, "a", "worker", "after")
	if next.MID == o1.MID || next.MID == o2.MID || next.ID <= o2.ID {
		t.Fatalf("mid reused: %+v", next)
	}
	_ = local
	// Refused before queueing: what Send would refuse.
	for _, bad := range []Outgoing{{From: "worker", To: "x@s1", Text: " "}, {From: "worker", To: "x@s1", Text: "x", Subject: "a\nb"},
		{From: "worker", To: "x@s1", Text: strings.Repeat("x", MaxText+1)}} {
		if _, err := s.Enqueue(bad); err == nil {
			t.Fatalf("queued %+v", bad)
		}
	}
	db.Close()

	db2 := openDB(t, dir)
	s2, err := Load(Options{Now: c.now, MachineID: "s2"}, db2)
	if err != nil {
		t.Fatal(err)
	}
	out := s2.Outbox()
	if len(out) != 2 || out[0].MID != o1.MID || out[1].MID != o2.MID || out[0].Options[1] != "no" {
		t.Fatalf("%+v", out)
	}
	if err := s2.Dequeue(o1.ID); err != nil {
		t.Fatal(err)
	}
	after := send(t, s2, "a", "worker", "after restart")
	if after.ID <= next.ID {
		t.Fatalf("counter went back: %+v", after)
	}
	db2.Close()
	db3 := openDB(t, dir)
	s3, _ := Load(Options{Now: c.now, MachineID: "s2"}, db3)
	if out := s3.Outbox(); len(out) != 1 || out[0].MID != o2.MID {
		t.Fatalf("dequeue not durable: %+v", out)
	}
}

// The inbox machine stores a spoke's message under the spoke's mid, once.
func TestSendMID_KeepsTheSpokesMIDAndDedupes(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "reviewer", "worker@s2", "do X")
	m, fresh, err := s.SendMID(SendRequest{From: "worker@s2", To: "reviewer", Text: "done", ReplyTo: root.MID, Remote: true}, "s2:9", "")
	if err != nil || !fresh || m.MID != "s2:9" || m.ThreadID != root.ThreadID || m.Kind != KindReply {
		t.Fatalf("%+v fresh=%v %v", m, fresh, err)
	}
	again, fresh, err := s.SendMID(SendRequest{From: "worker@s2", To: "reviewer", Text: "done", ReplyTo: root.MID, Remote: true}, "s2:9", "")
	if err != nil || fresh || again.ID != m.ID {
		t.Fatalf("duplicate: %+v fresh=%v %v", again, fresh, err)
	}
	if n, _ := s.Unread("reviewer"); n != 1 {
		t.Fatalf("unread %d", n)
	}
	// A reply whose parent is not held here keeps the spoke's thread.
	m, _, _ = s.SendMID(SendRequest{From: "worker@s2", To: "reviewer", Text: "x", ReplyTo: "s3:4"}, "s2:10", "s3:1")
	if m.ThreadID != "s3:1" || !m.ReplyToMissing {
		t.Fatalf("%+v", m)
	}
	// A duplicate spends no rate-limit token.
	for i := 0; i < 2*RateBurst; i++ {
		if _, _, err := s.SendMID(SendRequest{From: "worker@s2", To: "reviewer", Text: "done", ReplyTo: root.MID, Remote: true}, "s2:9", ""); err != nil {
			t.Fatalf("duplicate %d: %v", i, err)
		}
	}
}

// What the daemon writes itself (a bounce) is not rate limited.
func TestSend_DaemonMailIsNotRateLimited(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	for i := 0; i < 2*RateBurst; i++ {
		if _, err := s.Send(SendRequest{From: "mailai", To: "worker", Text: "bounce", NoLimit: true}); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
}
