package store

import (
	"errors"
	"testing"
)

// SPEC §6: thread_id is resolved at send time and is the mid of the thread
// starter; a reply carries the parent's thread_id.
func TestThread_ResolvedAtSendTime(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "sup", "worker", "do X")
	if root.ThreadID != root.MID {
		t.Fatalf("starter must carry its own mid as thread: %+v", root)
	}
	r1, err := s.Send(SendRequest{From: "worker", To: "sup", Text: "done", ReplyTo: root.MID})
	if err != nil {
		t.Fatal(err)
	}
	if r1.ThreadID != root.MID || r1.Kind != KindReply {
		t.Fatalf("reply: %+v", r1)
	}
	r2, _ := s.Send(SendRequest{From: "sup", To: "worker", Text: "thanks", ReplyTo: r1.MID})
	if r2.ThreadID != root.MID {
		t.Fatalf("reply to a reply must keep the root: %+v", r2)
	}
}

// A reply to a message the store no longer holds is still a reply: it roots
// at the parent's id and says the parent was missing.
func TestThread_ReplyToMissingParent(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	r, err := s.Send(SendRequest{From: "a", To: "b", Text: "re", ReplyTo: "local:999"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.ReplyToMissing || r.ThreadID != "local:999" {
		t.Fatalf("%+v", r)
	}
}

// Thread lookup finds a message wherever it is stored, not only in the
// recipient's box: the parent of a reply usually sits in another mailbox.
func TestThread_ReadByThreadAcrossMailboxes(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "sup", "worker", "do X")
	s.Send(SendRequest{From: "worker", To: "sup", Text: "done", ReplyTo: root.MID})
	send(t, s, "sup", "worker", "unrelated")
	got := s.Thread(root.MID)
	if len(got) != 2 || got[0].MID != root.MID || got[1].ReplyTo != root.MID {
		t.Fatalf("thread: %+v", got)
	}
	if s.Thread("local:404") != nil {
		t.Fatal("unknown thread must be nil")
	}
}

// SPEC §6: seen sits between unread and read. A peek by the recipient of its
// own inbox marks seen; a peek of another's inbox marks nothing.
func TestSeen_PeekMarksOwnInboxSeenOnly(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	m := send(t, s, "a", "worker", "hi")
	got, err := s.Peek(PeekQuery{Mailbox: "worker", By: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].WasUnread == false || got[0].WasSeen {
		t.Fatalf("first peek: %+v", got[0])
	}
	// Stored state: seen, still unread.
	after, _ := s.Read(ReadQuery{Mailbox: "worker"})
	if after[0].SeenAt != c.t.UnixNano() || after[0].ReadAt != 0 {
		t.Fatalf("after own peek: seen=%d read=%d", after[0].SeenAt, after[0].ReadAt)
	}
	if after[0].WasSeen != true || after[0].WasUnread != true {
		t.Fatalf("read copy must say it was seen and unread: %+v", after[0])
	}
	// Someone else's peek changes nothing.
	m2 := send(t, s, "a", "other", "yo")
	s.Peek(PeekQuery{Mailbox: "other", By: "worker"})
	o, _ := s.Read(ReadQuery{Mailbox: "other"})
	if o[0].SeenAt != 0 {
		t.Fatalf("foreign peek must not mark seen: %+v", o[0])
	}
	_ = m
	_ = m2
}

func TestSeen_MarkingReadLeavesSeen(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	send(t, s, "a", "worker", "hi")
	s.Peek(PeekQuery{Mailbox: "worker", By: "worker"})
	seenAt := c.t.UnixNano()
	c.tick(1)
	s.Read(ReadQuery{Mailbox: "worker", Mark: true})
	got, _ := s.Read(ReadQuery{Mailbox: "worker"})
	if got[0].SeenAt != seenAt || got[0].ReadAt == 0 {
		t.Fatalf("%+v", got[0])
	}
}

func TestUnread_CountAndSenders(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	send(t, s, "a", "worker", "1")
	send(t, s, "b", "worker", "2")
	send(t, s, "a", "worker", "3")
	n, from := s.Unread("worker")
	if n != 3 || len(from) != 2 || from[0] != "a" || from[1] != "b" {
		t.Fatalf("n=%d from=%v", n, from)
	}
	s.Read(ReadQuery{Mailbox: "worker", Mark: true})
	if n, _ := s.Unread("worker"); n != 0 {
		t.Fatalf("n=%d", n)
	}
	if n, _ := s.Unread("nobody"); n != 0 {
		t.Fatalf("n=%d", n)
	}
}

func TestSend_ReplyToSelfKindIsReplyEvenWhenKindGiven(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	root := send(t, s, "a", "b", "q")
	_, err := s.Send(SendRequest{From: "b", To: "a", Text: "x", ReplyTo: root.MID, Kind: KindNotice})
	if !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("a reply cannot also be a notice: %v", err)
	}
}
