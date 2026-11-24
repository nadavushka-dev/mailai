package tui

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// The daemon's log line for a delete says it came from the TUI.
func TestLiveDeleteSaysTUIInTheDaemonLog(t *testing.T) {
	sock := shortSock(t)
	logs := &lockedBuf{}
	st := store.New(store.Options{})
	d := daemon.New(daemon.Options{Store: st, Log: log.New(logs, "", 0)})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	dial := func() (*client.Client, error) { return client.Dial(sock, "") }
	c, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	l := NewLive(c, dial)
	t.Cleanup(func() { l.Close() })
	m, err := st.Send(store.SendRequest{From: "worker", To: "human", Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.DeleteThread(m.ThreadID); err != nil {
		t.Fatal(err)
	}
	if want := "deleted 1 message in 1 thread by human via tui: " + m.ThreadID; !strings.Contains(logs.String(), want) {
		t.Fatalf("log:\n%s\nwant %q", logs.String(), want)
	}
}
