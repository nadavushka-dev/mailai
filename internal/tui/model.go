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
}

type inputMode int

const (
	modeNone inputMode = iota
	modeAnswer
	modeReply
	modeDenyMessage
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
	return len(m.threads)
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
		m.view, m.cursor = ViewInbox, 0
		return m, nil
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
		if m.cursor < len(m.threads) {
			return m, m.loadThread(m.threads[m.cursor].ThreadID)
		}
		return m, nil
	case "r":
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
