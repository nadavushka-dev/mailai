package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Layout: a tab bar; a list pane beside a detail pane; the input or the
// key hints; a status line when something is wrong.
//
//  Inbox 3 │ Mail                         human (outside_pane)
//  ┌ Inbox ───────────────────┐┌ #1 ask_human · worker ──────┐
//  │ #1 ask_human  worker  …  ││ Deploy to staging?           │
//  └──────────────────────────┘└──────────────────────────────┘
//  enter open  r answer  1-8 pick  a allow  d deny  x dismiss  …

func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	w, h := m.width, m.height
	header := m.header(w)
	footer := m.footer(w)
	bodyH := h - 3 // header, footer, status
	if bodyH < 6 {
		bodyH = 6
	}
	listW := w * 55 / 100
	if w < 90 {
		listW = w
	}
	detailW := w - listW
	var body string
	if detailW < 30 {
		body = box(m.listTitle(), m.listBody(listW-2, bodyH-2), listW, bodyH, m.open == nil)
	} else {
		list := box(m.listTitle(), m.listBody(listW-2, bodyH-2), listW, bodyH, m.open == nil)
		detail := box(m.detailTitle(), m.detailBody(detailW-2, bodyH-2), detailW, bodyH, m.open != nil)
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, detail)
	}
	status := ""
	if m.status != "" {
		if m.status == "done" {
			status = styleOK.Render(" ✓ done")
		} else {
			status = styleBad.Render(fit(" "+m.status, w))
		}
	}
	return header + "\n" + body + "\n" + footer + "\n" + fit(status, w)
}

func (m Model) header(w int) string {
	open := 0
	for _, it := range m.items {
		if it.ClosedAt == 0 {
			open++
		}
	}
	inbox := "Inbox"
	if open > 0 {
		inbox += fmt.Sprintf(" %d", open)
	}
	tabs := []string{inbox, "Mail"}
	var left strings.Builder
	for i, t := range tabs {
		if View(i) == m.view {
			left.WriteString(styleTabOn.Render(t))
		} else {
			left.WriteString(styleTab.Render(t))
		}
		if i == 0 {
			left.WriteString(styleDim.Render("│"))
		}
	}
	who := styleDim.Render(fmt.Sprintf("%s (%s)", m.who.Principal, m.who.Proof))
	if !m.person {
		who = styleWarn.Render("browse only ") + who
	}
	return padBetween(left.String(), who+" ", w)
}

func (m Model) footer(w int) string {
	if m.mode != modeNone {
		return fit(" "+m.input.View(), w)
	}
	key := func(k, does string) string { return styleAccent.Render(k) + " " + does }
	var parts []string
	switch {
	case m.open != nil:
		parts = []string{key("r", "reply"), key("j/k", "scroll"), key("esc", "back"), key("o", "oldest")}
	case m.view == ViewInbox:
		parts = []string{key("enter", "open"), key("r", "answer"), key("1-8", "pick"), key("a", "allow"), key("d", "deny"), key("x", "dismiss"), key("A", "all"), key("o", "oldest")}
	default:
		parts = []string{key("enter", "open"), key("r", "reply"), key("t", "to me/agents"), key("u", "unread"), key("f", "filter"), key("o", "oldest")}
	}
	if len(m.files()) > 0 {
		parts = append([]string{key("v", "open a file")}, parts...)
	}
	parts = append(parts, key("tab", "view"), key("q", "quit"))
	return fit(" "+strings.Join(parts, "  "), w)
}

func (m Model) listTitle() string {
	if m.view == ViewInbox {
		if m.showAll {
			return "Inbox · all"
		}
		return "Inbox · waiting on you"
	}
	title := "Mail"
	switch m.scope {
	case scopeMe:
		title += " · to me"
	case scopeAgents:
		title += " · between agents"
	}
	if m.filterUnread {
		title += " · unread"
	}
	if m.filterWho != "" {
		title += " · " + m.filterWho
	}
	if m.filterSession != "" {
		title += " · session " + m.filterSession
	}
	if m.filterMachine != "" {
		title += " · " + m.filterMachine
	}
	if title == "Mail" {
		return "Mail · every thread"
	}
	return title
}

