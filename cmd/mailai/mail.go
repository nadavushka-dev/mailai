package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/render"
	"github.com/nadavushka-dev/mailai/internal/store"
)

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("mailai "+name, flag.ExitOnError)
	fs.BoolVar(&jsonOut, "json", false, "machine-readable output")
	return fs
}

// parse accepts flags anywhere among positionals ("mailai send worker
// --subject s text"), which is how people and agents write them. Go's flag
// package stops at the first positional, so we resume after each one. A
// bare "--" ends flag parsing as usual.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		rest := fs.Args()
		if len(rest) == 0 {
			return pos
		}
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...)
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// textArg takes the text from the remaining args, or stdin when there are none.
func textArg(rest []string) (string, error) {
	if len(rest) > 0 {
		return strings.Join(rest, " "), nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func cmdSend(args []string) error {
	fs := newFlags("send")
	from := fs.String("from", "", "sender address; who you are by default")
	subject := fs.String("subject", "", "one-line subject (at most 120 characters)")
	session := fs.String("session", "", "session, for a notice with no recipient")
	replyTo := fs.String("reply-to", "", "join this message's thread")
	var attach stringList
	fs.Var(&attach, "attach", "absolute path to attach (repeatable)")
	rest := parse(fs, args)
	var to string
	if len(rest) > 0 && *session == "" {
		to, rest = rest[0], rest[1:]
	}
	text, err := textArg(rest)
	if err != nil {
		return err
	}
	var atts []store.Attachment
	for _, p := range attach {
		atts = append(atts, store.Attachment{Kind: "file", Path: p})
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var res daemon.SendResult
	if err := c.Call("send", daemon.SendArgs{From: store.Address(*from), To: store.Address(to), Session: *session,
		Subject: *subject, Text: text, ReplyTo: *replyTo, Attachments: atts}, &res); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(res.Message)
	}
	if res.Queued {
		fmt.Printf("queued %s for %s: the link to the inbox machine is down; it goes when the link is back\n", res.Message.MID, res.Message.To)
		return nil
	}
	fmt.Printf("sent #%d (%s) to %s\n", res.Message.ID, res.Message.MID, orNotice(res.Message))
	return nil
}

func orNotice(m store.Message) string {
	if m.To == "" {
		return "session " + m.Session + " (notice)"
	}
	return string(m.To)
}

func cmdReply(args []string) error {
	fs := newFlags("reply")
	from := fs.String("from", "", "sender address; who you are by default")
	to := fs.String("to", "", "recipient; the thread's other party by default")
	rest := parse(fs, args)
	if len(rest) == 0 {
		return fmt.Errorf("reply needs the message id to answer")
	}
	mid, rest := rest[0], rest[1:]
	text, err := textArg(rest)
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	recipient := store.Address(*to)
	if recipient == "" {
		// Find the message so the reply goes back to whoever sent it.
		var rr daemon.ReadResult
		if err := c.Call("read", daemon.ReadArgs{Thread: mid}, &rr); err != nil {
			return err
		}
		for _, m := range rr.Messages {
			if m.MID == mid {
				recipient = m.From
			}
		}
		if recipient == "" {
			return fmt.Errorf("no message %s is held; pass --to", mid)
		}
	}
	var res daemon.SendResult
	if err := c.Call("send", daemon.SendArgs{From: store.Address(*from), To: recipient, Text: text, ReplyTo: mid}, &res); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(res.Message)
	}
	if res.Queued {
		fmt.Printf("queued reply %s for %s: the link to the inbox machine is down; it goes when the link is back\n", res.Message.MID, recipient)
		return nil
	}
	fmt.Printf("replied #%d (%s) to %s in thread %s\n", res.Message.ID, res.Message.MID, recipient, res.Message.ThreadID)
	return nil
}

func cmdRead(args []string) error {
	fs := newFlags("read")
	mailbox := fs.String("w", "", "mailbox to read; yours by default")
	unread := fs.Bool("unread", false, "only unread messages")
	peek := fs.Bool("peek", false, "read without marking read")
	summary := fs.Bool("summary", false, "one line per message, no bodies")
	thread := fs.String("thread", "", "every message in this thread")
	parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var rr daemon.ReadResult
	if err := c.Call("read", daemon.ReadArgs{Mailbox: store.Address(*mailbox), Unread: *unread, Peek: *peek || *summary, Thread: *thread}, &rr); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(rr)
	}
	if rr.PeekForced {
		fmt.Fprintln(os.Stderr, "mailai: peek only: a pane cannot mark the person's mail read")
	}
	if len(rr.Messages) == 0 {
		fmt.Println("no messages")
		return nil
	}
	if *summary {
		fmt.Print(render.Summary(rr.Messages))
		return nil
	}
	for _, m := range rr.Messages {
		fmt.Print(render.Fence(m))
	}
	return nil
}

