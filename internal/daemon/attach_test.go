package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/stash"
	"github.com/nadavushka-dev/mailai/internal/store"
)

func startWithStash(t *testing.T) (string, *stash.Stash) {
	t.Helper()
	sock := shortSock(t)
	st := stash.New(filepath.Join(t.TempDir(), "stash"))
	d := New(Options{Store: store.New(store.Options{}), Stash: st})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sock, st
}

// SPEC §6: attachments are references. The daemon fills size and type at
// send, and Missing is resolved at read, never trusted from the request.
func TestAttachments_ValidatedAtSendResolvedAtRead(t *testing.T) {
	sock, _ := startWithStash(t)
	c := dial(t, sock)
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	os.WriteFile(img, []byte("pngpng"), 0o644)
	var sr SendResult
	err := c.Call("send", SendArgs{From: "a", To: "b", Text: "see", Attachments: []store.Attachment{
		{Path: img, Bytes: 999, Missing: true, Stashed: true, Kind: "bogus"},
	}}, &sr)
	if err != nil {
		t.Fatal(err)
	}
	at := sr.Message.Attachments[0]
	if at.Kind != "image" || at.MediaType != "image/png" || at.Bytes != 6 || at.Missing || at.Stashed {
		t.Fatalf("client claims must be replaced: %+v", at)
	}
	var rr ReadResult
	c.Call("read", ReadArgs{Mailbox: "b", Peek: true}, &rr)
	if rr.Messages[0].Attachments[0].Missing {
		t.Fatal("present file marked missing")
	}
	os.Remove(img)
	c.Call("read", ReadArgs{Mailbox: "b", Peek: true}, &rr)
	if !rr.Messages[0].Attachments[0].Missing {
		t.Fatal("removed file not marked missing at read")
	}
	// Refusals.
	for _, bad := range []store.Attachment{{Path: "rel.txt"}, {Path: dir}, {Path: filepath.Join(dir, "nope")}} {
		err := c.Call("send", SendArgs{From: "a", To: "b", Text: "x", Attachments: []store.Attachment{bad}}, nil)
		if client.Code(err) != proto.CodeBadRequest {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	many := make([]store.Attachment, MaxAttachments+1)
	for i := range many {
		many[i] = store.Attachment{Path: img}
	}
	if err := c.Call("send", SendArgs{From: "a", To: "b", Text: "x", Attachments: many}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("too many: %v", err)
	}
}

func TestStash_VerbsAndStashedAttachmentsSurviveTheSource(t *testing.T) {
	sock, st := startWithStash(t)
	c := dial(t, sock)
	src := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(src, []byte("plan"), 0o644)
	var pr StashPutResult
	if err := c.Call("stash-put", StashPutArgs{Path: src}, &pr); err != nil {
		t.Fatal(err)
	}
	if !st.Owns(pr.Stored) {
		t.Fatal(pr.Stored)
	}
	var sr SendResult
	c.Call("send", SendArgs{From: "a", To: "b", Text: "plan attached", Attachments: []store.Attachment{{Path: pr.Stored}}}, &sr)
	if !sr.Message.Attachments[0].Stashed {
		t.Fatalf("%+v", sr.Message.Attachments[0])
	}
	os.Remove(src)
	var rr ReadResult
	c.Call("read", ReadArgs{Mailbox: "b"}, &rr)
	if rr.Messages[0].Attachments[0].Missing {
		t.Fatal("stashed copy must outlive the source")
	}
	var list struct{ Files []stash.Entry }
	c.Call("stash-list", StashListArgs{}, &list)
	if len(list.Files) != 1 || list.Files[0].Name != "plan.md" {
		t.Fatalf("%+v", list)
	}
	dest := filepath.Join(t.TempDir(), "copy.md")
	if err := c.Call("stash-get", StashGetArgs{Stored: pr.Stored, Dest: dest}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "plan" {
		t.Fatal(string(b))
	}
	err := c.Call("stash-get", StashGetArgs{Stored: "/etc/hosts", Dest: dest}, nil)
	if client.Code(err) != proto.CodeNotFound {
		t.Fatalf("get outside the stash: %v", err)
	}
	err = c.Call("stash-put", StashPutArgs{Path: src}, nil)
	if client.Code(err) != proto.CodeBadRequest || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("%v", err)
	}
}

func TestStash_DisabledWhenNoDirectory(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	c := dial(t, sock)
	if err := c.Call("stash-list", nil, nil); client.Code(err) != proto.CodeInternal {
		t.Fatal(err)
	}
}
