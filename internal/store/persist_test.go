package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openDB(t *testing.T, dir string) *SQLite {
	t.Helper()
	db, err := OpenSQLite(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// SPEC §11: the store survives a daemon restart: messages, read and seen
// marks, evictions, thread ids and the id counter all come back.
func TestPersist_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	lim := Limits{MaxMessages: 3, MaxBytes: 1 << 20}

	db := openDB(t, dir)
	s, err := Load(Options{Now: c.now, Limits: lim, MachineID: "m1"}, db)
	if err != nil {
		t.Fatal(err)
	}
	root := send(t, s, "sup", "worker", "do X")
	reply, _ := s.Send(SendRequest{From: "worker", To: "sup", Text: "done", ReplyTo: root.MID})
	send(t, s, "a", "worker", "2")
	send(t, s, "a", "worker", "3")
	s.Read(ReadQuery{Mailbox: "worker", IDs: []uint64{root.ID}, Mark: true})
	c.tick(time.Second)
	s.Peek(PeekQuery{Mailbox: "sup", By: "sup"})
	send(t, s, "a", "worker", "4") // evicts root (the only read one)
	if s.Evicted("worker") != 1 {
		t.Fatalf("evicted %d", s.Evicted("worker"))
	}
	db.Close()

	db2 := openDB(t, dir)
	s2, err := Load(Options{Now: c.now, Limits: lim, MachineID: "m1"}, db2)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := s2.Read(ReadQuery{Mailbox: "worker"})
	if ids := idsOf(w); len(ids) != 3 || ids[0] != 3 || ids[2] != 5 {
		t.Fatalf("worker after restart: %v", ids)
	}
	sup, _ := s2.Read(ReadQuery{Mailbox: "sup"})
	if len(sup) != 1 || sup[0].MID != reply.MID || sup[0].ThreadID != root.MID || sup[0].SeenAt == 0 || sup[0].ReadAt != 0 {
		t.Fatalf("sup after restart: %+v", sup)
	}
	if s2.Evicted("worker") != 1 {
		t.Fatalf("evicted count lost")
	}
	next := send(t, s2, "x", "y", "z")
	if next.ID != 6 || next.MID != "m1:6" {
		t.Fatalf("id counter not restored: %+v", next)
	}
	// The evicted message is gone from disk, not only from memory.
	if got := s2.Thread(root.MID); len(got) != 1 {
		t.Fatalf("thread after eviction: %+v", got)
	}
}

func TestPersist_AttachmentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	s, _ := Load(Options{}, db)
	s.Send(SendRequest{From: "a", To: "b", Text: "see", Attachments: []Attachment{{Kind: "file", Path: "/tmp/x", Bytes: 3, Stashed: true}}})
	db.Close()
	db2 := openDB(t, dir)
	s2, _ := Load(Options{}, db2)
	got, _ := s2.Read(ReadQuery{Mailbox: "b"})
	if len(got) != 1 || len(got[0].Attachments) != 1 || !got[0].Attachments[0].Stashed || got[0].Attachments[0].Path != "/tmp/x" {
		t.Fatalf("%+v", got)
	}
}

// SPEC §6/§9: the sender's pane is stored with the message and comes back
// after a restart; a row an older mailai wrote, without it, still loads.
func TestPersist_SenderPaneRoundTripAndOldRows(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	if _, err := db.db.Exec(`INSERT INTO messages(id, mid, box, thread, read_at, seen_at, body) VALUES(1, 'm1:1', 'b', 'm1:1', 0, 0, ?)`,
		`{"id":1,"mid":"m1:1","kind":"mail","from":"builder","to":"b","text":"old","thread_id":"m1:1","sent_at":1,"sender_proof":"pane"}`); err != nil {
		t.Fatal(err)
	}
	s, err := Load(Options{MachineID: "m1"}, db)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Send(SendRequest{From: "builder", To: "b", Text: "new", Proof: ProofPane, SenderPane: "w4Y:p1"})
	if err != nil || m.SenderPane != "w4Y:p1" {
		t.Fatalf("%+v %v", m, err)
	}
	db.Close()
	s2, err := Load(Options{MachineID: "m1"}, openDB(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Read(ReadQuery{Mailbox: "b"})
	if len(got) != 2 || got[0].Text != "old" || got[0].SenderPane != "" || got[1].SenderPane != "w4Y:p1" {
		t.Fatalf("%+v", got)
	}
}

// Without a persister the store is purely in memory, as the tests above use.
func TestPersist_NilPersisterIsMemoryOnly(t *testing.T) {
	s, err := Load(Options{}, nil)
	if err != nil || s == nil {
		t.Fatal(err)
	}
	send(t, s, "a", "b", "x")
}
