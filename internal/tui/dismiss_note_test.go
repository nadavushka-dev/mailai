package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §7a: dismissing a question leaves one line from mailai in its
// thread. The thread shows it as a system note, not as a message: no
// sender header, no trust mark, never the person's words.
func TestThread_MailaiNoteIsASystemNote(t *testing.T) {
	f := person()
	at := ns(2 * time.Minute)
	f.thread["local:1"] = append(f.thread["local:1"],
		store.Message{ID: 2, MID: "local:2", Kind: store.KindReply, From: daemon.Bouncer, To: "worker", ReplyTo: "local:1",
			ThreadID: "local:1", Text: "#1 dismissed by the person", SentAt: at})
	m := wide(start(f))
	m = press(m, "m", "enter")
	v := m.View()
	want := "── #1 dismissed by the person at " + time.Unix(0, at).Format("15:04")
	if !strings.Contains(v, want) {
		t.Fatalf("want %q:\n%s", want, v)
	}
	if strings.Contains(v, "#2 mailai") || strings.Contains(v, "│ #1 dismissed") {
		t.Fatalf("drawn as a message:\n%s", v)
	}
	// Someone who only claims to be mailai is a message like any other.
	f.thread["local:1"][1].SenderProof = store.ProofDetached
	m = press(wide(start(f)), "m", "enter")
	if v := m.View(); strings.Contains(v, "── #1 dismissed") || !strings.Contains(v, "#2 mailai") {
		t.Fatalf("a claimed mailai drawn as a note:\n%s", v)
	}
}
