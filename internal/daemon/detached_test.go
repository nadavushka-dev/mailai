package daemon

import (
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// §9: a detached caller (outside every pane, no terminal) may send under a
// claimed name, unverified, but is never the person: it cannot be human,
// answer the Inbox, mint tokens, grant, or open the web page. It may carry
// a link (the ssh bridge has no terminal).
func TestDetachedCallerIsNeverThePerson(t *testing.T) {
	sock, _ := startDaemonWith(t, Options{Placer: &fakePlacer{byToken: map[string]Placement{
		"detached": {Proof: store.ProofDetached, PID: 42},
	}}})
	person := dial(t, sock)
	if err := person.Call("ask-human", AskHumanArgs{From: "worker", Text: "ok?", NoWait: true}, nil); err != nil {
		t.Fatal(err)
	}
	c, err := client.Dial(sock, "detached")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var who WhoamiResult
	c.Call("whoami", nil, &who)
	if who.Proof != store.ProofDetached {
		t.Fatalf("%+v", who)
	}
	if err := c.Call("send", SendArgs{To: "worker", Text: "hi"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("send with no --from: %v", err)
	}
	if err := c.Call("send", SendArgs{From: "human", To: "worker", Text: "yes"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("send as human: %v", err)
	}
	var sr SendResult
	if err := c.Call("send", SendArgs{From: "nightly", To: "worker", Text: "build red"}, &sr); err != nil {
		t.Fatal(err)
	}
	if sr.Message.From != "nightly" || sr.Message.Verified != "" || sr.Message.SenderProof != store.ProofDetached {
		t.Fatalf("%+v", sr.Message)
	}
	var inbox InboxResult
	person.Call("inbox", InboxArgs{}, &inbox)
	if len(inbox.Items) != 1 {
		t.Fatalf("%+v", inbox)
	}
	for _, v := range []struct {
		verb string
		args any
	}{
		{"answer", AnswerArgs{Item: inbox.Items[0].ID, Text: "yes"}},
		{"token-new", TokenNewArgs{Name: "x"}},
		{"grant", GrantArgs{Principal: "worker", Rights: "read", Scope: "all"}},
		{"web-start", WebStartArgs{}},
	} {
		if err := c.Call(v.verb, v.args, nil); err == nil {
			t.Fatalf("%s from a detached caller succeeded", v.verb)
		}
	}
	c.Call("inbox", InboxArgs{}, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("detached caller listed the person's Inbox: %+v", inbox.Items)
	}
	// It has no mailbox of its own, so it never reads, marks or announces
	// the person's mail by default.
	if err := person.Call("send", SendArgs{From: "worker", To: "human", Text: "for you"}, nil); err != nil {
		t.Fatal(err)
	}
	if c.Call("whoami", nil, &who); who.Principal != "" {
		t.Fatalf("detached caller is %q", who.Principal)
	}
	for _, verb := range []string{"read", "unread", "notice"} {
		if err := c.Call(verb, struct{}{}, nil); client.Code(err) != proto.CodeForbidden {
			t.Fatalf("%s with no mailbox: %v", verb, err)
		}
	}
	var rr ReadResult
	if err := c.Call("read", ReadArgs{Mailbox: "human"}, &rr); err != nil || len(rr.Messages) != 2 || !rr.PeekForced {
		t.Fatalf("%+v %v", rr, err)
	}
	c.Call("read", ReadArgs{Mailbox: "human", Unread: true}, &rr)
	if len(rr.Messages) != 2 || rr.Messages[0].SeenAt != 0 || rr.Messages[1].SeenAt != 0 {
		t.Fatalf("a detached read touched the person's mail: %+v", rr.Messages)
	}
	var nr NoticeResult
	c.Call("notice", NoticeArgs{Mailbox: "human"}, nil)
	person.Call("notice", NoticeArgs{}, &nr)
	if !nr.New {
		t.Fatalf("a detached notice consumed the person's: %+v", nr)
	}
	if err := c.Call("link-hello", LinkHelloArgs{Machine: "s2"}, nil); err != nil {
		t.Fatalf("bridge without a terminal must link: %v", err)
	}
}
