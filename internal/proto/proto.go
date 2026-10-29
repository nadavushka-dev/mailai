// Package proto is the JSON-lines protocol between clients and the daemon
// (SPEC §12). One request per line, one response per line, matched by id;
// events from subscribe have no id.
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxLine bounds one frame; a message body is at most 64 KB and the rest is
// small, so this is generous.
const MaxLine = 1 << 20

// Request is what a client sends.
type Request struct {
	ID   uint64          `json:"id"`
	Verb string          `json:"verb"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response answers one request. Exactly one of Result or Error is set.
type Response struct {
	ID     uint64          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  Code            `json:"error,omitempty"`
	Hint   string          `json:"hint,omitempty"`
}

// Event is pushed to a subscribed connection.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Code is the closed set of error codes (SPEC §12).
type Code string

const (
	CodeBadRequest       Code = "bad_request"
	CodeUnknownVerb      Code = "unknown_verb"
	CodeForbidden        Code = "forbidden"
	CodeNotHuman         Code = "not_human"
	CodeMailboxFull      Code = "mailbox_full"
	CodeRateLimited      Code = "rate_limited"
	CodeAskCycle         Code = "ask_cycle"
	CodeAskLimit         Code = "ask_limit"
	CodeUnknownPrincipal Code = "unknown_principal"
	CodeNotFound         Code = "not_found"
	CodeTimeout          Code = "timeout"
	CodeOffline          Code = "offline"
	CodeInternal         Code = "internal"
)

// Error is a protocol error a handler returns; the daemon turns it into a
// Response.
type Error struct {
	Code Code
	Hint string
}

func (e *Error) Error() string {
	if e.Hint == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Hint)
}

// Errorf makes an Error.
func Errorf(c Code, format string, a ...any) *Error {
	return &Error{Code: c, Hint: fmt.Sprintf(format, a...)}
}

// Frame is any line on the wire, decoded far enough to tell what it is.
type Frame struct {
	ID    *uint64         `json:"id,omitempty"`
	Verb  string          `json:"verb,omitempty"`
	Args  json.RawMessage `json:"args,omitempty"`
	OK    *bool           `json:"ok,omitempty"`
	Res   json.RawMessage `json:"result,omitempty"`
	Error Code            `json:"error,omitempty"`
	Hint  string          `json:"hint,omitempty"`
	Event string          `json:"event,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Reader reads frames.
type Reader struct{ sc *bufio.Scanner }

// NewReader wraps r.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	return &Reader{sc: sc}
}

var ErrLineTooLong = errors.New("line too long")

// Next returns the next frame, io.EOF at a clean end.
func (r *Reader) Next() (*Frame, error) {
	for r.sc.Scan() {
		line := r.sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			return nil, fmt.Errorf("bad frame: %w", err)
		}
		return &f, nil
	}
	if err := r.sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, ErrLineTooLong
		}
		return nil, err
	}
	return nil, io.EOF
}

// Write encodes v as one line.
func Write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
