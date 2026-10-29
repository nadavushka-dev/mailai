package proto

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, Request{ID: 1, Verb: "send", Args: json.RawMessage(`{"to":"human"}`)})
	Write(&buf, Response{ID: 1, OK: true, Result: json.RawMessage(`{"id":7}`)})
	Write(&buf, Response{ID: 2, Error: CodeForbidden, Hint: "nope"})
	Write(&buf, Event{Event: "mail", Data: json.RawMessage(`{}`)})
	r := NewReader(&buf)
	f, _ := r.Next()
	if f.Verb != "send" || *f.ID != 1 {
		t.Fatalf("%+v", f)
	}
	f, _ = r.Next()
	if !*f.OK || string(f.Res) != `{"id":7}` {
		t.Fatalf("%+v", f)
	}
	f, _ = r.Next()
	if f.Error != CodeForbidden || f.Hint != "nope" {
		t.Fatalf("%+v", f)
	}
	f, _ = r.Next()
	if f.Event != "mail" || f.ID != nil {
		t.Fatalf("%+v", f)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestBlankLinesSkippedAndBadJSONIsError(t *testing.T) {
	r := NewReader(strings.NewReader("\n\n{\"id\":3,\"verb\":\"ping\"}\nnot json\n"))
	f, err := r.Next()
	if err != nil || f.Verb != "ping" {
		t.Fatal(err)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("bad json must error")
	}
}

func TestLineTooLong(t *testing.T) {
	r := NewReader(strings.NewReader(strings.Repeat("x", MaxLine+1) + "\n"))
	if _, err := r.Next(); err != ErrLineTooLong {
		t.Fatalf("got %v", err)
	}
}
