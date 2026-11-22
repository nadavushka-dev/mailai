// Command mailai is the CLI, the daemon and every other entry point in one
// binary (SPEC §4, §12). Every command takes --json.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/nadavushka-dev/mailai/internal/buildinfo"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/config"
)

const usage = `mailai: mail for coding agents and the people who run them

usage: mailai <command> [flags]

mail
  send <to> [text]        leave a message; text from stdin when omitted
  read                    read your mailbox inside the fence
  reply <mid> [text]      reply in a thread
  ask <to> <text>         send and wait for the reply; prints the reply alone
  ask-human <text> -o A   put a question with fixed answers in the person's Inbox
  unread                  how much mail waits: count and senders
  notice                  the one-line notice an agent is given, if any (marks it announced)
  queue <to> <text>       typed as the agent's next prompt when it comes to rest
  agents                  what the adapter and hooks know about live agents
  stash put|get|list      keep a file for the session so a reader finds it later
  token new|ls|revoke     agent tokens, for an agent with no pane (person only)

inbox (the person)
  tui, or plain mailai    the Inbox and Mail views in the terminal
  web [--port N]          the same Inbox in a browser, on 127.0.0.1; prints a one-time link
  inbox                   everything waiting on you
  answer <item> <text>    answer an item; closes it
  dismiss <item>          close an item without an answer
  grant <agent> <rights>  make a supervisor: read,respond over --scope (person; holders delegate narrower)

harnesses
  hook <harness> <event>  what an installed hook runs (payload on stdin)
  integration install|uninstall|status [harness...]
  --skill                 the short guide an agent reads to use mailai

daemon
  daemon run              run the daemon in the foreground
  service install|uninstall|status
                          keep the daemon running at login and after a crash (launchd / systemd user unit)
  ping                    is the daemon up, and this machine's link in the star
  bridge                  what a spoke runs over ssh to reach this machine's daemon
  whoami                  how the daemon places this caller
  version

Every command takes --json. MAILAI_SOCKET points at a daemon elsewhere.
MAILAI_TOKEN makes this process the agent the token was issued to.
MAILAI_ADAPTER=herdr|none overrides the adapter the daemon picks.

Several machines (a star): one inbox machine, where the person answers.
On every other machine, config.toml has
  [fleet]
  inbox = "<ssh target of the inbox machine>"
and its agents reach the person as human, other machines' agents as name@machine.
`

func main() {
	if len(os.Args) < 2 {
		// Plain `mailai` is the TUI, like `enigma`.
		if err := cmdTUI(nil); err != nil {
			exit(err)
		}
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "daemon":
		err = cmdDaemon(args)
	case "service":
		err = cmdService(args)
	case "send":
		err = cmdSend(args)
	case "read":
		err = cmdRead(args)
	case "reply":
		err = cmdReply(args)
	case "ask":
		err = cmdAsk(args)
	case "ask-human":
		err = cmdAskHuman(args)
	case "inbox":
		err = cmdInbox(args)
	case "answer":
		err = cmdAnswer(args)
	case "dismiss":
		err = cmdDismiss(args)
	case "unread":
		err = cmdUnread(args)
	case "notice":
		err = cmdNotice(args)
	case "stash":
		err = cmdStash(args)
	case "agents":
		err = cmdAgents(args)
	case "queue":
		err = cmdQueue(args)
	case "token":
		err = cmdToken(args)
	case "grant":
		err = cmdGrant(args)
	case "hook":
		err = cmdHook(args)
	case "integration":
		err = cmdIntegration(args)
	case "--skill", "skill":
		err = cmdSkill(args)
	case "ping":
		err = cmdPing(args)
	case "bridge":
		err = cmdBridge(args)
	case "whoami":
		err = cmdWhoami(args)
	case "tui":
		err = cmdTUI(args)
	case "web":
		err = cmdWeb(args)
	case "version", "--version", "-V":
		fmt.Println(buildinfo.Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mailai: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		exit(err)
	}
}

// exit reports an error the way scripts and agents can branch on: the
// protocol code on stderr as JSON when --json, else one line; exit 1.
// An ask that got no reply exits 2 so a script can tell "no" from "nothing".
func exit(err error) {
	if jsonOut {
		json.NewEncoder(os.Stderr).Encode(map[string]string{"error": string(client.Code(err)), "hint": err.Error()})
	} else {
		fmt.Fprintln(os.Stderr, "mailai:", err)
	}
	if client.Code(err) == "timeout" {
		os.Exit(2)
	}
	os.Exit(1)
}

var jsonOut bool

func connect() (*client.Client, error) {
	p, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return client.Dial(p.Socket(), os.Getenv("MAILAI_TOKEN"))
}

func printJSON(v any) error { return writeJSON(os.Stdout, v) }

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
