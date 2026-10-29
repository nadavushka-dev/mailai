// Package tui is the terminal Inbox (D13, SPEC §12): two views, Inbox and
// Mail, over the daemon's verbs. Everything the TUI does to the world goes
// through Backend, so tests drive the model with a fake.
package tui

import (
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Backend is what the TUI needs from the daemon.
type Backend interface {
	Whoami() (daemon.WhoamiResult, error)
	Inbox(all bool) ([]store.Item, error)
	Threads() ([]daemon.ThreadRow, error)
	Thread(id string) ([]store.Message, error)
	Answer(item uint64, text, message string) error
	Dismiss(item uint64) error
	Reply(mid string, to store.Address, text string) error
	// Events delivers daemon events; nil when not subscribed.
	Events() <-chan proto.Frame
}

// Live is the Backend over a client connection.
type Live struct{ C *client.Client }

func (l Live) Whoami() (daemon.WhoamiResult, error) {
	var w daemon.WhoamiResult
	err := l.C.Call("whoami", nil, &w)
	return w, err
}

func (l Live) Inbox(all bool) ([]store.Item, error) {
	var r daemon.InboxResult
	err := l.C.Call("inbox", daemon.InboxArgs{All: all}, &r)
	return r.Items, err
}

func (l Live) Threads() ([]daemon.ThreadRow, error) {
	var r daemon.ThreadsResult
	err := l.C.Call("threads", nil, &r)
	return r.Threads, err
}

func (l Live) Thread(id string) ([]store.Message, error) {
	var r daemon.ReadResult
	err := l.C.Call("read", daemon.ReadArgs{Thread: id}, &r)
	return r.Messages, err
}

func (l Live) Answer(item uint64, text, message string) error {
	return l.C.Call("answer", daemon.AnswerArgs{Item: item, Text: text, Message: message}, nil)
}

func (l Live) Dismiss(item uint64) error {
	return l.C.Call("dismiss", daemon.DismissArgs{Item: item}, nil)
}

func (l Live) Reply(mid string, to store.Address, text string) error {
	return l.C.Call("send", daemon.SendArgs{To: to, Text: text, ReplyTo: mid}, nil)
}

func (l Live) Events() <-chan proto.Frame {
	if err := l.C.Call("subscribe", nil, nil); err != nil {
		return nil
	}
	return l.C.Events()
}
