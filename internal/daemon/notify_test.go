package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/notify"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// fakeNotifier records notifications instead of showing them.
type fakeNotifier struct {
	mu  sync.Mutex
	got []string // "title|body"
}

func (f *fakeNotifier) Notify(title, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, title+"|"+body)
}

func (f *fakeNotifier) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// settle waits out a coalescing window and checks exactly want arrived.
func (f *fakeNotifier) expect(t *testing.T, wait time.Duration, want ...string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && len(f.all()) < len(want) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(wait) // nothing more may follow
	got := f.all()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notifications:\n got  %q\n want %q", got, want)
	}
}

func notifyDaemon(t *testing.T, every time.Duration) (string, *fakeNotifier) {
	t.Helper()
	fn := &fakeNotifier{}
	sock, _ := startDaemonWith(t, Options{Notifier: fn, NotifyEvery: every,
		Placer: placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofPane, Principal: "w1:p3"})})
	return sock, fn
}

// SPEC §12b: a new Inbox item notifies the person once, in one line;
// answering it, and mail that opens no item, notify nothing.
func TestNotify_NewItemOnceAnsweredNever(t *testing.T) {
	sock, fn := notifyDaemon(t, 100*time.Millisecond)
	worker := dialAs(t, sock, "worker")
	person := dial(t, sock)
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Merge PR 51?", Options: []string{"yes", "no"}, NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	fn.expect(t, 300*time.Millisecond, "mailai|ask_human from worker: Merge PR 51? [yes|no]")
	if err := person.Call("answer", AnswerArgs{Item: r.Item.ID, Text: "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	// Plain mail to the person and agent-to-agent asks are Mail, not Inbox.
	if err := worker.Call("send", SendArgs{To: "human", Text: "fyi"}, nil); err != nil {
		t.Fatal(err)
	}
	go worker.Call("ask", AskArgs{To: "reviewer", Text: "lgtm?", TimeoutMS: 300}, nil)
	fn.expect(t, 300*time.Millisecond, "mailai|ask_human from worker: Merge PR 51? [yes|no]")
}

// Approvals and plain asks to the person notify too; a risky one says so.
func TestNotify_ApprovalsAndAsks(t *testing.T) {
	sock, fn := notifyDaemon(t, 10*time.Millisecond)
	hook := dialAs(t, sock, "w1:p3")
	worker := dialAs(t, sock, "worker")
	go hook.Call("hold-approval", HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "rm -rf node_modules", Risky: true, TimeoutMS: 200}, nil)
	fn.expect(t, 100*time.Millisecond, "mailai|risky approval from w1:p3: Bash: rm -rf node_modules")
	go hook.Call("hold-approval", HoldApprovalArgs{Tool: "Edit", Summary: "main.go", TimeoutMS: 200}, nil)
	fn.expect(t, 100*time.Millisecond, "mailai|risky approval from w1:p3: Bash: rm -rf node_modules", "mailai|approval from w1:p3: Edit: main.go")
	go worker.Call("ask", AskArgs{To: "human", Subject: "retry budget", Text: "how many?", TimeoutMS: 200}, nil)
	fn.expect(t, 100*time.Millisecond, "mailai|risky approval from w1:p3: Bash: rm -rf node_modules", "mailai|approval from w1:p3: Edit: main.go",
		"mailai|ask from worker: retry budget")
}

// A burst is one notification now and one summary when the window ends;
// an item closed meanwhile is not counted.
func TestNotify_BurstCoalesces(t *testing.T) {
	sock, fn := notifyDaemon(t, 300*time.Millisecond)
	worker := dialAs(t, sock, "worker")
	person := dial(t, sock)
	ids := []uint64{}
	for _, q := range []string{"one?", "two?", "three?", "four?", "five?"} {
		var r AskHumanResult
		if err := worker.Call("ask-human", AskHumanArgs{Text: q, NoWait: true}, &r); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.Item.ID)
	}
	if got := fn.all(); len(got) != 1 || got[0] != "mailai|ask_human from worker: one?" {
		t.Fatalf("%q", got)
	}
	// "three?" is answered before the window ends: it no longer waits.
	if err := person.Call("dismiss", DismissArgs{Item: ids[2]}, nil); err != nil {
		t.Fatal(err)
	}
	fn.expect(t, 600*time.Millisecond, "mailai|ask_human from worker: one?", "mailai|ask_human from worker: two? (+2 more waiting)")

	// All of a window's items closed: nothing follows.
	var a, b AskHumanResult
	worker.Call("ask-human", AskHumanArgs{Text: "six?", NoWait: true}, &a) // window over: at once
	worker.Call("ask-human", AskHumanArgs{Text: "seven?", NoWait: true}, &b)
	person.Call("dismiss", DismissArgs{Item: b.Item.ID}, nil)
	fn.expect(t, 600*time.Millisecond, "mailai|ask_human from worker: one?", "mailai|ask_human from worker: two? (+2 more waiting)",
		"mailai|ask_human from worker: six?")
}

// The body is one line of at most notify.MaxBody bytes, with no control
// characters, the options kept visible.
func TestNotify_BodySanitized(t *testing.T) {
	sock, fn := notifyDaemon(t, 10*time.Millisecond)
	worker := dialAs(t, sock, "worker")
	q := "line one\x1b[2J\nline two\r" + strings.Repeat("x", 130)
	if err := worker.Call("ask-human", AskHumanArgs{Text: q, Options: []string{"yes", "no"}, NoWait: true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a notification", func() bool { return len(fn.all()) == 1 })
	body := strings.TrimPrefix(fn.all()[0], "mailai|")
	if len(body) > notify.MaxBody || !strings.HasPrefix(body, "ask_human from worker: line one [2J line two xxx") || !strings.HasSuffix(body, "… [yes|no]") {
		t.Fatalf("%d %q", len(body), body)
	}
	for _, r := range body {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character in %q", body)
		}
	}
}

// SPEC §13a: items from a spoke open on the inbox machine, so only the
// inbox machine notifies.
func TestStar_OnlyInboxMachineNotifies(t *testing.T) {
	hubN, spokeN := &fakeNotifier{}, &fakeNotifier{}
	s := newStar(t, func(hub, spoke *Options) {
		hub.Notifier, hub.NotifyEvery = hubN, 10*time.Millisecond
		spoke.Notifier, spoke.NotifyEvery = spokeN, 10*time.Millisecond
	})
	worker := dialAs(t, s.spokeSock, "worker")
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Merge PR 51?", Options: []string{"yes", "no"}, NoWait: true}, nil); err != nil {
		t.Fatal(err)
	}
	hubN.expect(t, 200*time.Millisecond, "mailai|ask_human from worker@s2: Merge PR 51? [yes|no]")
	go worker.Call("hold-approval", HoldApprovalArgs{Tool: "Bash", Summary: "git push", Risky: true, TimeoutMS: 300}, nil)
	hubN.expect(t, 200*time.Millisecond, "mailai|ask_human from worker@s2: Merge PR 51? [yes|no]", "mailai|risky approval from worker@s2: Bash: git push")
	spokeN.expect(t, 0)
}
