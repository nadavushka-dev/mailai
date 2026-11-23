package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// cmdAskHuman puts a question with fixed answers in the person's Inbox and
// prints the answer alone (SPEC §7). Exit 2 when there is no answer yet.
func cmdAskHuman(args []string) error {
	fs := newFlags("ask-human")
	from := fs.String("from", "", "asker address; who you are by default")
	subject := fs.String("subject", "", "one-line subject")
	var opts stringList
	fs.Var(&opts, "o", "an allowed answer (repeatable); free text when none")
	noWait := fs.Bool("no-wait", false, "return at once; the answer arrives as mail, or come back with --wait-for")
	waitFor := fs.Uint64("wait-for", 0, "wait for the answer to an earlier ask-human, by item id")
	timeout := fs.Duration("timeout", 0, "give up after this long (exit 2); forever by default")
	var attach stringList
	fs.Var(&attach, "attach", "a file for the person to look at (repeatable); a reference, not a copy")
	replaces := fs.Uint64("replaces", 0, "withdraw your earlier open question #N as this one opens")
	rest := parse(fs, args)
	atts, err := attachments(attach)
	if err != nil {
		return err
	}
	text := ""
	if *waitFor == 0 {
		var err error
		if text, err = textArg(rest); err != nil {
			return err
		}
		text = strings.TrimSpace(text)
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r daemon.AskHumanResult
	err = c.Call("ask-human", daemon.AskHumanArgs{From: store.Address(*from), Subject: *subject, Text: text, Options: opts,
		NoWait: *noWait, WaitFor: *waitFor, TimeoutMS: timeout.Milliseconds(), Attachments: atts, Replaces: *replaces}, &r)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	if r.Reply == nil && r.Queued {
		fmt.Printf("queued as #%d: the link to the inbox machine is down; the question reaches the Inbox when it is back. Come back with: mailai ask-human --wait-for %d\n", r.Item.ID, r.Item.ID)
		return nil
	}
	if r.Reply == nil {
		if *replaces != 0 {
			fmt.Printf("asked as item #%d (replaces #%d, now withdrawn); come back with: mailai ask-human --wait-for %d\n", r.Item.ID, *replaces, r.Item.ID)
			return nil
		}
		fmt.Printf("asked as item #%d; come back with: mailai ask-human --wait-for %d\n", r.Item.ID, r.Item.ID)
		return nil
	}
	fmt.Println(strings.TrimRight(r.Reply.Text, "\n"))
	return nil
}

func cmdInbox(args []string) error {
	fs := newFlags("inbox")
	all := fs.Bool("all", false, "include closed items")
	closed := fs.Bool("closed", false, "list what closed in the last hour, newest first, and how")
	parse(fs, args)
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r daemon.InboxResult
	if err := c.Call("inbox", daemon.InboxArgs{All: *all, Closed: *closed}, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	now := time.Now()
	if *closed {
		if len(r.Items) == 0 {
			fmt.Println("nothing closed in the last hour")
		}
		for _, it := range r.Items {
			fmt.Printf("#%d  %-9s %-14s %s  (%s %s ago)\n", it.ID, it.Kind, it.From, firstLine(it.Question), daemon.ClosedHow(it), ago(now, it.ClosedAt))
		}
		return nil
	}
	if len(r.Items) == 0 {
		if r.NotPerson != "" {
			fmt.Println(r.NotPerson)
			return nil
		}
		fmt.Println("inbox empty: nothing waits on you")
		return nil
	}
	for _, it := range r.Items {
		state := "waiting " + ago(now, it.OpenedAt)
		if it.MaybeInSessionAt != 0 {
			state = daemon.MaybeBadge + ", " + state
		}
		if it.ClosedAt != 0 {
			state = it.ClosedBy
		}
		opt := ""
		if len(it.Options) > 0 {
			opt = " [" + strings.Join(it.Options, "|") + "]"
		}
		fmt.Printf("#%d  %-9s %-14s %s%s  (%s)\n", it.ID, it.Kind, it.From, firstLine(it.Question), opt, state)
	}
	return nil
}

func cmdAnswer(args []string) error {
	fs := newFlags("answer")
	rest := parse(fs, args)
	if len(rest) < 1 {
		return fmt.Errorf("usage: mailai answer <item> <text|choice>")
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(rest[0], "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("item id must be a number, got %q", rest[0])
	}
	text, err := textArg(rest[1:])
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r map[string]any
	if err := c.Call("answer", daemon.AnswerArgs{Item: id, Text: strings.TrimSpace(text)}, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Printf("answered #%d\n", id)
	return nil
}

func cmdDismiss(args []string) error {
	fs := newFlags("dismiss")
	rest := parse(fs, args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: mailai dismiss <item>")
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(rest[0], "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("item id must be a number, got %q", rest[0])
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r map[string]any
	if err := c.Call("dismiss", daemon.DismissArgs{Item: id}, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Printf("dismissed #%d\n", id)
	return nil
}

// cmdWithdraw: an agent takes back its own open question (SPEC §7a).
func cmdWithdraw(args []string) error {
	fs := newFlags("withdraw")
	rest := parse(fs, args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: mailai withdraw <item>")
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(rest[0], "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("item id must be a number, got %q", rest[0])
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	var r daemon.WithdrawResult
	if err := c.Call("withdraw", daemon.WithdrawArgs{Item: id}, &r); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(r)
	}
	fmt.Printf("withdrew #%d\n", id)
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + "…"
	}
	if len(s) > 70 {
		s = s[:69] + "…"
	}
	return s
}

func ago(now time.Time, ns int64) string {
	d := now.Sub(time.Unix(0, ns)).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
