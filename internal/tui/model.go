package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// View is one of the two (D13).
type View int

const (
	ViewInbox View = iota
	ViewMail
)

// Model is the TUI state. It is a plain value the tests can drive with
// Update and inspect with View.
type Model struct {
	b      Backend
	who    daemon.WhoamiResult
	person bool
	view   View
	width  int
	height int

	items   []store.Item
	threads []daemon.ThreadRow
	cursor  int
	showAll bool

	// open is the thread being read, if any.
	open    []store.Message
	openID  string
	openTop int

	// input is the reply or answer being typed; mode says for what.
	input   textinput.Model
	mode    inputMode
	target  store.Item
	replyTo store.Message

	status string
	errAt  time.Time
	now    func() time.Time

	// Mail filters (SPEC §12): unread, principal, session, machine.
	filterUnread  bool
	filterWho     string
	filterSession string
	filterMachine string
	pendingReply  bool
}

type inputMode int

const (
	modeNone inputMode = iota
	modeAnswer
	modeReply
	modeDenyMessage
	modeFilter
)

// Messages between the program and the model.
type (
	refreshMsg struct {
		items   []store.Item
		threads []daemon.ThreadRow
		err     error
	}
	threadMsg struct {
		id   string
		msgs []store.Message
		err  error
	}
	eventMsg  struct{ f proto.Frame }
	actionMsg struct{ err error }
	tickMsg   time.Time
)

// New makes a model over a backend.
func New(b Backend) Model {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = store.MaxText
	m := Model{b: b, input: in, now: time.Now, width: 100, height: 30}
	if w, err := b.Whoami(); err == nil {
		m.who = w
		m.person = w.Proof == store.ProofOutsidePane
	}
	return m
}

// Run starts the program.
func Run(b Backend) error {
	_, err := tea.NewProgram(New(b), tea.WithAltScreen()).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.listen(), tick())
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m Model) refresh() tea.Cmd {
	b, all := m.b, m.showAll
	return func() tea.Msg {
		items, err := b.Inbox(all)
		if err != nil {
			return refreshMsg{err: err}
		}
		threads, err := b.Threads()
		return refreshMsg{items: items, threads: threads, err: err}
	}
}

func (m Model) listen() tea.Cmd {
	ch := m.b.Events()
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		f, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg{f}
	}
}

