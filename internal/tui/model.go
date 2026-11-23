package tui

import (
	"fmt"
	"path/filepath"
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
	recent  []store.Item // closed in the last hour, newest first (§7a)
	threads []daemon.ThreadRow
	cursor  int
	showAll bool

	// open is the thread being read, if any.
	open    []store.Message
	openID  string
	openTop int
	// pendingG is a first g waiting for the second (gg: to the top).
	pendingG bool
	// openItems are the open thread's items, for its one-line notes.
	openItems []store.Item

	// input is the reply or answer being typed; mode says for what.
	input   textinput.Model
	mode    inputMode
	target  store.Item
	replyTo store.Message
	// joinTo are the agents of a thread between agents the person is
	// joining; joinPick 0 writes to all of them, i to joinTo[i-1] only.
	joinTo   []store.Address
	joinPick int

	status string
	errAt  time.Time
	now    func() time.Time

	// Mail filters (SPEC §12): unread, principal, session, machine.
	filterUnread  bool
	filterWho     string
	filterSession string
	filterMachine string
	pendingReply  bool
	// scope narrows Mail to the person's own threads or to those between
	// agents (D13).
	scope mailScope
	// opener shows a file with the system viewer; systemOpen by default.
	opener func(path string) error

	// offline is set while the daemon is away (a restart): the TUI keeps
	// what it shows and redials, redials being the tries so far. gen counts
	// connections, so the end of an old one's events is not a new loss.
	offline bool
	redials int
	gen     int
}

type mailScope int

const (
	scopeAll mailScope = iota
	scopeMe
	scopeAgents
)

// toMe says a thread started with the person: from or to human. One the
// person stepped into later stays between agents.
func toMe(t daemon.ThreadRow) bool {
	return localName(t.From) == string(store.Human) || localName(t.To) == string(store.Human)
}

type inputMode int

const (
	modeNone inputMode = iota
	modeAnswer
	modeReply
	modeDenyMessage
	modeFilter
	// modeFile waits for the number of the file to open.
	modeFile
)

// Messages between the program and the model.
type (
	refreshMsg struct {
		items   []store.Item
		recent  []store.Item
		threads []daemon.ThreadRow
		err     error
	}
	threadMsg struct {
		id    string
		msgs  []store.Message
		items []store.Item
		err   error
	}
	eventMsg struct {
		f   proto.Frame
		ch  <-chan proto.Frame
		gen int
	}
	// lostMsg: the events of connection gen ended.
	lostMsg struct{ gen int }
	// redialMsg: time to try the daemon again.
	redialMsg struct{}
	// redialedMsg is how a try went, with who the daemon says this is.
	redialedMsg struct {
		who daemon.WhoamiResult
		err error
	}
	actionMsg struct{ err error }
	fileMsg   struct {
		path string
		err  error
	}
	tickMsg time.Time
)

// New makes a model over a backend.
func New(b Backend) Model {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = store.MaxText
	m := Model{b: b, input: in, now: time.Now, width: 100, height: 30, opener: systemOpen}
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
		recent, err := b.Recent()
		if err != nil {
			return refreshMsg{err: err}
		}
		threads, err := b.Threads()
		return refreshMsg{items: items, recent: recent, threads: threads, err: err}
	}
}

func (m Model) listen() tea.Cmd {
	ch := m.b.Events()
	if ch == nil {
		return nil
	}
	return listenOn(ch, m.gen)
}

// listenOn waits for the next event; the stream ending is the connection
// ending.
func listenOn(ch <-chan proto.Frame, gen int) tea.Cmd {
	return func() tea.Msg {
		f, ok := <-ch
		if !ok {
			return lostMsg{gen}
		}
		return eventMsg{f: f, ch: ch, gen: gen}
	}
}

// redialDelay is the wait before try n (from 0): short at first, since a
// restart takes a moment, then every 3 s until the daemon is back.
func redialDelay(n int) time.Duration {
	steps := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	if n < len(steps) {
		return steps[n]
	}
	return 3 * time.Second
}

func redialAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return redialMsg{} })
}

// redial connects again and asks who this is: the answer may differ.
func (m Model) redial() tea.Cmd {
	b := m.b
	return func() tea.Msg {
		if err := b.Reconnect(); err != nil {
			return redialedMsg{err: err}
		}
		w, err := b.Whoami()
		return redialedMsg{who: w, err: err}
	}
}

