package store

import (
	"testing"
	"time"
)

// SPEC §6 Keeping: a message read more than keep_days ago is deleted;
// unread mail is never deleted by age, however old.
func TestExpire_DropsOnlyMailReadLongAgo(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	old := send(t, s, "a", "worker", "read long ago")
	unread := send(t, s, "a", "worker", "never read")
	s.Read(ReadQuery{Mailbox: "worker", IDs: []uint64{old.ID}, Mark: true})
	c.tick(6 * 24 * time.Hour)
	recent := send(t, s, "a", "worker", "read yesterday")
	s.Read(ReadQuery{Mailbox: "worker", IDs: []uint64{recent.ID}, Mark: true})
	c.tick(24*time.Hour + time.Second)

	n, err := s.Expire(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired %d, want 1", n)
	}
	got, _ := s.Read(ReadQuery{Mailbox: "worker"})
	if ids := idsOf(got); len(ids) != 2 || ids[0] != unread.ID || ids[1] != recent.ID {
		t.Fatalf("left %v, want [%d %d]", ids, unread.ID, recent.ID)
	}
	if _, ok := s.Get(old.MID); ok {
		t.Fatal("expired message still found by mid")
	}
	if s.Evicted("worker") != 0 {
		t.Fatal("expiry is not eviction: the evicted count must not move")
	}
}

func TestExpire_ZeroKeepsEverything(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	m := send(t, s, "a", "worker", "x")
	s.Read(ReadQuery{Mailbox: "worker", Mark: true})
	c.tick(365 * 24 * time.Hour)
	if n, _ := s.Expire(0); n != 0 {
		t.Fatalf("expired %d with keep 0", n)
	}
	if _, ok := s.Get(m.MID); !ok {
		t.Fatal("message gone")
	}
}

// The person deletes a thread: every message in it, in whatever mailbox,
// read or not. Other threads stay.
func TestDeleteThread_RemovesEveryMessageInIt(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "sup", "worker", "do X")
	reply, _ := s.Send(SendRequest{From: "worker", To: "sup", Text: "done", ReplyTo: root.MID})
	other := send(t, s, "sup", "worker", "do Y")

	n, err := s.DeleteThread(root.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	if len(s.Thread(root.ThreadID)) != 0 {
		t.Fatal("thread still held")
	}
	for _, mid := range []string{root.MID, reply.MID} {
		if _, ok := s.Get(mid); ok {
			t.Fatalf("%s still found", mid)
		}
	}
	if _, ok := s.Get(other.MID); !ok {
		t.Fatal("another thread was deleted too")
	}
	if n, _ := s.DeleteThread("nope"); n != 0 {
		t.Fatalf("deleted %d from an unknown thread", n)
	}
}

func TestDelete_RemovesOneMessage(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "sup", "worker", "do X")
	reply, _ := s.Send(SendRequest{From: "worker", To: "sup", Text: "done", ReplyTo: root.MID})

	n, err := s.Delete(reply.MID, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	if th := s.Thread(root.ThreadID); len(th) != 1 || th[0].MID != root.MID {
		t.Fatalf("thread now %v", th)
	}
}

// Deletions and expiry are durable: they stay gone after a restart.
func TestPersist_DeleteAndExpireSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	db := openDB(t, dir)
	s, err := Load(Options{Now: c.now, MachineID: "m1"}, db)
	if err != nil {
		t.Fatal(err)
	}
	gone := send(t, s, "a", "worker", "delete me")
	old := send(t, s, "a", "worker", "read long ago")
	kept := send(t, s, "a", "worker", "keep me")
	s.Read(ReadQuery{Mailbox: "worker", IDs: []uint64{old.ID}, Mark: true})
	c.tick(8 * 24 * time.Hour)
	if _, err := s.Delete(gone.MID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Expire(7 * 24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	db.Close()

	db2 := openDB(t, dir)
	s2, err := Load(Options{Now: c.now, MachineID: "m1"}, db2)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Read(ReadQuery{Mailbox: "worker"})
	if ids := idsOf(got); len(ids) != 1 || ids[0] != kept.ID {
		t.Fatalf("after restart %v, want [%d]", ids, kept.ID)
	}
	if s2.Evicted("worker") != 0 {
		t.Fatalf("evicted %d after restart", s2.Evicted("worker"))
	}
}

// ExpireThreads says which threads lost mail, so the daemon can log it.
func TestExpireThreads_SaysWhichThreads(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	a := send(t, s, "a", "worker", "one")
	b := send(t, s, "a", "worker", "two")
	s.Read(ReadQuery{Mailbox: "worker", Mark: true})
	c.tick(8 * 24 * time.Hour)
	n, threads, err := s.ExpireThreads(7 * 24 * time.Hour)
	if err != nil || n != 2 || len(threads) != 2 || threads[0] != a.ThreadID || threads[1] != b.ThreadID {
		t.Fatalf("%v %d %v", err, n, threads)
	}
}
