package store

import (
	"path/filepath"
	"testing"
)

// D13: an ask to human is an Inbox item; plain mail to human is not; the
// person's verified reply in the thread closes it as answered.
func TestD13_AsksOpenItemsPlainMailDoesNot(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	send(t, s, "worker", "human", "fyi: done")
	if n := len(s.Items(false)); n != 0 {
		t.Fatalf("plain mail opened %d items", n)
	}
	ask, _ := s.Send(SendRequest{From: "worker", To: "human", Text: "merge?", Kind: KindAsk, Subject: "pr 12"})
	q, _ := s.Send(SendRequest{From: "w2", To: "human", Text: "region?", Kind: KindAskHuman, Options: []string{"us", "eu"}})
	// Asks between agents are not the person's business.
	s.Send(SendRequest{From: "a", To: "b", Text: "?", Kind: KindAsk})
	items := s.Items(false)
	if len(items) != 2 || items[0].Kind != ItemAsk || items[0].MID != ask.MID || items[0].Subject != "pr 12" ||
		items[1].Kind != ItemAskHuman || len(items[1].Options) != 2 || items[1].Question != "region?" {
		t.Fatalf("%+v", items)
	}
	// An unverified "human" reply (a claim) closes nothing.
	s.Send(SendRequest{From: "human", To: "worker", Text: "yes", ReplyTo: ask.MID})
	if len(s.Items(false)) != 2 {
		t.Fatal("unverified reply closed an item")
	}
	rep, _ := s.Send(SendRequest{From: "human", To: "worker", Text: "yes", ReplyTo: ask.MID, Verified: "human"})
	items = s.Items(false)
	if len(items) != 1 || items[0].MID != q.MID {
		t.Fatalf("%+v", items)
	}
	all := s.Items(true)
	if len(all) != 2 || all[0].ClosedBy != "answered" || all[0].Answer != rep.MID || all[0].ClosedAt == 0 {
		t.Fatalf("%+v", all[0])
	}
	// A reply to someone else in the thread does not close it.
	s.Send(SendRequest{From: "human", To: "observer", Text: "fyi", ReplyTo: q.MID, Verified: "human"})
	if len(s.Items(false)) != 1 {
		t.Fatal("reply to a third party closed the item")
	}
}

func TestCloseItemDismissAndErrors(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	s.Send(SendRequest{From: "w", To: "human", Text: "?", Kind: KindAsk})
	it := s.Items(false)[0]
	closed, err := s.CloseItem(it.ID, "dismissed", "")
	if err != nil || closed.ClosedBy != "dismissed" {
		t.Fatal(err, closed)
	}
	if _, err := s.CloseItem(it.ID, "dismissed", ""); err != ErrNoItem {
		t.Fatal("double close")
	}
	if _, err := s.CloseItem(99, "x", ""); err != ErrNoItem {
		t.Fatal("unknown")
	}
	if got, ok := s.Item(it.ID); !ok || got.ClosedBy != "dismissed" {
		t.Fatal(got, ok)
	}
}

func TestItemsPersist(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	s, _ := Load(Options{}, db)
	s.Send(SendRequest{From: "w", To: "human", Text: "a?", Kind: KindAsk})
	s.Send(SendRequest{From: "w", To: "human", Text: "b?", Kind: KindAskHuman, Options: []string{"x"}})
	s.CloseItem(1, "dismissed", "")
	db.Close()
	db2, _ := OpenSQLite(filepath.Join(dir, "store.db"))
	defer db2.Close()
	s2, err := Load(Options{}, db2)
	if err != nil {
		t.Fatal(err)
	}
	open := s2.Items(false)
	if len(open) != 1 || open[0].ID != 2 || open[0].Options[0] != "x" {
		t.Fatalf("%+v", open)
	}
	if all := s2.Items(true); len(all) != 2 || all[0].ClosedBy != "dismissed" {
		t.Fatalf("%+v", all)
	}
	s2.Send(SendRequest{From: "w", To: "human", Text: "c?", Kind: KindAsk})
	if items := s2.Items(false); items[len(items)-1].ID != 3 {
		t.Fatalf("item id counter not restored: %+v", items)
	}
}
