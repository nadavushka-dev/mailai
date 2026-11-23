package client

import (
	"errors"
	"io"
	"testing"
	"time"
)

// deadStream ends at once on read but swallows writes, like a stream whose
// far end went away before the write noticed.
type deadStream struct{}

func (deadStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (deadStream) Write(b []byte) (int, error) { return len(b), nil }
func (deadStream) Close() error                { return nil }

// Found when the daemon restarted under the TUI: a call made after the
// connection ended waited forever for an answer that could not come. It
// must fail at once, and say the connection is closed.
func TestCallAfterTheConnectionEndedFailsAtOnce(t *testing.T) {
	cl, err := New(deadStream{}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-cl.done
	got := make(chan error, 1)
	go func() { got <- cl.Call("ping", nil, nil) }()
	select {
	case err := <-got:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("got %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call on an ended connection hung")
	}
}
