// Package render turns messages into what a terminal, and an agent, reads.
// Bodies always sit inside a fence so a reader can tell data from
// instructions (D3); notices are bounded lines holding nothing a sender
// chose (D12).
package render

import (
	"fmt"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Fence renders one message: a header that says what the daemon knows about
// the sender, every body line prefixed with "│ ", and a footer.
func Fence(m store.Message) string {
	var b strings.Builder
	b.WriteString(header(m))
	b.WriteByte('\n')
	text := strings.TrimRight(m.Text, "\n")
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("│ ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(m.Attachments) > 0 {
		b.WriteString("attachments:\n")
		for _, a := range m.Attachments {
			b.WriteString("  ")
			b.WriteString(a.Path)
			switch {
			case a.Missing:
				b.WriteString(" (missing)")
			case a.Changed:
				fmt.Fprintf(&b, " (%s, changed since sent)", humanBytes(a.Bytes))
			case a.Bytes > 0:
				fmt.Fprintf(&b, " (%s)", humanBytes(a.Bytes))
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("── end ──\n")
	return b.String()
}

func header(m store.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "── #%d from %s", m.ID, m.From)
	// A pane-proven sender's pane, daemon-chosen, beside its name (§9).
	if m.SenderPane != "" && m.SenderPane != string(m.From) {
		fmt.Fprintf(&b, " · %s", m.SenderPane)
	}
	fmt.Fprintf(&b, " (%s)", trust(m))
	if m.Kind != "" && m.Kind != store.KindMail {
		fmt.Fprintf(&b, " · %s", m.Kind)
	}
	if m.Subject != "" {
		fmt.Fprintf(&b, " · subject: %s", m.Subject)
	}
	if m.ReplyTo != "" {
		fmt.Fprintf(&b, " · re %s", m.ReplyTo)
	}
	b.WriteString(" ──")
	return b.String()
}

// trust is the one phrase that says how far to believe the sender.
func trust(m store.Message) string {
	switch {
	case m.Verified != "":
		return "verified " + strings.ReplaceAll(m.Verified, ":", " ")
	case m.ClaimedHuman:
		return "automated reply"
	case m.Origin == "link":
		if m.OriginHost != "" {
			return fmt.Sprintf("link, claims %s", m.OriginHost)
		}
		return "link"
	case m.SenderProof != "":
		return string(m.SenderProof)
	}
	return "unplaced"
}

// Summary is one line per message: id, sender, proof, subject. An agent
// triages from it before paying for bodies (D12).
func Summary(ms []store.Message) string {
	var b strings.Builder
	for _, m := range ms {
		mark := "  "
		if m.ReadAt == 0 {
			mark = "* "
		}
		subj := m.Subject
		if subj == "" {
			subj = "(no subject)"
		}
		fmt.Fprintf(&b, "%s#%d %s (%s) %s\n", mark, m.ID, m.From, trust(m), subj)
	}
	return b.String()
}

// Notice is the one line an agent is told when mail waits (SPEC §7, D12):
// a count, at most three sender addresses sanitised and clipped, and the
// read command with extra flags. Never a subject or a body. Empty when n is 0.
func Notice(n int, from []store.Address, readFlags string) string {
	if n <= 0 {
		return ""
	}
	cmd := "mailai read --unread"
	if readFlags != "" {
		cmd += " " + readFlags
	}
	word, it := "messages", "them"
	if n == 1 {
		word, it = "message", "it"
	}
	senders := ""
	if len(from) > 0 {
		var names []string
		for i, a := range from {
			if i == 3 {
				names = append(names, fmt.Sprintf("+%d more", len(from)-3))
				break
			}
			names = append(names, clean(string(a), 24))
		}
		senders = " from " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("You have %d unread mailai %s%s. Read %s with: %s", n, word, senders, it, cmd)
}

// clean keeps an address to the characters an address may hold and clips it.
func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '@', r == '-', r == '_', r == '.', r == '/':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= max {
			return b.String() + "…"
		}
	}
	return b.String()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
