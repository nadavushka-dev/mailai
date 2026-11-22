package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// cannotType is an adapter that sees agents but cannot type (the sandbox's
// "none", or a pane that refuses): every Type fails.
type cannotType struct {
	*fakeAdapter
	mu    sync.Mutex
	tries int
}

func (c *cannotType) Type(context.Context, store.Address, string) error {
	c.mu.Lock()
	c.tries++
	c.mu.Unlock()
	return adapter.ErrCannotType
}

// Found in the sandbox: a wake that fails was retried, and logged, on every
// 1 s tick forever. A failed wake waits the same 30 s spacing as one that
// worked.
func TestWake_FailureIsSpacedNotRetriedEverySecond(t *testing.T) {
	ca := &cannotType{fakeAdapter: &fakeAdapter{states: map[store.Address]adapter.State{"worker": adapter.Idle}}}
	clk := &testClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	var logs bytes.Buffer
	d := New(Options{Store: store.New(store.Options{Now: clk.now}), Adapter: ca, Wake: true, Now: clk.now, Log: log.New(&logs, "", 0)})
	ctx := context.Background()
	d.refreshAgents(ctx)
	d.st.Send(store.SendRequest{From: "reviewer", To: "worker", Text: "hi"})
	clk.add(6 * time.Second)
	for i := 0; i < 10; i++ {
		d.wakeOnce(ctx)
		clk.add(time.Second)
	}
	if ca.tries != 1 {
		t.Fatalf("tried to type %d times in 10 s; want 1", ca.tries)
	}
	if n := strings.Count(logs.String(), "wake worker"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, logs.String())
	}
	clk.add(30 * time.Second)
	d.wakeOnce(ctx)
	if ca.tries != 2 {
		t.Fatalf("no retry after the spacing: %d", ca.tries)
	}
}
