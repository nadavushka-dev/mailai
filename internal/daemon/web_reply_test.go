package daemon

import (
	"strings"
	"testing"
)

// Found on UbCl (2026-11-24): a reply to one's own message went back to
// oneself. Parity with the TUI: the page replies to the newest message
// with a party other than the person, to that party (its sender, or the
// recipient of the person's own message), and skips mailai's own notes.
func TestWeb_ReplyGoesToTheOtherParty(t *testing.T) {
	sock, _ := startDaemon(t, placed())
	cl, base := webClient(t, dial(t, sock))
	_, page := get(t, cl, base+"/")
	for _, want := range []string{
		"function replyTarget(messages)",
		"if (isDaemonNote(m)) continue;",
		"if (!isHuman(m.from)) return {mid: m.mid, to: m.from};",
		"if (m.to && !isHuman(m.to)) return {mid: m.mid, to: m.to};",
		"const target = replyTarget(messages);",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "find(m => !isHuman(m.from)) || first") {
		t.Fatal("the page still falls back to the person's own message")
	}
}
