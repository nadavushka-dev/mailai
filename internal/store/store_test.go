package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// clock is a test clock the store reads through Options.Now.
type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) tick(d time.Duration) { c.t = c.t.Add(d) }

func newTestStore(t *testing.T, lim Limits) (*Store, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	s := New(Options{Now: c.now, Limits: lim})
	return s, c
}

func send(t *testing.T, s *Store, from, to, text string) Message {
	t.Helper()
	m, err := s.Send(SendRequest{From: Address(from), To: Address(to), Text: text})
	if err != nil {
		t.Fatalf("send %s->%s: %v", from, to, err)
	}
	return m
}

func TestSend_AssignsIDsAndTimestamps(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	a := send(t, s, "worker", "human", "hello")
	c.tick(time.Second)
	b := send(t, s, "worker", "human", "again")
	if a.ID == 0 || b.ID != a.ID+1 {
		t.Fatalf("ids: %d %d", a.ID, b.ID)
	}
	if a.SentAt != c.t.Add(-time.Second).UnixNano() || b.SentAt != c.t.UnixNano() {
		t.Fatalf("sent_at: %d %d", a.SentAt, b.SentAt)
	}
	if a.Kind != KindMail {
		t.Fatalf("kind: %q", a.Kind)
	}
	if a.ReadAt != 0 || a.SeenAt != 0 {
		t.Fatalf("new message must be unread and unseen")
	}
}

func TestSend_MessageIsImmutable(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	m := send(t, s, "worker", "human", "hello")
	m.Text = "tampered"
	got, err := s.Read(ReadQuery{Mailbox: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Text != "hello" {
		t.Fatalf("store handed out an alias of its record")
	}
}

func TestSend_SubjectAndTextBounds(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	_, err := s.Send(SendRequest{From: "a", To: "b", Subject: strings.Repeat("x", MaxSubject+1), Text: "t"})
	if !errors.Is(err, ErrSubjectTooLong) {
		t.Fatalf("want ErrSubjectTooLong, got %v", err)
	}
	_, err = s.Send(SendRequest{From: "a", To: "b", Text: strings.Repeat("x", MaxText+1)})
	if !errors.Is(err, ErrTextTooLong) {
		t.Fatalf("want ErrTextTooLong, got %v", err)
	}
	_, err = s.Send(SendRequest{From: "a", To: "b", Subject: "two\nlines", Text: "t"})
	if !errors.Is(err, ErrSubjectNotOneLine) {
		t.Fatalf("want ErrSubjectNotOneLine, got %v", err)
	}
}

func TestSend_RequiresFromAndTextOrNotice(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	if _, err := s.Send(SendRequest{To: "b", Text: "t"}); !errors.Is(err, ErrNoSender) {
		t.Fatalf("want ErrNoSender, got %v", err)
	}
	if _, err := s.Send(SendRequest{From: "a", To: "b"}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("want ErrEmptyText, got %v", err)
	}
	// A notice has no recipient and goes to its session's notice box.
	n, err := s.Send(SendRequest{From: "a", Session: "api", Text: "deploy started"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != KindNotice || n.To != "" {
		t.Fatalf("notice: %+v", n)
	}
	got, _ := s.Read(ReadQuery{Mailbox: NoticeBox("api")})
	if len(got) != 1 || got[0].ID != n.ID {
		t.Fatalf("notice not in session box: %+v", got)
	}
	if _, err := s.Send(SendRequest{From: "a", Text: "nowhere"}); !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("notice without session: want ErrNoRecipient, got %v", err)
	}
}

func TestRead_OrdersOldestFirstAndMarksRead(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	a := send(t, s, "w", "human", "1")
	b := send(t, s, "w", "human", "2")
	c.tick(time.Minute)
	got, err := s.Read(ReadQuery{Mailbox: "human", Mark: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("order: %+v", got)
	}
	for _, m := range got {
		if !m.WasUnread {
			t.Fatalf("first read must report was_unread")
		}
		if m.ReadAt != c.t.UnixNano() {
			t.Fatalf("read_at not stamped: %+v", m)
		}
	}
	again, _ := s.Read(ReadQuery{Mailbox: "human", Mark: true})
	if again[0].WasUnread {
		t.Fatalf("second read must not report was_unread")
	}
	unread, _ := s.Read(ReadQuery{Mailbox: "human", Unread: true})
	if len(unread) != 0 {
		t.Fatalf("nothing should be unread, got %d", len(unread))
	}
}

// D-rule §6: eviction removes the oldest read messages first and never an
// unread one; a mailbox whose unread mail alone fills it refuses new mail.
func TestEviction_ReadFirstUnreadNeverByEvictionAlone(t *testing.T) {
	s, _ := newTestStore(t, Limits{MaxMessages: 3, MaxBytes: 1 << 20})
	a := send(t, s, "w", "human", "a")
	b := send(t, s, "w", "human", "b")
	c := send(t, s, "w", "human", "c")
	// Read only b. Sending d must evict b (read), not a (unread, older).
	if _, err := s.Read(ReadQuery{Mailbox: "human", IDs: []uint64{b.ID}, Mark: true}); err != nil {
		t.Fatal(err)
	}
	d := send(t, s, "w", "human", "d")
	got, _ := s.Read(ReadQuery{Mailbox: "human"})
	ids := idsOf(got)
	if len(ids) != 3 || ids[0] != a.ID || ids[1] != c.ID || ids[2] != d.ID {
		t.Fatalf("after evicting the read one: %v", ids)
	}
	// Now everything is unread and full: the next send is refused.
	_, err := s.Send(SendRequest{From: "w", To: "human", Text: "e"})
	if !errors.Is(err, ErrMailboxFull) {
		t.Fatalf("want ErrMailboxFull, got %v", err)
	}
	if s.Evicted("human") != 1 {
		t.Fatalf("evicted count: %d", s.Evicted("human"))
	}
}

func TestEviction_ByBytes(t *testing.T) {
	s, _ := newTestStore(t, Limits{MaxMessages: 100, MaxBytes: 40})
	a := send(t, s, "w", "human", strings.Repeat("x", 15))
	s.Read(ReadQuery{Mailbox: "human", Mark: true})
	b := send(t, s, "w", "human", strings.Repeat("y", 15))
	s.Read(ReadQuery{Mailbox: "human", Mark: true})
	c := send(t, s, "w", "human", strings.Repeat("z", 15)) // 45 > 40: a goes
	got, _ := s.Read(ReadQuery{Mailbox: "human"})
	ids := idsOf(got)
	if len(ids) != 2 || ids[0] != b.ID || ids[1] != c.ID {
		t.Fatalf("want [%d %d] got %v (a=%d)", b.ID, c.ID, ids, a.ID)
	}
	// One message bigger than the whole box is refused, not stored.
	_, err := s.Send(SendRequest{From: "w", To: "human", Text: strings.Repeat("q", 41)})
	if !errors.Is(err, ErrMailboxFull) {
		t.Fatalf("oversized: want ErrMailboxFull, got %v", err)
	}
}

func TestRead_UnknownMailboxIsEmptyNotError(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	got, err := s.Read(ReadQuery{Mailbox: "nobody"})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func idsOf(ms []Message) []uint64 {
	out := make([]uint64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