func (m Model) loadThread(id string) tea.Cmd {
	b := m.b
	return func() tea.Msg {
		msgs, err := b.Thread(id)
		return threadMsg{id: id, msgs: msgs, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case refreshMsg:
		if msg.err != nil {
			return m.fail(msg.err), nil
		}
		m.items, m.threads = msg.items, msg.threads
		m.clampCursor()
		return m, nil
	case threadMsg:
		if msg.err != nil {
			return m.fail(msg.err), nil
		}
		m.open, m.openID, m.openTop = msg.msgs, msg.id, 0
		if m.pendingReply {
			m.pendingReply = false
			return m.startReply()
		}
		return m, nil
	case eventMsg:
		// Any event may change a list; fetch, then keep listening.
		return m, tea.Batch(m.refresh(), m.listen())
	case actionMsg:
		if msg.err != nil {
			return m.fail(msg.err), m.refresh()
		}
		m.status = "done"
		cmds := []tea.Cmd{m.refresh()}
		if m.openID != "" {
			cmds = append(cmds, m.loadThread(m.openID))
		}
		return m, tea.Batch(cmds...)
	case tickMsg:
		if m.status != "" && m.now().Sub(m.errAt) > 4*time.Second {
			m.status = ""
		}
		return m, tick()
	case tea.KeyMsg:
		if m.mode != modeNone {
			return m.updateInput(msg)
		}
		return m.updateKeys(msg)
	}
	return m, nil
}

func (m Model) fail(err error) Model {
	m.status = err.Error()
	m.errAt = m.now()
	return m
}

func (m *Model) clampCursor() {
	n := m.rows()
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) rows() int {
	if m.view == ViewInbox {
		return len(m.items)
	}
	return len(m.filteredThreads())
}

// filteredThreads applies the Mail filters. An empty text filter matches all.
func (m Model) filteredThreads() []daemon.ThreadRow {
	var out []daemon.ThreadRow
	for _, t := range m.threads {
		if m.filterUnread && t.Unread == 0 {
			continue
		}
		if m.filterWho != "" && !addrMatch(t.From, t.To, t.LastFrom, m.filterWho) {
			continue
		}
		if m.filterSession != "" && !strings.EqualFold(t.Session, m.filterSession) {
			continue
		}
		if m.filterMachine != "" && !hostMatch(t.From, t.To, t.LastFrom, m.filterMachine) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func localName(a store.Address) string {
	s := string(a)
	if i := strings.IndexByte(s, '@'); i >= 0 {
		return s[:i]
	}
	return s
}

func addrMatch(from, to, last store.Address, who string) bool {
	who = strings.ToLower(who)
	for _, a := range []store.Address{from, to, last} {
		if strings.EqualFold(localName(a), who) {
			return true
		}
	}
	return false
}

func hostMatch(from, to, last store.Address, host string) bool {
	host = strings.ToLower(host)
	for _, a := range []store.Address{from, to, last} {
		s := string(a)
		i := strings.IndexByte(s, '@')
		if i >= 0 && strings.EqualFold(s[i+1:], host) {
			return true
		}
	}
	return false
}

// parseMailFilter reads "name", "session:api", "machine:s2", or a mix.
// An empty string clears every text filter.
func parseMailFilter(s string) (who, session, machine string) {
	for _, tok := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(tok, "session:"):
			session = strings.TrimPrefix(tok, "session:")
		case strings.HasPrefix(tok, "s:"):
			session = strings.TrimPrefix(tok, "s:")
		case strings.HasPrefix(tok, "machine:"):
			machine = strings.TrimPrefix(tok, "machine:")
		case strings.HasPrefix(tok, "m:"):
			machine = strings.TrimPrefix(tok, "m:")
		case strings.HasPrefix(tok, "principal:"):
			who = strings.TrimPrefix(tok, "principal:")
		case strings.HasPrefix(tok, "p:"):
			who = strings.TrimPrefix(tok, "p:")
		default:
			who = tok
		}
	}
	return who, session, machine
}

func (m Model) updateKeys(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "i":
		m.view, m.cursor, m.open, m.openID = ViewInbox, 0, nil, ""
		return m, nil
	case "m":
		m.view, m.cursor, m.open, m.openID = ViewMail, 0, nil, ""
		return m, nil
	case "tab":
		if m.view == ViewInbox {
			m.view = ViewMail
		} else {
			m.view = ViewInbox
		}
		m.cursor, m.open, m.openID = 0, nil, ""
		return m, nil
	case "j", "down":
		if m.open != nil {
			m.openTop++
			return m, nil
		}
		if m.cursor < m.rows()-1 {
			m.cursor++
		}
		return m, nil
	case "k", "up":
		if m.open != nil {
			if m.openTop > 0 {
				m.openTop--
			}
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "o":
		m.view, m.cursor, m.open, m.openID = ViewInbox, 0, nil, ""
		return m, nil
	case "u":
		if m.view == ViewMail && m.open == nil {
			m.filterUnread = !m.filterUnread
			m.clampCursor()
		}
		return m, nil
	case "f":
		if m.view != ViewMail || m.open != nil {
			return m, nil
		}
		m.mode = modeFilter
		m.input.Placeholder = "name, session:…, machine:… — empty clears"
		m.input.SetValue("")
		return m, m.input.Focus()
	case "A":
		m.showAll = !m.showAll
		return m, m.refresh()
	case "esc":
		if m.open != nil {
			m.open, m.openID = nil, ""
			return m, nil
		}
		return m, nil
	case "enter":
		if m.view == ViewInbox {
			if it, ok := m.current(); ok && it.ThreadID != "" {
				return m, m.loadThread(it.ThreadID)
			}
			return m, nil
		}
		rows := m.filteredThreads()
		if m.cursor < len(rows) {
			return m, m.loadThread(rows[m.cursor].ThreadID)
		}
		return m, nil
	case "r":
		if m.view == ViewMail && m.open == nil {
			rows := m.filteredThreads()
			if m.cursor < len(rows) {
				m.pendingReply = true
				return m, m.loadThread(rows[m.cursor].ThreadID)
			}
			return m, nil
		}
		return m.startReply()
	case "a", "y":
		if it, ok := m.current(); ok && it.Kind == store.ItemApproval && m.view == ViewInbox {
			return m.act(func() error { return m.b.Answer(it.ID, "allow", "") })
		}
		return m, nil
	case "d", "n":
		if it, ok := m.current(); ok && it.Kind == store.ItemApproval && m.view == ViewInbox {
			m.target, m.mode = it, modeDenyMessage
			m.input.Placeholder = "reason shown to the agent (enter for none)"
			m.input.SetValue("")
			return m, m.input.Focus()
		}
		return m, nil
	case "x":
		if it, ok := m.current(); ok && m.view == ViewInbox {
			return m.act(func() error { return m.b.Dismiss(it.ID) })
		}
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8":
		if it, ok := m.current(); ok && m.view == ViewInbox && it.Kind == store.ItemAskHuman {
			i := int(k.String()[0] - '1')
			if i < len(it.Options) {
				opt := it.Options[i]
				return m.act(func() error { return m.b.Answer(it.ID, opt, "") })
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) current() (store.Item, bool) {
	if m.view != ViewInbox || m.cursor >= len(m.items) {
		return store.Item{}, false
	}
	return m.items[m.cursor], true
}

// startReply opens the input for the current item or thread.
func (m Model) startReply() (tea.Model, tea.Cmd) {
	if !m.person {
		return m.fail(fmt.Errorf("browse only: this client is inside a pane, so its replies would not be the person's (SPEC §12)")), nil
	}
	switch {
	case m.view == ViewInbox:
		it, ok := m.current()
		if !ok || it.Kind == store.ItemApproval {
			return m, nil
		}
		m.target, m.mode = it, modeAnswer
		if len(it.Options) > 0 {
			m.input.Placeholder = "one of " + strings.Join(it.Options, " | ")
		} else {
			m.input.Placeholder = "your answer to " + string(it.From)
		}
	case m.open != nil:
		root := m.open[0]
		for _, x := range m.open {
			if x.From != store.Human {
				root = x
			}
		}
		m.replyTo, m.mode = root, modeReply
		m.input.Placeholder = "reply to " + string(root.From)
	default:
		return m, nil
	}
	m.input.SetValue("")
	return m, m.input.Focus()
}

func (m Model) updateInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNone
		m.input.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		mode, target, replyTo := m.mode, m.target, m.replyTo
		m.mode = modeNone
		m.input.Blur()
		switch mode {
		case modeAnswer:
			if text == "" {
				return m, nil
			}
			return m.act(func() error { return m.b.Answer(target.ID, text, "") })
		case modeDenyMessage:
			return m.act(func() error { return m.b.Answer(target.ID, "deny", text) })
		case modeReply:
			if text == "" {
				return m, nil
			}
			return m.act(func() error { return m.b.Reply(replyTo.MID, replyTo.From, text) })
		case modeFilter:
			m.filterWho, m.filterSession, m.filterMachine = parseMailFilter(text)
			m.clampCursor()
			return m, nil
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m Model) act(f func() error) (tea.Model, tea.Cmd) {
	if !m.person {
		return m.fail(fmt.Errorf("browse only: this client is inside a pane (SPEC §12)")), nil
	}
	return m, func() tea.Msg { return actionMsg{err: f()} }
}
