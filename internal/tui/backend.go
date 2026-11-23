// Package tui is the terminal Inbox (D13, SPEC §12): two views, Inbox and
// Mail, over the daemon's verbs. Everything the TUI does to the world goes
// through Backend, so tests drive the model with a fake.
package tui

import (
	"errors"
	"sync"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Backend is what the TUI needs from the daemon.
type Backend interface {
	Whoami() (daemon.WhoamiResult, error)
	Inbox(all bool) ([]store.Item, error)
	// Recent is what closed in the last hour, newest first (SPEC §7a).
	Recent() ([]store.Item, error)
	// ThreadItems are the items opened in a thread, open or closed.
	ThreadItems(id string) ([]store.Item, error)
	Threads() ([]daemon.ThreadRow, error)
	Thread(id string) ([]store.Message, error)
	Answer(item uint64, text, message string) error
	Dismiss(item uint64) error
	Reply(mid string, to store.Address, text string) error
	// MarkRead marks these of the person's own messages read.
	MarkRead(ids []uint64) error
	// Events delivers daemon events and is closed when the connection
	// ends; nil when there is no connection to listen on.
	Events() <-chan proto.Frame
	// Reconnect connects to the daemon again after the connection ended.
	Reconnect() error
}

// ErrNotConnected is what every call says while the daemon is away.
var ErrNotConnected = errors.New("not connected to the daemon")

// Live is the Backend over a client connection, which it dials again
// when the daemon restarts.
type Live struct {
	dial func() (*client.Client, error)
	mu   sync.Mutex
	c    *client.Client
}

// NewLive serves the backend over c; Reconnect replaces it with dial().
func NewLive(c *client.Client, dial func() (*client.Client, error)) *Live {
	return &Live{c: c, dial: dial}
}

func (l *Live) conn() *client.Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c
}

func (l *Live) call(verb string, args, out any) error {
	c := l.conn()
	if c == nil {
		return ErrNotConnected
	}
	err := c.Call(verb, args, out)
	if errors.Is(err, client.ErrClosed) {
		return ErrNotConnected
	}
	return err
}

// Reconnect dials again and, on success, drops the old connection.
func (l *Live) Reconnect() error {
	if l.dial == nil {
		return ErrNotConnected
	}
	c, err := l.dial()
	if err != nil {
		return err
	}
	l.mu.Lock()
	old := l.c
	l.c = c
	l.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

// Close ends the current connection.
func (l *Live) Close() error {
	l.mu.Lock()
	c := l.c
	l.c = nil
	l.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}

func (l *Live) Whoami() (daemon.WhoamiResult, error) {
	var w daemon.WhoamiResult
	err := l.call("whoami", nil, &w)
	return w, err
}

func (l *Live) Inbox(all bool) ([]store.Item, error) {
	var r daemon.InboxResult
	err := l.call("inbox", daemon.InboxArgs{All: all}, &r)
	return r.Items, err
}

func (l *Live) Recent() ([]store.Item, error) {
	var r daemon.InboxResult
	err := l.call("inbox", daemon.InboxArgs{Closed: true}, &r)
	return r.Items, err
}

func (l *Live) ThreadItems(id string) ([]store.Item, error) {
	var r daemon.InboxResult
	err := l.call("inbox", daemon.InboxArgs{Thread: id}, &r)
	return r.Items, err
}

func (l *Live) Threads() ([]daemon.ThreadRow, error) {
	var r daemon.ThreadsResult
	err := l.call("threads", nil, &r)
	return r.Threads, err
}

func (l *Live) Thread(id string) ([]store.Message, error) {
	var r daemon.ReadResult
	err := l.call("read", daemon.ReadArgs{Thread: id}, &r)
	return r.Messages, err
}

func (l *Live) Answer(item uint64, text, message string) error {
	return l.call("answer", daemon.AnswerArgs{Item: item, Text: text, Message: message}, nil)
}

func (l *Live) Dismiss(item uint64) error {
	return l.call("dismiss", daemon.DismissArgs{Item: item}, nil)
}

func (l *Live) Reply(mid string, to store.Address, text string) error {
	return l.call("send", daemon.SendArgs{To: to, Text: text, ReplyTo: mid}, nil)
}

func (l *Live) MarkRead(ids []uint64) error {
	if len(ids) == 0 {
		return nil // no ids would read the whole mailbox
	}
	return l.call("read", daemon.ReadArgs{Mailbox: store.Human, IDs: ids}, nil)
}

// Events subscribes. A failed subscribe still hands back the stream: it
// closes when the connection ends, which is how the TUI learns of it.
func (l *Live) Events() <-chan proto.Frame {
	c := l.conn()
	if c == nil {
		return nil
	}
	c.Call("subscribe", nil, nil)
	return c.Events()
}
