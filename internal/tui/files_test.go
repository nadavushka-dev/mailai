package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Files an agent attached show on the item and in the thread, numbered, and
// v opens one with the system viewer (the web page's parity, SPEC §12a).
func TestFilesShowAndOpen(t *testing.T) {
	f := person()
	f.items[0].Attachments = []store.Attachment{
		{Kind: "image", Path: "/work/design.png", Bytes: 2048},
		{Kind: "file", Path: "/work/notes.md", Bytes: 10, Changed: true},
	}
	m := start(f)
	var opened []string
	m.opener = func(p string) error { opened = append(opened, p); return nil }

	v := m.View()
	for _, want := range []string{"1 design.png", "2.0 KB", "2 notes.md", "changed since sent", "v open a file"} {
		if !strings.Contains(v, want) {
			t.Fatalf("inbox detail lacks %q:\n%s", want, v)
		}
	}
	// Several files: v asks which, a digit opens it. Digits do not pick an
	// answer meanwhile.
	m = press(m, "v")
	if m.mode != modeFile {
		t.Fatalf("v with two files should ask which, mode %v", m.mode)
	}
	m = press(m, "2")
	if len(opened) != 1 || opened[0] != "/work/notes.md" || len(f.answered) != 0 {
		t.Fatalf("opened %v, answers %v", opened, f.answered)
	}
	if !strings.Contains(m.View(), "opened notes.md") {
		t.Fatalf("no status: %s", m.View())
	}
	// esc cancels without opening.
	m = press(m, "v", "esc")
	if m.mode != modeNone || len(opened) != 1 {
		t.Fatalf("esc: mode %v opened %v", m.mode, opened)
	}

	// In a thread: one file opens at once; a gone one is not opened.
	f.thread["local:1"][0].Attachments = []store.Attachment{{Kind: "file", Path: "/work/run.log", Bytes: 5}}
	m = press(m, "enter")
	if !strings.Contains(m.View(), "1 run.log") {
		t.Fatalf("thread lacks the file:\n%s", m.View())
	}
	m = press(m, "v")
	if len(opened) != 2 || opened[1] != "/work/run.log" {
		t.Fatalf("thread open: %v", opened)
	}
	f.thread["local:1"][0].Attachments[0].Missing = true
	m = press(m, "esc", "enter", "v")
	if len(opened) != 2 || !strings.Contains(m.View(), "gone") {
		t.Fatalf("a gone file was opened: %v\n%s", opened, m.View())
	}

	// No viewer on this machine (over ssh): say where the file is.
	f.thread["local:1"][0].Attachments[0].Missing = false
	m.opener = func(string) error { return errors.New("no display") }
	m = press(m, "esc", "enter", "v")
	if !strings.Contains(m.View(), "/work/run.log") || !strings.Contains(m.View(), "no viewer") {
		t.Fatalf("no viewer: %s", m.View())
	}
}
