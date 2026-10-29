package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/identity"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
)

// A real identity.Placer over the real kernel: every test process is
// outside any pane, so tokens are what distinguish callers.
func startWithTokens(t *testing.T) (string, *tokens.Tokens) {
	t.Helper()
	sock := shortSock(t)
	tk, err := tokens.Open(filepath.Join(t.TempDir(), "tokens.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	pl := &identity.Placer{Proc: realProc{}, Tokens: tk}
	d := New(Options{Store: store.New(store.Options{}), Placer: pl, Tokens: tk})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sock, tk
}

func TestHello_TokenPlacesConnectionAndPersonSendsVerified(t *testing.T) {
	sock, _ := startWithTokens(t)
	person := dial(t, sock)
	var who WhoamiResult
	person.Call("whoami", nil, &who)
	if who.Proof != store.ProofOutsidePane || who.PID == 0 {
		t.Fatalf("test process should be outside_pane with a kernel pid: %+v", who)
	}
	var tn TokenNewResult
	if err := person.Call("token-new", TokenNewArgs{Name: "helper", Label: "l"}, &tn); err != nil {
		t.Fatal(err)
	}
	agent := dial(t, sock)
	if err := agent.Call("hello", HelloArgs{Token: tn.Token}, &who); err != nil {
		t.Fatal(err)
	}
	if who.Proof != store.ProofToken || who.Principal != "helper" {
		t.Fatalf("%+v", who)
	}
	// The agent is helper whatever it claims, and may not be human.
	var sr SendResult
	agent.Call("send", SendArgs{To: "human", Text: "hi"}, &sr)
	if sr.Message.From != "helper" || sr.Message.SenderProof != store.ProofToken || sr.Message.Verified != "" {
		t.Fatalf("%+v", sr.Message)
	}
	if err := agent.Call("send", SendArgs{From: "human", To: "x", Text: "yes"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
	// Token holders cannot manage tokens.
	if err := agent.Call("token-new", TokenNewArgs{Name: "evil"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("%v", err)
	}
	if err := agent.Call("token-revoke", TokenRevokeArgs{Name: "helper"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("%v", err)
	}
	// The person's reply is verified human (SPEC §9).
	person.Call("send", SendArgs{To: "helper", Text: "hello helper"}, &sr)
	if sr.Message.From != store.Human || sr.Message.Verified != "human" {
		t.Fatalf("%+v", sr.Message)
	}
	// ...but a message the person sends as someone else is not.
	var sr2 SendResult
	person.Call("send", SendArgs{From: "worker", To: "helper", Text: "x"}, &sr2)
	if sr2.Message.Verified != "" {
		t.Fatalf("%+v", sr2.Message)
	}
	// A bad or revoked token falls back to outside_pane, not an error.
	bad := dial(t, sock)
	bad.Call("hello", HelloArgs{Token: "mt_nope"}, &who)
	if who.Proof != store.ProofOutsidePane {
		t.Fatalf("%+v", who)
	}
	var rv map[string]int
	person.Call("token-revoke", TokenRevokeArgs{Name: "helper"}, &rv)
	if rv["revoked"] != 1 {
		t.Fatal(rv)
	}
	again := dial(t, sock)
	again.Call("hello", HelloArgs{Token: tn.Token}, &who)
	if who.Proof != store.ProofOutsidePane {
		t.Fatalf("revoked token still places: %+v", who)
	}
	if err := person.Call("token-new", TokenNewArgs{Name: "Bad Name"}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatal(err)
	}
}