func (m Model) listBody(w, h int) string {
	if m.view == ViewInbox {
		strip := !m.showAll && len(m.recent) > 0
		if len(m.items) == 0 && !strip {
			return center(styleDim.Render("inbox empty: nothing waits on you"), w, h)
		}
		var rows []string
		if len(m.items) == 0 {
			rows = append(rows, styleDim.Render("inbox empty: nothing waits on you"))
		}
		for i, it := range m.items {
			rows = append(rows, m.itemRow(it, w, i == m.cursor))
		}
		// Recently closed (§7a): dim, not selectable, newest first.
		if strip {
			rows = append(rows, "", styleDim.Render(fit("── Recently closed "+strings.Repeat("─", w), w)))
			for _, it := range m.recent {
				rows = append(rows, m.recentRow(it, w))
			}
		}
		return strings.Join(window(rows, m.cursor, h), "\n")
	}
	if len(m.threads) == 0 {
		return center(styleDim.Render("no mail yet"), w, h)
	}
	threads := m.filteredThreads()
	if len(threads) == 0 {
		if len(m.threads) == 0 {
			return center(styleDim.Render("no mail yet"), w, h)
		}
		return center(styleDim.Render("no threads match"), w, h)
	}
	var rows []string
	for i, t := range threads {
		rows = append(rows, m.threadRow(t, w, i == m.cursor))
	}
	return strings.Join(window(rows, m.cursor, h), "\n")
}

// window keeps the cursor row visible in h lines.
func window(rows []string, cursor, h int) []string {
	if len(rows) <= h || h <= 0 {
		return rows
	}
	top := cursor - h/2
	if top < 0 {
		top = 0
	}
	if top > len(rows)-h {
		top = len(rows) - h
	}
	return rows[top : top+h]
}

func (m Model) itemRow(it store.Item, w int, sel bool) string {
	kind := string(it.Kind)
	kindStyled := styleDim.Render(fit(kind, 14))
	if it.Risky {
		kindStyled = styleBad.Render(fit("risky "+kind, 14))
	}
	q := it.Question
	if it.Kind == store.ItemApproval {
		q = it.Subject + ": " + it.Question
	}
	state := styleWarn.Render("waiting " + ago(m.now(), it.OpenedAt))
	if it.MaybeInSessionAt != 0 {
		state = styleSuper.Render(daemon.MaybeBadge)
	}
	if it.ClosedAt != 0 {
		state = styleDim.Render(it.ClosedBy)
		if it.DecidedBy != "" && it.DecidedBy != "human" {
			state = styleSuper.Render(it.ClosedBy + " by " + it.DecidedBy)
		}
		if it.ClosedBy == store.ClosedInSession {
			state = styleOK.Render(daemon.InSessionBadge)
		}
	}
	who := string(it.From)
	if it.Session != "" {
		who += " · " + it.Session
	}
	left := fmt.Sprintf("%s %s %s ", styleDim.Render(fmt.Sprintf("#%-3d", it.ID)), kindStyled, styleBold.Render(fit(who, 22)))
	room := w - lipgloss.Width(left) - lipgloss.Width(state) - 2
	row := padBetween(left+fit(oneLine(q), room), state, w)
	if sel {
		return styleSelected.Render(row)
	}
	return row
}

// recentRow is one dim line of the Recently closed strip: what, who, how,
// how long ago.
func (m Model) recentRow(it store.Item, w int) string {
	q := it.Question
	if it.Kind == store.ItemApproval {
		q = it.Subject + ": " + it.Question
	}
	when := " · " + ago(m.now(), it.ClosedAt)
	how := styleDim.Render(daemon.ClosedHow(it) + when)
	if it.ClosedBy == store.ClosedInSession {
		how = styleOK.Render(daemon.InSessionBadge) + styleDim.Render(when)
	}
	left := fmt.Sprintf("#%-3d %s %s ", it.ID, fit(string(it.Kind), 14), fit(string(it.From), 22))
	room := w - lipgloss.Width(left) - lipgloss.Width(how) - 2
	return padBetween(styleDim.Render(left+fit(oneLine(q), room)), how, w)
}

