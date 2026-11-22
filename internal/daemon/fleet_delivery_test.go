package daemon

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Quieter delivery (SPEC §13a): the inbox machine pushes mail for a spoke
// once, resends all that is unacknowledged on link-hello, and on a
// heartbeat only what has waited unacknowledged past the resend age. The
// spoke acknowledges a burst with one link-ack.

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) tick(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// delivered drains the deliver events already received, by mid.
func delivered(c *client.Client) []string {
	var mids []string
	for {
		select {
		case f := <-c.Events():
			if f.Event != "deliver" {
				continue
			}
			var m store.Message
			json.Unmarshal(f.Data, &m)
			mids = append(mids, m.MID)
		default:
			return mids
		}
	}
}

func TestStar_HeartbeatResendsOnlyStaleUnacked(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	sock, _ := startDaemonWith(t, Options{Name: "s1", Now: clk.now, Store: store.New(store.Options{MachineID: "s1"})})
	l := dial(t, sock)
	if err := l.Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	person := dial(t, sock)
	var sr SendResult
	if err := person.Call("send", SendArgs{From: "reviewer", To: "worker@s2", Text: "hi"}, &sr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first push", func() bool {
		l.Call("ping", nil, nil)
		return len(delivered(l)) == 1
	})
	flush := func() []string {
		t.Helper()
		if err := l.Call("link-flush", nil, nil); err != nil {
			t.Fatal(err)
		}
		return delivered(l)
	}
	if got := flush(); len(got) != 0 {
		t.Fatalf("resent at once on a heartbeat: %v", got)
	}
	clk.tick(59 * time.Second)
	if got := flush(); len(got) != 0 {
		t.Fatalf("resent before 60 s: %v", got)
	}
	clk.tick(2 * time.Second)
	if got := flush(); len(got) != 1 || got[0] != sr.Message.MID {
		t.Fatalf("not resent after 60 s unacked: %v", got)
	}
	if got := flush(); len(got) != 0 {
		t.Fatalf("a resend restarts the wait: %v", got)
	}
	if err := l.Call("link-ack", LinkAckArgs{MIDs: []string{sr.Message.MID}}, nil); err != nil {
		t.Fatal(err)
	}
	clk.tick(2 * time.Minute)
	if got := flush(); len(got) != 0 {
		t.Fatalf("resent after the ack: %v", got)
	}
}

// A burst of deliveries is acknowledged with one link-ack.
func TestStar_SpokeAcksABurstOnce(t *testing.T) {
	s := newStar(t)
	reviewer := dialAs(t, s.hubSock, "reviewer")
	worker := dialAs(t, s.spokeSock, "worker")
	s.cut(true)
	s.waitLink(t, false)
	for i := 0; i < 8; i++ {
		if err := reviewer.Call("send", SendArgs{To: "worker@s2", Text: "burst"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := s.hub.ackCalls.Load()
	s.heal()
	waitUnread(t, worker, 8)
	waitFor(t, "all acknowledged", func() bool {
		n, _ := s.hub.st.Unread("worker@s2")
		return n == 0
	})
	if n := s.hub.ackCalls.Load() - before; n != 1 {
		t.Fatalf("%d link-acks for one burst", n)
	}
}
