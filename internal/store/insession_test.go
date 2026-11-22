package store

import (
	"path/filepath"
	"testing"
	"time"
)

// The person sometimes answers in the agent's own session. A likely sign
// (the agent got a new prompt) marks the agent's open asks "maybe answered
// in session" without closing them; approvals and other agents' items are
// untouched; the mark is stamped once.
func TestInSession_MaybeMarksOnlyTheAgentsOpenAsks(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	s.Send(SendRequest{From: "worker", To: "human", Text: "merge?", Kind: KindAsk})
	s.Send(SendRequest{From: "worker", To: "human", Text: "region?", Kind: KindAskHuman, Options: []string{"us", "eu"}})
	s.Send(SendRequest{From: "other", To: "human", Text: "mine?", Kind: KindAskHuman})
	ap, _ := s.OpenApproval(Item{Kind: ItemApproval, From: "worker", Subject: "Bash"})

	c.tick(time.Minute)
	marked := s.MarkMaybeInSession("worker")
	if len(marked) != 2 {
		t.Fatalf("%+v", marked)
	}
	at := c.now().UnixNano()
	for _, it := range s.Items(false) {
		switch {
		case it.From == "worker" && it.Kind != ItemApproval:
			if it.MaybeInSessionAt != at || it.ClosedAt != 0 {
				t.Fatalf("%+v", it)
			}
		default:
			if it.MaybeInSessionAt != 0 {
				t.Fatalf("marked another item: %+v", it)
			}
		}
	}
	// Once: a second prompt does not move the stamp or report again.
	c.tick(time.Minute)
	if again := s.MarkMaybeInSession("worker"); len(again) != 0 {
		t.Fatalf("%+v", again)
	}
	if it, _ := s.Item(1); it.MaybeInSessionAt != at {
		t.Fatalf("%+v", it)
	}
	if it, _ := s.Item(ap.ID); it.MaybeInSessionAt != 0 {
		t.Fatalf("%+v", it)
	}
}

// Recently closed lists what closed since a time, newest first, with how;
// who decided is kept on the item, not only on the answer.
func TestInSession_RecentlyClosedNewestFirstWithWho(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	a, _ := s.Send(SendRequest{From: "w1", To: "human", Text: "a?", Kind: KindAsk})
	s.Send(SendRequest{From: "w2", To: "human", Text: "b?", Kind: KindAskHuman})
	s.Send(SendRequest{From: "w3", To: "human", Text: "c?", Kind: KindAskHuman})
	s.Send(SendRequest{From: "w4", To: "human", Text: "d?", Kind: KindAskHuman})

	s.CloseItemBy(4, "dismissed", "", "human")
	c.tick(2 * time.Hour)
	since := c.now().UnixNano()
	c.tick(time.Minute)
	s.Send(SendRequest{From: "sup", To: "w1", Text: "yes", ReplyTo: a.MID, Verified: "supervisor:sup"})
	c.tick(time.Minute)
	s.CloseItem(2, ClosedInSession, "")
	c.tick(time.Minute)
	s.CloseItemBy(3, "dismissed", "", "human")

	got := s.RecentlyClosed(since)
	if len(got) != 3 || got[0].ID != 3 || got[1].ID != 2 || got[2].ID != 1 {
		t.Fatalf("%+v", got)
	}
	if got[0].DecidedBy != "human" || got[1].ClosedBy != "in_session" || got[2].ClosedBy != "answered" || got[2].DecidedBy != "supervisor:sup" {
		t.Fatalf("%+v", got)
	}
	// By thread: every item of it, open or closed.
	if th := s.ItemsInThread(a.MID); len(th) != 1 || th[0].ID != 1 {
		t.Fatalf("%+v", th)
	}
}

func TestInSession_MarkAndDecidedByPersist(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	s, _ := Load(Options{}, db)
	s.Send(SendRequest{From: "w", To: "human", Text: "a?", Kind: KindAsk})
	s.Send(SendRequest{From: "w", To: "human", Text: "b?", Kind: KindAsk})
	s.MarkMaybeInSession("w")
	s.CloseItemBy(2, "dismissed", "", "supervisor:sup")
	db.Close()
	db2, _ := OpenSQLite(filepath.Join(dir, "store.db"))
	defer db2.Close()
	s2, err := Load(Options{}, db2)
	if err != nil {
		t.Fatal(err)
	}
	if it, _ := s2.Item(1); it.MaybeInSessionAt == 0 {
		t.Fatalf("%+v", it)
	}
	if it, _ := s2.Item(2); it.DecidedBy != "supervisor:sup" {
		t.Fatalf("%+v", it)
	}
}