func (m Model) loadThread(id string) tea.Cmd {
	b := m.b
	return func() tea.Msg {
		msgs, err := b.Thread(id)
		if err != nil {
			return threadMsg{id: id, err: err}
		}
		items, err := b.ThreadItems(id)
		return threadMsg{id: id, msgs: msgs, items: items, err: err}
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
		m.items, m.recent, m.threads = msg.items, msg.recent, msg.threads
		m.clampCursor()
		return m, nil
	case threadMsg:
		if msg.err != nil {
			return m.fail(msg.err), nil
		}
		m.open, m.openID, m.openTop, m.openItems = msg.msgs, msg.id, 0, msg.items
		mark := m.markRead(msg.msgs)
		if m.pendingReply {
			m.pendingReply = false
			mm, cmd := m.startReply()
			return mm, tea.Batch(cmd, mark)
		}
		return m, mark
	case eventMsg:
		// Any event may change a list; fetch, then keep listening.
		return m, tea.Batch(m.refresh(), listenOn(msg.ch, msg.gen))
	case lostMsg:
		if msg.gen != m.gen || m.offline {
			return m, nil
		}
		m.offline, m.redials, m.status = true, 0, ""
		return m, redialAfter(redialDelay(0))
	case redialMsg:
		if !m.offline {
			return m, nil
		}
		return m, m.redial()
	case redialedMsg:
		if !m.offline {
			return m, nil
		}
		if msg.err != nil {
			m.redials++
			return m, redialAfter(redialDelay(m.redials))
		}
		// Back: do again what starting does.
		m.offline = false
		m.gen++
		m.who, m.person = msg.who, msg.who.Proof == store.ProofOutsidePane
		m = m.say("reconnected")
		cmds := []tea.Cmd{m.refresh(), m.listen()}
		if m.openID != "" {
			cmds = append(cmds, m.loadThread(m.openID))
		}
		return m, tea.Batch(cmds...)
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
	case fileMsg:
		if msg.err != nil {
			return m.say("no viewer here (" + msg.err.Error() + "); the file is at " + msg.path), nil
		}
		return m.say("opened " + filepath.Base(msg.path)), nil
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
		if (m.scope == scopeMe && !toMe(t)) || (m.scope == scopeAgents && toMe(t)) {
			continue
		}
		if m.filterUnread && t.Unread == 0 {
			continue
		}
		if m.filterWho != "" && !addrMatch(t.From, t.To, t.LastFrom, m.filterWho) && !peopleMatch(t.People, m.filterWho) {
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

func peopleMatch(people []store.Address, who string) bool {
	for _, a := range people {
		if strings.EqualFold(localName(a), who) {
			return true
		}
	}
	return false
}

// markRead marks the person's own unread mail in an opened thread read;
// an agent's mail is its own to read, and a browse-only client marks
// nothing.
func (m Model) markRead(msgs []store.Message) tea.Cmd {
	if !m.person {
		return nil
	}
	var ids []uint64
	for _, x := range msgs {
		if x.To == store.Human && x.ReadAt == 0 {
			ids = append(ids, x.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	b := m.b
	return func() tea.Msg {
		if err := b.MarkRead(ids); err != nil {
			return refreshMsg{err: err}
		}
		return m.refresh()()
	}
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
	// gg: a g waits for the next key; anything else cancels it.
	pendingG := m.pendingG
	m.pendingG = false
	switch k.String() {
	case "g":
		if !pendingG {
			m.pendingG = true
			return m, nil
		}
		if m.open != nil {
			m.openTop = 0
		} else {
			m.cursor = 0
		}
		return m, nil
	case "G":
		if m.open != nil {
			m.openTop = m.maxOpenTop()
		} else if n := m.rows(); n > 0 {
			m.cursor = n - 1
		}
		return m, nil
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
			if m.openTop < m.maxOpenTop() {
				m.openTop++
			}
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
	case "v":
		return m.startOpen()
	case "t":
		if m.view == ViewMail && m.open == nil {
			m.scope = (m.scope + 1) % 3
			m.cursor = 0
		}
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
			if m.offline {
				return m.fail(ErrNotConnected), nil
			}
			m.target, m.mode = it, modeDenyMessage
			m.input.Placeholder = "reason shown to the agent (enter for none)"
			m.input.SetValue("")
			return m, m.input.Focus()
		}
		// Maybe answered in the session (§7a): d dismisses it, like x.
		if it, ok := m.current(); ok && it.MaybeInSessionAt != 0 && m.view == ViewInbox {
			return m.act(func() error { return m.b.Dismiss(it.ID) })
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
	if m.offline {
		return m.fail(ErrNotConnected), nil
	}
	if !m.person {
		return m.fail(m.browseOnly()), nil
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
		m.replyTo, m.mode, m.joinTo, m.joinPick = root, modeReply, nil, 0
		m.input.Placeholder = "reply to " + string(root.From)
		if first := m.open[0]; first.From != store.Human && first.To != store.Human {
			// Between agents: the person joins, by default writing to
			// every agent in it (D13).
			seen := map[store.Address]bool{}
			for _, x := range m.open {
				for _, a := range []store.Address{x.From, x.To} {
					if a != "" && a != store.Human && !seen[a] {
						seen[a] = true
						m.joinTo = append(m.joinTo, a)
					}
				}
			}
			m.replyTo = m.open[len(m.open)-1]
			m.input.Placeholder = m.joinPlaceholder()
		}
	default:
		return m, nil
	}
	m.input.SetValue("")
	return m, m.input.Focus()
}

// joinTargets are who a join writes to with the current pick.
func (m Model) joinTargets() []store.Address {
	if m.joinPick == 0 || m.joinPick > len(m.joinTo) {
		return m.joinTo
	}
	return m.joinTo[m.joinPick-1 : m.joinPick]
}

func (m Model) joinPlaceholder() string {
	names := make([]string, 0, len(m.joinTo))
	for _, a := range m.joinTargets() {
		names = append(names, string(a))
	}
	hint := ""
	if len(m.joinTo) > 1 {
		hint = "  (tab: who)"
	}
	return "join thread as you → " + strings.Join(names, " + ") + hint
}

// files are what can be opened from here: every attachment of the open
// thread, in order, or the current Inbox item's.
func (m Model) files() []store.Attachment {
	if m.open != nil {
		var out []store.Attachment
		for _, x := range m.open {
			out = append(out, x.Attachments...)
		}
		return out
	}
	if m.view == ViewInbox {
		if it, ok := m.current(); ok {
			return it.Attachments
		}
	}
	return nil
}

// startOpen opens the only file at once, or asks which by number.
func (m Model) startOpen() (tea.Model, tea.Cmd) {
	fs := m.files()
	switch len(fs) {
	case 0:
		return m.say("no files here"), nil
	case 1:
		return m.openFile(fs[0])
	}
	m.mode = modeFile
	n := len(fs)
	if n > 9 {
		n = 9
	}
	m.input.SetValue("")
	m.input.Placeholder = fmt.Sprintf("open which file? 1-%d  (esc cancels)", n)
	return m, nil
}

func (m Model) openFile(a store.Attachment) (tea.Model, tea.Cmd) {
	if a.Missing {
		return m.say("gone: " + a.Path), nil
	}
	open, path := m.opener, a.Path
	return m, func() tea.Msg { return fileMsg{path: path, err: open(path)} }
}

// say shows a line in the status bar for a few seconds.
func (m Model) say(s string) Model {
	m.status = s
	m.errAt = m.now()
	return m
}

func (m Model) updateInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeFile {
		m.mode = modeNone
		if s := k.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			if fs := m.files(); int(s[0]-'1') < len(fs) {
				return m.openFile(fs[s[0]-'1'])
			}
		}
		return m, nil
	}
	switch k.String() {
	case "tab":
		if m.mode == modeReply && len(m.joinTo) > 1 {
			m.joinPick = (m.joinPick + 1) % (len(m.joinTo) + 1)
			m.input.Placeholder = m.joinPlaceholder()
		}
		return m, nil
	case "esc":
		m.mode = modeNone
		m.input.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		mode, target, replyTo, join := m.mode, m.target, m.replyTo, m.joinTargets()
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
			if len(join) > 0 {
				return m.act(func() error {
					for _, to := range join {
						if err := m.b.Reply(replyTo.MID, to, text); err != nil {
							return err
						}
					}
					return nil
				})
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
	if m.offline {
		return m.fail(ErrNotConnected), nil
	}
	if !m.person {
		return m.fail(m.browseOnly()), nil
	}
	return m, func() tea.Msg { return actionMsg{err: f()} }
}

// browseOnly is why this client cannot act: the daemon's reason (§9).
func (m Model) browseOnly() error {
	if m.who.NotPerson != "" {
		return fmt.Errorf("browse only: %s", m.who.NotPerson)
	}
	return fmt.Errorf("browse only: this client is inside a pane, so its replies would not be the person's (SPEC §12)")
}
