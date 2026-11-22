package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

func (f *fake) Recent() ([]store.Item, error) { return f.recent, nil }
func (f *fake) ThreadItems(id string) ([]store.Item, error) {
	return f.byThread[id], nil
}

var tuiNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func ns(ago time.Duration) int64 { return tuiNow.Add(-ago).UnixNano() }

// SPEC §7a: under the open items, a dim "Recently closed" strip lists what
// closed in the last hour, newest first, with how; the cursor stays on the
// open items.
func TestInSession_RecentlyClosedStrip(t *testing.T) {
	f := person()
	f.recent = []store.Item{
		{ID: 7, Kind: store.ItemAskHuman, From: "worker", Question: "Merge it?", ClosedAt: ns(time.Minute), ClosedBy: store.ClosedInSession},
		{ID: 6, Kind: store.ItemAsk, From: "builder", Question: "Which tag?", ClosedAt: ns(5 * time.Minute), ClosedBy: "answered", DecidedBy: "human"},
		{ID: 5, Kind: store.ItemAsk, From: "fixer", Question: "Retry?", ClosedAt: ns(9 * time.Minute), ClosedBy: "answered", DecidedBy: "supervisor:lead"},
		{ID: 4, Kind: store.ItemApproval, From: "w1:p3", Subject: "Bash", Question: "ls", ClosedAt: ns(20 * time.Minute), ClosedBy: "timeout"},
		{ID: 3, Kind: store.ItemAskHuman, From: "worker", Question: "Old?", ClosedAt: ns(30 * time.Minute), ClosedBy: "dismissed", DecidedBy: "human"},
	}
	m := wide(start(f))
	v := m.View()
	if !strings.Contains(v, "Recently closed") {
		t.Fatalf("%s", v)
	}
	order := []string{"Merge it?", "Which tag?", "Retry?", "Bash: ls", "Old?"}
	hows := []string{"in session", "by you", "by supervisor:lead", "timeout", "dismissed"}
	last := strings.Index(v, "Recently closed")
	for i, q := range order {
		at := strings.Index(v, q)
		if at < last {
			t.Fatalf("%q out of order or missing:\n%s", q, v)
		}
		last = at
		line := lineWith(v, q)
		if !strings.Contains(line, hows[i]) {
			t.Fatalf("%q: want %q in %q", q, hows[i], line)
		}
	}
	// The strip is not selectable: j stops at the last open item.
	m = press(m, "j", "j", "j", "j")
	if m.cursor != 1 {
		t.Fatalf("cursor %d", m.cursor)
	}
	// Empty Inbox: the strip still shows under the empty note.
	f2 := person()
	f2.items = nil
	f2.recent = f.recent[:1]
	v = wide(start(f2)).View()
	if !strings.Contains(v, "inbox empty") || !strings.Contains(v, "Recently closed") || !strings.Contains(v, "Merge it?") {
		t.Fatalf("%s", v)
	}
}

// An open item marked maybe answered in session carries a badge in the row
// and a line in the detail pane; d dismisses it like x.
func TestInSession_MaybeBadgeAndDismiss(t *testing.T) {
	f := person()
	f.items[0].MaybeInSessionAt = ns(2 * time.Minute)
	m := wide(start(f))
	v := m.View()
	if !strings.Contains(lineWith(v, "Deploy?"), "maybe answered in session") {
		t.Fatalf("%s", v)
	}
	if !strings.Contains(v, "maybe answered in the session at "+time.Unix(0, ns(2*time.Minute)).Format("15:04")) {
		t.Fatalf("%s", v)
	}
	m = press(m, "d")
	if len(f.dismiss) != 1 || f.dismiss[0] != 1 || m.mode != modeNone {
		t.Fatalf("%v mode %v", f.dismiss, m.mode)
	}
}

// The Mail view's thread shows a one-line note from the item, not a stored
// message: answered in the session at 10:42.
func TestInSession_ThreadNote(t *testing.T) {
	f := person()
	closedAt := ns(3 * time.Minute)
	f.byThread = map[string][]store.Item{"local:1": {{ID: 1, Kind: store.ItemAskHuman, From: "worker", ThreadID: "local:1", ClosedAt: closedAt, ClosedBy: store.ClosedInSession}}}
	m := start(f)
	m = press(m, "m", "enter")
	v := m.View()
	want := "answered in the session at " + time.Unix(0, closedAt).Format("15:04")
	if !strings.Contains(v, want) {
		t.Fatalf("want %q:\n%s", want, v)
	}
	if len(m.open) != 1 {
		t.Fatalf("the note must not be a message: %+v", m.open)
	}
}

// wide gives the list pane room for the question beside the state.
func wide(m Model) Model {
	m.width, m.height = 160, 40
	return m
}

func lineWith(v, sub string) string {
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}
