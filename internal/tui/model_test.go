package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

type fake struct {
	who      daemon.WhoamiResult
	items    []store.Item
	threads  []daemon.ThreadRow
	thread   map[string][]store.Message
	answered []string
	replied  []string
	dismiss  []uint64
	failNext error
}

func (f *fake) Whoami() (daemon.WhoamiResult, error)      { return f.who, nil }
func (f *fake) Inbox(all bool) ([]store.Item, error)      { return f.items, nil }
func (f *fake) Threads() ([]daemon.ThreadRow, error)      { return f.threads, nil }
func (f *fake) Thread(id string) ([]store.Message, error) { return f.thread[id], nil }
func (f *fake) Answer(item uint64, text, msg string) error {
	if f.failNext != nil {
		e := f.failNext
		f.failNext = nil
		return e
	}
	f.answered = append(f.answered, strings.TrimSpace(strings.Join([]string{itoa(item), text, msg}, " ")))
	return nil
}
func (f *fake) Dismiss(item uint64) error { f.dismiss = append(f.dismiss, item); return nil }
func (f *fake) Reply(mid string, to store.Address, text string) error {
	f.replied = append(f.replied, mid+" "+string(to)+" "+text)
	return nil
}
func (f *fake) Events() <-chan proto.Frame { return nil }

func itoa(n uint64) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n))) }

func person() *fake {
	return &fake{who: daemon.WhoamiResult{Principal: "human", Proof: store.ProofOutsidePane},
		items: []store.Item{
			{ID: 1, Kind: store.ItemAskHuman, From: "worker", Question: "Deploy?", Options: []string{"yes", "no"}, ThreadID: "local:1", MID: "local:1"},
			{ID: 2, Kind: store.ItemApproval, From: "w1:p3", Subject: "Bash", Question: "git push --force", Risky: true},
		},
		threads: []daemon.ThreadRow{{ThreadID: "local:1", From: "worker", To: "human", Subject: "deploy", Count: 1, Unread: 1, LastFrom: "worker"}},
		thread:  map[string][]store.Message{"local:1": {{ID: 1, MID: "local:1", From: "worker", To: "human", Text: "Deploy?\nreally", SenderProof: store.ProofPane}}},
	}
}

// run applies a key and runs any returned command synchronously.
func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.Msg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var cmd tea.Cmd
		var mm tea.Model
		mm, cmd = m.Update(msg)
		m = mm.(Model)
		m = drain(m, cmd)
	}
	return m
}

func drain(m Model, cmd tea.Cmd) Model {
	for i := 0; cmd != nil && i < 10; i++ {
		msg := cmd()
		if msg == nil {
			return m
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = drain(m, c)
			}
			return m
		}
		var mm tea.Model
		mm, cmd = m.Update(msg)
		m = mm.(Model)
	}
	return m
}

func start(f Backend) Model {
	m := New(f)
	m.now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	return drain(m, m.Init())
}

func TestInboxViewListsItemsWithOptionsAndRisky(t *testing.T) {
	m := start(person())
	v := m.View()
	if !strings.Contains(v, "Inbox") || !strings.Contains(v, "#1") || !strings.Contains(v, "Deploy?") || !strings.Contains(v, "1 yes") || !strings.Contains(v, "2 no") {
		t.Fatalf("%s", v)
	}
	if !strings.Contains(v, "risky approval") {
		t.Fatalf("%s", v)
	}
	// The detail pane shows the selected item in full.
	v = press(m, "j").View()
	if !strings.Contains(v, "git push --force") || !strings.Contains(v, "marked risky") {
		t.Fatalf("%s", v)
	}
	if !strings.Contains(v, "human (outside_pane)") || strings.Contains(v, "browse only") {
		t.Fatalf("%s", v)
	}
}

