package daemon

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §9: an Inbox item shows the asker's pane beside its name, as thread
// rows do, when its message proved a pane: carried from the ask's message
// for an ask, recorded from the hook's placement for an approval.
func TestInboxItemsCarryTheSendersPane(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofPane, Principal: "reviewer", Pane: "w2Y:pT"}, Placement{Proof: store.ProofToken, Principal: "tok"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	reviewer := dialAs(t, sock, "reviewer")
	tok := dialAs(t, sock, "tok")
	var a1, a2 AskHumanResult
	if err := reviewer.Call("ask-human", AskHumanArgs{Text: "ok?", NoWait: true}, &a1); err != nil {
		t.Fatal(err)
	}
	if err := tok.Call("ask-human", AskHumanArgs{Text: "and me?", NoWait: true}, &a2); err != nil {
		t.Fatal(err)
	}
	var held HoldApprovalResult
	if err := reviewer.Call("hold-approval", HoldApprovalArgs{Harness: "claude", Tool: "Bash", Summary: "ls", TimeoutMS: 10}, &held); err != nil {
		t.Fatal(err)
	}
	if held.Item.FromPane != "w2Y:pT" {
		t.Fatalf("approval %+v", held.Item)
	}
	var ir InboxResult
	if err := person.Call("inbox", InboxArgs{All: true}, &ir); err != nil {
		t.Fatal(err)
	}
	panes := map[uint64]string{}
	for _, it := range ir.Items {
		panes[it.ID] = it.FromPane
	}
	if panes[a1.Item.ID] != "w2Y:pT" || panes[a2.Item.ID] != "" || panes[held.Item.ID] != "w2Y:pT" {
		t.Fatalf("%v", panes)
	}
}

// The web Inbox row shows it with the same helper as thread rows.
func TestWeb_InboxRowShowsTheSendersPane(t *testing.T) {
	if page := webPage(t); !strings.Contains(page, "${agentName(it.from)}${withPane(it.from, it.from_pane)}") {
		t.Fatal("the Inbox row does not show the sender's pane")
	}
}
