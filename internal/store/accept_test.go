package store

import "testing"

// SPEC §13a: a message minted on another machine keeps its mid and thread
// when it lands here, gets a local id, and a second copy of the same mid is
// dropped (at-least-once delivery, mid-deduped).
func TestAccept_KeepsMIDAndThreadAndDedupes(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	own := send(t, s, "human", "worker", "local first")
	in := Message{MID: "s1:7", ThreadID: "s1:5", Kind: KindReply, From: "reviewer@s1", To: "worker",
		Text: "yes", ReplyTo: "s1:5", Verified: "human", Origin: "link", OriginHost: "s1", ReadAt: 99}
	got, fresh, err := s.Accept("worker", in)
	if err != nil || !fresh {
		t.Fatalf("accept: %v fresh=%v", err, fresh)
	}
	if got.MID != "s1:7" || got.ThreadID != "s1:5" || got.ID <= own.ID || got.ReadAt != 0 || got.Verified != "human" {
		t.Fatalf("%+v", got)
	}
	if n, _ := s.Unread("worker"); n != 2 {
		t.Fatalf("unread %d", n)
	}
	again, fresh, err := s.Accept("worker", in)
	if err != nil || fresh || again.ID != got.ID {
		t.Fatalf("duplicate: %+v fresh=%v %v", again, fresh, err)
	}
	if n, _ := s.Unread("worker"); n != 2 {
		t.Fatalf("duplicate counted: %d", n)
	}
	if m, ok := s.Get("s1:7"); !ok || m.ID != got.ID {
		t.Fatalf("get: %+v %v", m, ok)
	}
	if th := s.Thread("s1:5"); len(th) != 1 || th[0].MID != "s1:7" {
		t.Fatalf("thread: %+v", th)
	}
}

// Mids carry the machine's name, so two machines never mint the same one.
func TestMachineIDPrefixesMIDs(t *testing.T) {
	s := New(Options{MachineID: "s2"})
	m, err := s.Send(SendRequest{From: "a", To: "b", Text: "x"})
	if err != nil || m.MID != "s2:1" {
		t.Fatalf("%+v %v", m, err)
	}
}
