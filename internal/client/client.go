// Package client talks to the daemon over its unix socket. The CLI, hooks,
// TUI and MCP server all go through it.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
)

// Client is one connection.
type Client struct {
	c      io.ReadWriteCloser
	r      *proto.Reader
	wmu    sync.Mutex
	mu     sync.Mutex
	nextID uint64
	pend   map[uint64]chan proto.Frame
	events chan proto.Frame
	done   chan struct{}
	err    error
}

// Dial connects to the socket at path. With a token it says hello first, so
// the daemon places the connection as that agent (SPEC §9).
func Dial(path string, token string) (*Client, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("no daemon at %s: %w", path, err)
	}
	return New(c, token)
}

// New speaks the protocol over any stream: a socket, or an ssh session to
// another machine's bridge (SPEC §13a).
func New(c io.ReadWriteCloser, token string) (*Client, error) {
	cl := &Client{c: c, r: proto.NewReader(c), pend: map[uint64]chan proto.Frame{}, events: make(chan proto.Frame, 256), done: make(chan struct{})}
	go cl.readLoop()
	if token != "" {
		if err := cl.Call("hello", map[string]string{"token": token}, nil); err != nil {
			cl.Close()
			return nil, err
		}
	}
	return cl, nil
}

func (cl *Client) readLoop() {
	defer close(cl.done)
	for {
		f, err := cl.r.Next()
		if err != nil {
			cl.mu.Lock()
			cl.err = err
			for id, ch := range cl.pend {
				close(ch)
				delete(cl.pend, id)
			}
			cl.mu.Unlock()
			close(cl.events)
			return
		}
		if f.Event != "" {
			select {
			case cl.events <- *f:
			default:
			}
			continue
		}
		if f.ID == nil {
			continue
		}
		cl.mu.Lock()
		ch := cl.pend[*f.ID]
		delete(cl.pend, *f.ID)
		cl.mu.Unlock()
		if ch != nil {
			ch <- *f
		}
	}
}

// Call sends one request and decodes the result into out (may be nil).
func (cl *Client) Call(verb string, args any, out any) error {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		raw = b
	}
	cl.mu.Lock()
	cl.nextID++
	id := cl.nextID
	ch := make(chan proto.Frame, 1)
	cl.pend[id] = ch
	cl.mu.Unlock()

	cl.wmu.Lock()
	err := proto.Write(cl.c, proto.Request{ID: id, Verb: verb, Args: raw})
	cl.wmu.Unlock()
	if err != nil {
		return err
	}
	f, ok := <-ch
	if !ok {
		cl.mu.Lock()
		defer cl.mu.Unlock()
		return fmt.Errorf("connection closed: %w", cl.err)
	}
	if f.Error != "" {
		return &proto.Error{Code: f.Error, Hint: f.Hint}
	}
	if out != nil && len(f.Res) > 0 {
		return json.Unmarshal(f.Res, out)
	}
	return nil
}

// Events are frames from subscribe; closed when the connection ends.
func (cl *Client) Events() <-chan proto.Frame { return cl.events }

// Close ends the connection.
func (cl *Client) Close() error {
	err := cl.c.Close()
	<-cl.done
	return err
}

// Code extracts the protocol error code from err, or "".
func Code(err error) proto.Code {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}
