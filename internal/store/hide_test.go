package store

import (
	"testing"
	"time"
)

// SPEC §6 (decided 2026-11-24, option A): the person's delete hides mail
// from the person's views only. The messages stay where they are, for the
// agents, until expiry.
func TestHide_ThreadAndMessagesForThePersonOnly(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "worker", "reviewer", "brief")
	reply, _ := s.Send(SendRequest{From: "reviewer", To: "worker", Text: "ok", ReplyTo: root.MID})
	other := send(t, s, "worker", "human", "to the person")
	second, _ := s.Send(SendRequest{From: "worker", To: "human", Text: "and more", ReplyTo: other.MID})

	n, err := s.HideThread(root.ThreadID)
	if err != nil || n != 2 {
		t.Fatalf("hid %d %v", n, err)
	}
	if th := s.Thread(root.ThreadID); len(th) != 2 {
		t.Fatalf("hiding removed mail: %+v", th)
	}
	if !s.Hidden(root) || !s.Hidden(reply) || s.Hidden(other) {
		t.Fatal("hidden marks wrong")
	}
	if n, _ := s.HideThread(root.ThreadID); n != 0 {
		t.Fatalf("hid an already hidden thread again: %d", n)
	}
	if n, _ := s.HideThread("nope"); n != 0 {
		t.Fatalf("hid %d of an unknown thread", n)
	}
	// One message: the person's unread skips it, the box still holds it.
	if n, err := s.HideMessages(second.MID, "nope"); err != nil || n != 1 {
		t.Fatalf("hid %d %v", n, err)
	}
	if n, _ := s.Unread(Human); n != 1 {
		t.Fatalf("person's unread %d, want 1", n)
	}
	if got, _ := s.Read(ReadQuery{Mailbox: Human, SkipHidden: true, Mark: true}); len(got) != 1 || got[0].MID != other.MID {
		t.Fatalf("person reads %+v", got)
	}
	if m, _ := s.Get(second.MID); m.ReadAt != 0 {
		t.Fatal("a hidden message was marked read")
	}
	if got, _ := s.Peek(PeekQuery{Mailbox: Human}); len(got) != 2 {
		t.Fatalf("the box no longer holds it: %+v", got)
	}
	if got, _ := s.Peek(PeekQuery{Mailbox: Human, SkipHidden: true}); len(got) != 1 {
		t.Fatalf("peek for the person: %+v", got)
	}
	// An agent's own mail is untouched.
	if n, _ := s.Unread("reviewer"); n != 1 {
		t.Fatalf("reviewer's unread %d", n)
	}
}

// New mail in a hidden thread stays hidden, unless it is to or from the
// person: that brings the thread back.
func TestHide_NewMailInAHiddenThread(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "worker", "reviewer", "brief")
	s.HideThread(root.ThreadID)
	more, _ := s.Send(SendRequest{From: "reviewer", To: "worker", Text: "between agents", ReplyTo: root.MID})
	if !s.Hidden(more) {
		t.Fatal("mail between agents brought the thread back")
	}
	in, _, err := s.Accept("worker", Message{MID: "s2:9", ThreadID: root.ThreadID, From: "lead@s2", To: "worker", Text: "from afar", Kind: KindReply})
	if err != nil || !s.Hidden(in) {
		t.Fatalf("accepted mail between agents: %v %v", err, s.Hidden(in))
	}
	toMe, _ := s.Send(SendRequest{From: "worker", To: "human", Text: "you there?", ReplyTo: root.MID})
	if s.Hidden(toMe) || s.Hidden(root) || s.Hidden(more) {
		t.Fatal("mail to the person did not bring the thread back")
	}
	s.HideThread(root.ThreadID)
	in2, _, _ := s.Accept("worker", Message{MID: "s2:10", ThreadID: root.ThreadID, From: "human@s2", To: "worker", Text: "the person, elsewhere", Kind: KindReply})
	if s.Hidden(in2) || s.Hidden(root) {
		t.Fatal("mail from the person on another machine did not bring it back")
	}
}

// Hidden marks survive a restart; expiry really deletes, and takes the
// marks of what it removed with it.
func TestHide_SurvivesRestartAndExpiryCleansUp(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	db := openDB(t, dir)
	s, err := Load(Options{Now: c.now, MachineID: "m1"}, db)
	if err != nil {
		t.Fatal(err)
	}
	th := send(t, s, "worker", "reviewer", "brief")
	one := send(t, s, "worker", "human", "hide just me")
	old := send(t, s, "worker", "reviewer", "read long ago")
	s.HideThread(th.ThreadID)
	s.HideMessages(one.MID)
	s.HideThread(old.ThreadID)
	s.Read(ReadQuery{Mailbox: "reviewer", IDs: []uint64{old.ID}, Mark: true})
	db.Close()

	db2 := openDB(t, dir)
	s2, err := Load(Options{Now: c.now, MachineID: "m1"}, db2)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Hidden(th) || !s2.Hidden(one) || !s2.Hidden(old) {
		t.Fatal("hidden marks lost in a restart")
	}
	c.tick(8 * 24 * time.Hour)
	if n, err := s2.Expire(7 * 24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("expire %d %v", n, err)
	}
	if _, ok := s2.Get(old.MID); ok {
		t.Fatal("expiry did not delete")
	}
	var marks int
	if err := db2.db.QueryRow(`SELECT count(*) FROM hidden`).Scan(&marks); err != nil || marks != 2 {
		t.Fatalf("hidden rows %d %v, want 2 (the expired thread's mark gone)", marks, err)
	}
}