func TestAnswerByNumberAndByText(t *testing.T) {
	f := person()
	m := start(f)
	m = press(m, "2") // option 2 of item 1: no
	if len(f.answered) != 1 || f.answered[0] != "1 no" {
		t.Fatalf("%v", f.answered)
	}
	m = press(m, "r")
	if m.mode != modeAnswer {
		t.Fatal("r should open the answer input")
	}
	m = press(m, "y", "e", "s", "enter")
	if len(f.answered) != 2 || f.answered[1] != "1 yes" {
		t.Fatalf("%v", f.answered)
	}
}

func TestApprovalAllowDenyWithMessageAndDismiss(t *testing.T) {
	f := person()
	m := start(f)
	m = press(m, "j", "a")
	if len(f.answered) != 1 || f.answered[0] != "2 allow" {
		t.Fatalf("%v", f.answered)
	}
	m = press(m, "d")
	if m.mode != modeDenyMessage {
		t.Fatal("d should ask for a reason")
	}
	m = press(m, "n", "o", "enter")
	if len(f.answered) != 2 || f.answered[1] != "2 deny no" {
		t.Fatalf("%v", f.answered)
	}
	m = press(m, "x")
	if len(f.dismiss) != 1 || f.dismiss[0] != 2 {
		t.Fatalf("%v", f.dismiss)
	}
}

func TestMailViewOpenThreadAndReply(t *testing.T) {
	f := person()
	m := start(f)
	m = press(m, "m")
	if v := m.View(); !strings.Contains(v, "worker") || !strings.Contains(v, "deploy") || !strings.Contains(v, "messages") {
		t.Fatalf("%s", v)
	}
	m = press(m, "enter")
	v := m.View()
	if !strings.Contains(v, "thread local:1") || !strings.Contains(v, "│ Deploy?") || !strings.Contains(v, "│ really") || !strings.Contains(v, "worker pane") {
		t.Fatalf("%s", v)
	}
	m = press(m, "r", "o", "k", "enter")
	if len(f.replied) != 1 || f.replied[0] != "local:1 worker ok" {
		t.Fatalf("%v", f.replied)
	}
	m = press(m, "esc")
	if m.open != nil {
		t.Fatal("esc should close the thread")
	}
}

// SPEC §12: inside a pane the TUI browses but never answers.
func TestBrowseOnlyInsideAPane(t *testing.T) {
	f := person()
	f.who = daemon.WhoamiResult{Principal: "w2Y:pY", Proof: store.ProofPane}
	m := start(f)
	if v := m.View(); !strings.Contains(v, "browse only") {
		t.Fatalf("%s", v)
	}
	m = press(m, "1", "j", "a", "x", "r")
	if len(f.answered)+len(f.dismiss)+len(f.replied) != 0 {
		t.Fatal("acted from inside a pane")
	}
	if !strings.Contains(m.View(), "browse only: this client is inside a pane") {
		t.Fatalf("%s", m.View())
	}
}

func TestErrorsShowAndFade(t *testing.T) {
	f := person()
	f.failNext = errors.New("not_human: nope")
	m := start(f)
	m = press(m, "1")
	if !strings.Contains(m.View(), "not_human: nope") {
		t.Fatalf("%s", m.View())
	}
	later := m.now().Add(5 * time.Second)
	m.now = func() time.Time { return later }
	mm, _ := m.Update(tickMsg(later))
	if strings.Contains(mm.(Model).View(), "not_human") {
		t.Fatal("status did not fade")
	}
}

func TestEmptyStates(t *testing.T) {
	f := &fake{who: daemon.WhoamiResult{Principal: "human", Proof: store.ProofOutsidePane}}
	m := start(f)
	if !strings.Contains(m.View(), "inbox empty") {
		t.Fatal(m.View())
	}
	m = press(m, "tab")
	if !strings.Contains(m.View(), "no mail yet") {
		t.Fatal(m.View())
	}
	m = press(m, "enter", "r", "x", "j", "k") // nothing to act on, no panic
	_ = m
}