func cmdAsk(args []string) error {
	fs := newFlags("ask")
	from := fs.String("from", "", "asker address; who you are by default")
	subject := fs.String("subject", "", "one-line subject")
	timeout := fs.Duration("timeout", 0, "give up after this long (exit 2); forever by default")
	verbose := fs.Bool("verbose", false, "print the reply inside the fence with its header")
	rest := parse(fs, args)
	if len(rest) == 0 {
		return fmt.Errorf("ask needs a recipient")
	}
	to, rest := rest[0], rest[1:]
	text, err := textArg(rest)
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var res daemon.AskResult
	if err := c.Call("ask", daemon.AskArgs{From: store.Address(*from), To: store.Address(to), Subject: *subject, Text: text, TimeoutMS: timeout.Milliseconds()}, &res); err != nil {
		return err
	}
	switch {
	case jsonOut:
		return printJSON(res)
	case *verbose:
		fmt.Print(render.Fence(res.Reply))
	default:
		// The reply alone, so a script branches on it and the model reads
		// nothing it did not ask for (D12).
		fmt.Println(strings.TrimRight(res.Reply.Text, "\n"))
	}
	return nil
}

func cmdUnread(args []string) error {
	fs := newFlags("unread")
	mailbox := fs.String("w", "", "mailbox; yours by default")
	parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var ur daemon.UnreadResult
	if err := c.Call("unread", daemon.UnreadArgs{Mailbox: store.Address(*mailbox)}, &ur); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(ur)
	}
	if ur.Count == 0 {
		fmt.Println("0 unread")
		return nil
	}
	fmt.Printf("%d unread from %s\n", ur.Count, joinAddrs(ur.From))
	return nil
}

// cmdNotice prints the one-line notice for the caller's mailbox, or nothing.
// Hooks run it and paste its output into an agent's context (SPEC §7).
func cmdNotice(args []string) error {
	fs := newFlags("notice")
	mailbox := fs.String("w", "", "mailbox; yours by default")
	parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	flags := ""
	if *mailbox != "" {
		flags = "-w " + *mailbox
	}
	var nr daemon.NoticeResult
	if err := c.Call("notice", daemon.NoticeArgs{Mailbox: store.Address(*mailbox), Flags: flags}, &nr); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(nr)
	}
	// Once per message: a line only when something is new (D12).
	if nr.New && nr.Line != "" {
		fmt.Println(nr.Line)
	}
	return nil
}

func cmdPing(args []string) error {
	parse(newFlags("ping"), args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r daemon.PingResult
	if err := c.Call("ping", nil, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Println("pong", r.Pong)
	switch {
	case r.Inbox != "":
		fmt.Printf("machine %s, spoke: link to inbox machine %s is %s; outbox: %d waiting to send\n", r.Machine, r.Inbox, r.Link, r.Outbox)
	case r.Machine != "":
		links := "none"
		if len(r.Links) > 0 {
			links = strings.Join(r.Links, ", ")
		}
		fmt.Printf("machine %s, inbox machine: spokes linked: %s\n", r.Machine, links)
	}
	return nil
}

func cmdWhoami(args []string) error {
	parse(newFlags("whoami"), args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var w daemon.WhoamiResult
	if err := c.Call("whoami", nil, &w); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(w)
	}
	if w.Principal == "" {
		fmt.Printf("nobody (%s: no terminal and no token, so not the person; pass --from)\n", w.Proof)
		return nil
	}
	fmt.Printf("%s (%s)\n", w.Principal, w.Proof)
	return nil
}

func joinAddrs(as []store.Address) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = string(a)
	}
	return strings.Join(s, ", ")
}

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }
