package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// SPEC §6: a token bucket per claimed sender, 10 burst, 1/s refill; senders
// from another machine past the bucket cap share one overflow bucket.
func TestRateLimit_PerSenderBurstAndRefill(t *testing.T) {
	s, c := newTestStore(t, DefaultLimits)
	for i := 0; i < RateBurst; i++ {
		send(t, s, "spammer", "human", fmt.Sprint(i))
	}
	_, err := s.Send(SendRequest{From: "spammer", To: "human", Text: "one too many"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	// Another sender is unaffected.
	send(t, s, "polite", "human", "hi")
	// One second later one token is back.
	c.tick(time.Second)
	send(t, s, "spammer", "human", "ok now")
	_, err = s.Send(SendRequest{From: "spammer", To: "human", Text: "no"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited after using the refilled token, got %v", err)
	}
}

func TestRateLimit_RefusedSendStoresNothingAndKeepsIDs(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	var last Message
	for i := 0; i < RateBurst; i++ {
		last = send(t, s, "a", "b", "x")
	}
	s.Send(SendRequest{From: "a", To: "b", Text: "refused"})
	got, _ := s.Read(ReadQuery{Mailbox: "b"})
	if len(got) != RateBurst {
		t.Fatalf("stored %d", len(got))
	}
	next := send(t, s, "c", "b", "y")
	if next.ID != last.ID+1 {
		t.Fatalf("a refused send must not burn an id: %d after %d", next.ID, last.ID)
	}
}

// Past MaxBuckets distinct remote senders, the rest share one bucket, so a
// peer cannot dodge the limit by inventing names.
func TestRateLimit_RemoteSendersOverflowIntoSharedBucket(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits)
	for i := 0; i < MaxBuckets; i++ {
		send(t, s, fmt.Sprintf("local%d", i), "human", "x")
	}
	// Local senders are never pushed into overflow.
	send(t, s, "local-extra", "human", "fine")
	// Remote invented names share the overflow bucket.
	for i := 0; i < RateBurst; i++ {
		if _, err := s.Send(SendRequest{From: Address(fmt.Sprintf("r%d@peer", i)), To: "human", Text: "x", Remote: true}); err != nil {
			t.Fatalf("remote %d: %v", i, err)
		}
	}
	_, err := s.Send(SendRequest{From: "r-new@peer", To: "human", Text: "x", Remote: true})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("overflow bucket should be empty, got %v", err)
	}
}