// clock is the local time of day of a timestamp, for notes.
func clock(ns int64) string { return time.Unix(0, ns).Format("15:04") }

func (m Model) threadRow(t daemon.ThreadRow, w int, sel bool) string {
	// ● unread by the person; ○ not yet read by an agent in it.
	mark := " "
	switch {
	case t.UnreadMine > 0:
		mark = styleAccent.Render("●")
	case t.Unread > 0:
		mark = styleDim.Render("○")
	}
	subj := t.Subject
	if subj == "" {
		subj = t.First
	}
	to := string(t.To)
	if to == "" {
		to = "session " + t.Session
	}
	last := string(t.LastFrom)
	switch {
	case t.Verified == "human":
		last = styleOK.Render(last)
	case strings.HasPrefix(t.Verified, "supervisor:"):
		last = styleSuper.Render(last)
	}
	right := fmt.Sprintf("%s %s  %s", styleDim.Render(fmt.Sprintf("%d msgs", t.Count)), last, styleDim.Render(ago(m.now(), t.LastAt)))
	left := fmt.Sprintf("%s %s %s %s ", mark, styleBold.Render(fit(string(t.From), 12)), styleDim.Render("→"), fit(to, 12))
	room := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if room < 16 {
		// Tight: keep the subject, drop the count.
		right = last + "  " + styleDim.Render(ago(m.now(), t.LastAt))
		room = w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	}
	row := padBetween(left+fit(oneLine(subj), room), right, w)
	if sel {
		return styleSelected.Render(row)
	}
	return row
}

func (m Model) detailTitle() string {
	if m.open != nil {
		return "thread " + m.openID
	}
	if it, ok := m.current(); ok {
		return fmt.Sprintf("#%d %s · %s", it.ID, it.Kind, it.From)
	}
	if m.view == ViewMail && m.cursor < len(m.threads) {
		return "thread"
	}
	return "detail"
}

func (m Model) detailBody(w, h int) string {
	if m.open != nil {
		return m.threadBody(w, h)
	}
	if m.view == ViewInbox {
		it, ok := m.current()
		if !ok {
			return center(styleDim.Render("nothing selected"), w, h)
		}
		var b strings.Builder
		row := func(k, v string) {
			if v != "" {
				fmt.Fprintf(&b, "%s %s\n", styleDim.Render(fit(k, 9)), v)
			}
		}
		row("from", styleBold.Render(string(it.From)))
		row("kind", string(it.Kind))
		if it.Risky {
			row("risk", styleBad.Render("marked risky by "+it.Harness+": waits for you"))
		}
		row("session", it.Session)
		row("asked", ago(m.now(), it.OpenedAt)+" ago")
		if it.ClosedAt == 0 && it.MaybeInSessionAt != 0 {
			row("note", styleSuper.Render("maybe answered in the session at "+clock(it.MaybeInSessionAt))+styleDim.Render("; d dismisses"))
		}
		if it.ClosedAt != 0 {
			if it.ClosedBy == store.ClosedInSession {
				row("closed", "answered in the session at "+clock(it.ClosedAt))
			} else {
				who := it.DecidedBy
				if who == "" {
					who = "human"
				}
				row("closed", fmt.Sprintf("%s by %s", it.ClosedBy, who))
			}
		}
		b.WriteString("\n")
		if it.Kind == store.ItemApproval {
			b.WriteString(styleBold.Render(it.Subject) + "\n")
			b.WriteString(wrap(it.Question, w) + "\n\n")
			b.WriteString(styleOK.Render("a") + " allow   " + styleBad.Render("d") + " deny   " + styleDim.Render("x dismiss") + "\n")
		} else {
			b.WriteString(wrap(it.Question, w) + "\n\n")
			if len(it.Options) > 0 {
				for i, o := range it.Options {
					fmt.Fprintf(&b, "  %s %s\n", styleAccent.Render(fmt.Sprintf("%d", i+1)), o)
				}
			} else {
				b.WriteString(styleDim.Render("free text: press r to answer") + "\n")
			}
		}
		if len(it.Attachments) > 0 {
			b.WriteString("\n" + styleDim.Render("files") + "\n")
			for i, a := range it.Attachments {
				b.WriteString(fileLine(i+1, a) + "\n")
			}
		}
		return b.String()
	}
	threads := m.filteredThreads()
	if m.cursor < len(threads) {
		t := threads[m.cursor]
		where := ""
		if t.Session != "" {
			where = "\nsession " + t.Session
		}
		return fmt.Sprintf("%s\n%s → %s%s\n%d messages, %d unread\n\n%s", styleBold.Render(orFirst(t)), t.From, t.To, where, t.Count, t.Unread, styleDim.Render("enter opens the thread, r replies"))
	}
	return center(styleDim.Render("nothing selected"), w, h)
}

func orFirst(t daemon.ThreadRow) string {
	if t.Subject != "" {
		return t.Subject
	}
	return t.First
}

// fileLine is one attachment, numbered for v: name, size, what happened to
// it since it was sent, and where it is.
func fileLine(n int, a store.Attachment) string {
	s := fmt.Sprintf("  %s %s  %s", styleAccent.Render(fmt.Sprint(n)), filepath.Base(a.Path), styleDim.Render(fileSize(a.Bytes)))
	switch {
	case a.Missing:
		s += "  " + styleBad.Render("gone")
	case a.Changed:
		s += "  " + styleWarn.Render("changed since sent")
	}
	return s + "  " + styleDim.Render(a.Path)
}

func fileSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func (m Model) threadBody(w, h int) string {
	var lines []string
	nfile := 0
	for _, x := range m.open {
		head := styleDim.Render(fmt.Sprintf("#%d ", x.ID)) + styleBold.Render(string(x.From))
		switch {
		case x.Verified == "human":
			head += " " + styleOK.Render("verified human")
		case strings.HasPrefix(x.Verified, "supervisor:"):
			head += " " + styleSuper.Render("verified "+strings.ReplaceAll(x.Verified, ":", " "))
		case x.ClaimedHuman:
			head += " " + styleWarn.Render("automated reply")
		default:
			head += " " + styleDim.Render(string(x.SenderProof))
		}
		if x.To != "" {
			head += styleDim.Render(" → ") + string(x.To)
		}
		if x.Subject != "" {
			head += styleDim.Render(" · ") + x.Subject
		}
		lines = append(lines, head)
		for _, l := range strings.Split(wrap(strings.TrimRight(x.Text, "\n"), w-2), "\n") {
			lines = append(lines, styleDim.Render("│ ")+l)
		}
		for _, a := range x.Attachments {
			nfile++
			lines = append(lines, fileLine(nfile, a))
		}
		lines = append(lines, "")
	}
	// One-line notes rendered from the thread's items, never stored as
	// messages (§7a).
	for _, it := range m.openItems {
		switch {
		case it.ClosedBy == store.ClosedInSession:
			lines = append(lines, styleDim.Render(fmt.Sprintf("── #%d answered in the session at %s", it.ID, clock(it.ClosedAt))))
		case it.ClosedAt == 0 && it.MaybeInSessionAt != 0:
			lines = append(lines, styleDim.Render(fmt.Sprintf("── #%d maybe answered in the session at %s", it.ID, clock(it.MaybeInSessionAt))))
		}
	}
	top := m.openTop
	if top > len(lines)-h {
		top = len(lines) - h
	}
	if top < 0 {
		top = 0
	}
	end := top + h
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[top:end], "\n")
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// wrap breaks text at spaces to width.
func wrap(s string, width int) string {
	if width < 10 {
		width = 10
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line == "" {
				line = word
			} else if lipgloss.Width(line)+1+lipgloss.Width(word) > width {
				out = append(out, line)
				line = word
			} else {
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func ago(now time.Time, ns int64) string {
	if ns == 0 {
		return ""
	}
	d := now.Sub(time.Unix(0, ns)).Round(time.Second)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
