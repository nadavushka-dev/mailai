# mailai — design spec

Status: **agreed**, 2026-10-29. Sections 1–3 record what was decided in
discussion. Sections 4 onward are the technical design. Section 15 records
how the open questions were resolved.

Prior art: TUIOS's agent mail (`reference/tuios/`, MIT). Its design is the
starting point; what we keep, drop and change is said in each section.

---

## 1. Problem and purpose

Coding agents run in many panes, on many machines, under several humans and
supervising agents. Each one, when it needs an answer, blocks in its own pane,
and the person has to find it. Agents that need to tell each other something
have no channel except typing into each other's prompts, which is both
fragile and a prompt-injection path.

mailai is a mail system for agents and the people who run them: every agent
and every person has an inbox, messages are stored by a daemon, and one
Inbox view lists everything waiting for an answer across every session and
every machine. The person, or a supervisor agent given the right, answers
from that one place.

Two advantages, both design goals (D12):
- **UX:** the person and supervisors answer everything from one place, on
  any machine, instead of hunting through panes.
- **Tokens:** agents stop polling panes and re-reading scrollback. A
  question costs one line in the asker's context and zero model turns while
  it waits. A reply is the sentence, not 120 lines of terminal.

**What it is not:**
- Not email. No SMTP, no external addresses. It is local to your machines.
- Not a terminal multiplexer. It runs beside Herdr (or any other, or none)
  and never owns panes.
- Not part of Enigma or any other tool. Full separation (D2): it talks to
  other machines only through the standard `ssh` command, with its own
  daemon and its own fleet.

## 2. Decisions so far

| # | Topic | Decision |
|---|---|---|
| D1 | Shape | A daemon per machine owns the store. CLI, TUI, hooks and MCP are clients of its unix socket. |
| D2 | Separation | Standalone, POSIX style: one tool, one job. No code or runtime dependency on Enigma, Herdr or TUIOS. Herdr is reached through its CLI as an optional adapter. |
| D3 | Delivery | Mail never types into an agent. An agent learns of mail through a one-line notice holding only a count and a read command, and reads the body itself. Bodies print inside an untrusted fence. (TUIOS, kept.) |
| D4 | Identity | The daemon decides who sent a message from the connection, never from the request. `from` is a claim shown as one. (TUIOS, kept.) |
| D5 | Answering | The person answers from the Inbox. A **supervisor** is an agent given a `respond` grant over a scope; its answers are stored and shown as `verified_supervisor:<name>`, distinct from `verified_human`. No pane can grant itself. (New.) |
| D6 | Multi-machine | **v1 is a star (§13a, decided 2026-11-19):** a daemon on every machine, one of them the *inbox machine*, every other machine linked to it over ssh. The person answers only there. The full mesh below is deferred. mailai machines form their own fleet, linked over ssh, every machine equal, messages relayed through a third machine when two can't reach each other, offline catch-up on reconnect. The pattern is Enigma's. Code from Enigma (link, bridge, proto framing, fleet records, service install, machine add, upgrade) is copied into this repo verbatim where it fits and adapted where it doesn't. Copied, never imported: the two tools share no module and evolve apart. |
| D7 | Interface | A CLI for everything, usable by agents and scripts, plus a TUI Inbox. Every command takes `--json`. |
| D8 | Harnesses | Claude Code and Codex first, through their hook systems; Herdr's agent state when inside Herdr. Others later. |
| D9 | Language | Go. Dependencies: stdlib, bubbletea/bubbles/lipgloss, BurntSushi/toml, fsnotify, modernc.org/sqlite. Anything else needs approval. |
| D10 | Dev tools | tuxedo, tuicr and herdr are for our development workflow only, never part of mailai. |
| D11 | Web UI | In addition to the TUI, a browser Inbox served by the daemon on loopback only, off by default (`mailai web`). It shows exactly what the Inbox shows, mail and attention items, and nothing else: no pane output, no logs, no dashboards. Remote access is the user's own tunnel. |
| D13 | Two views | The **Human Inbox** lists only what waits on the person: open asks to `human`, permission requests nobody answered, errored agents. **Mail** is everything: every thread, agent-to-agent traffic, supervisor answers, plain mail to `human`. A supervisor's answer closes the Inbox item and stays in Mail. The Inbox lists only what went through mailai; a pane Herdr sees as `blocked` is Herdr's business. |
| D14 | Harness-agnostic | The core knows no harness: a notice is a line and a command, state is five words. Hook glue is a declarative TOML manifest per harness (config path, how to add a hook entry, event→state map, whether a permission request can be held, where it reads skills). Claude Code and Codex ship bundled; a user manifest in `~/.config/mailai/harnesses/` adds any other, no rebuild. Without a manifest an agent still works: state and wake from the adapter, `mailai read` from its skill text. |
| D12 | Token economy | Mail exists for two reasons: one place to answer everything, and fewer tokens. Everything mailai puts into an agent's context is bounded and enumerable: one notice line per message, subject ≤ 120 chars, bodies only on demand. Waiting happens in a blocked process, never in a model turn. `mailai status` reports the context cost per agent. |

## 3. Vocabulary

- **Machine:** a computer running the mailai daemon. Has a random `machine_id`.
- **Fleet:** all your machines; exactly one.
- **Principal:** anyone who can send or receive: an **agent** (a pane, or a
  headless process), the **person** (`human`), or a **supervisor** (an agent
  with a `respond` grant). Addressed as `name@machine`; `@machine` is
  omitted when local.
- **Mailbox:** a principal's inbox, held by the daemon on the principal's
  machine.
- **Message:** one immutable record (§6). A **notice** is a message with no
  recipient, posted to a session.
- **Thread:** the set of messages sharing a `thread_id`, which is the id of
  the message that started it.
- **Ask:** a message whose sender waits for a reply (`ask`), or a question
  with fixed answers put to the person (`ask-human`).
- **Attention item:** something waiting for an answer: an unread message to
  the person, an open ask-human, an agent blocked on an approval or
  question, an errored agent. The **Inbox** is the list of attention items.
- **Grant:** what a principal may do through mailai: `read`, `write`,
  `respond`, `admin` (§10).
- **Session:** a grouping of panes, taken from the adapter (a Herdr
  workspace) or declared by the user. Scopes and notices are per session.

---

## 4. Architecture

```
          machine A                                     machine B
 ┌──────────────────────────────┐             ┌──────────────────────────────┐
 │ mailai CLI / TUI / hooks /   │             │ mailai CLI / TUI             │
 │ MCP server                   │             │        │ unix socket         │
 │        │ unix socket (0700)  │  ssh -T B   │ mailai daemon                │
 │ mailai daemon                │────────────▶│  ▲                           │
 │  ├ store (mailboxes, items)  │  "mailai    │  │ unix socket               │
 │  ├ identity (peer pid, nonce)│   bridge"   │ mailai bridge (stdio ⇄ sock) │
 │  ├ delivery (notice, wake)   │◀────────────│                              │
 │  ├ adapters (herdr, hooks)   │  one link,  └──────────────────────────────┘
 │  └ links (fleet, relay)      │  both ways
 └──────────────────────────────┘
```

- One static Go binary, `mailai`, with subcommands. macOS and Linux, amd64
  and arm64.
- **daemon:** one per machine, started by launchd or a systemd user unit.
  `mailai service install` writes and starts it: on macOS the launchd agent
  `~/Library/LaunchAgents/dev.mailai.daemon.plist` (`RunAtLoad`,
  `KeepAlive`), on Linux the systemd user unit
  `~/.config/systemd/user/mailai.service` (`Restart=on-failure`, enabled
  for `default.target`). Either runs `<absolute path of mailai> daemon run`
  at login and again after a crash, with stdout and stderr appended to the
  data dir's `daemon.log`, and carries `MAILAI_HOME`, `MAILAI_SOCKET`,
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME` and `PATH` into the service when they
  are set at install (`PATH`, because launchd and systemd start services
  with a minimal one and the herdr adapter runs `herdr` from it). Installing again replaces the definition and restarts the
  daemon; `service uninstall` stops it and removes the file; `service
  status` reports installed, loaded (launchd: bootstrapped; systemd:
  enabled), running, and the path. Linux linger is not enabled: a user
  unit runs while the user is logged in.
  The daemon owns all state on that machine. Every mailbox lives on the
  machine its principal runs on; other machines hold only what is in flight to it.
- **bridge:** what a peer runs over ssh: copies bytes between ssh's
  stdin/stdout and the local daemon's socket. Nothing listens on the network.
- **clients:** CLI, TUI, hook entrypoints and the MCP server all speak the
  same JSON-lines protocol over the socket (§12).

The daemon is organized as:
- `store`: mailboxes, attention items, grants, sessions; persisted (§11).
- `identity`: places every caller (§9).
- `delivery`: notices and wakes (§7).
- `adapter`: how mailai learns about agents and their state, and types a
  notice at one (§8). `herdr` and `none` in v1.
- `fleet`: machine records, links, relay, catch-up (§13).

## 5. Principals and addressing

An agent principal is created the first time something about a pane reaches
the daemon: a hook fires, the adapter lists it, or a command runs from inside
it. Its name is the adapter's stable id (Herdr pane id `w1:p3`, or the live
agent name when one is set) plus an optional label.

- `human` is the person. One per machine, but the fleet treats every
  machine's `human` as the same person: mail to `human` shows in the Inbox
  on every machine, and a reply from any machine's attached client is the
  person's (§9).
- A headless process (ACP, an app-server, a script) is an agent principal
  known only by its token (§9). It can send, read and ask; it has no state
  and gets no wake. Nothing more in v1.
- A supervisor is an ordinary agent principal with a `respond` grant (§10).
  Agents address it by name like any other.
- Addresses: `reviewer`, `w1:p3`, `reviewer@devbox`, `human`. A selector
  (`session:api`, `group:fan/retry`, `needs:human`, `machine:devbox`)
  addresses several; a send to a selector lists the matches and asks for
  confirmation unless `--yes`.

## 6. Messages

```
id            uint64   daemon-assigned, unique per machine
mid           string   "<machine_id>:<id>", unique in the fleet
kind          mail | notice | ask | ask_human | reply | review
session       string
from          address  as claimed by the sender
from_label    string
to            address  empty for a notice
subject       string   one line, ≤ 120 chars
text          string   ≤ 64 KB
reply_to      mid      zero when it answers nothing
thread_id     mid      resolved at send time; a thread starter carries its own mid
attachments   []ref    {kind, path, media_type, bytes, mtime, stashed, missing, changed}
sent_at       int64    ns, sender's clock
origin        local | link
origin_host   string   claim, shown as one
sender_proof  pane | outside_pane | link | token    (§9)
sender_pane   string   the pane of a `pane` sender, set by the daemon; shown beside the name (§9)
verified      "" | human | supervisor:<name>       set by the daemon only
claimed_human bool                                 (§9)
read_at       int64    zero while unread
seen_at       int64    peek by the recipient
undeliverable bool     resolved at read time: recipient gone
```

Rules, all kept from TUIOS:
- A message is immutable once stored. Reading marks, never consumes.
- `thread_id` is resolved at send time, so a bounded store stays consistent
  when the root ages out. A reply to a forgotten parent roots at the
  parent's id and sets `reply_to_missing`.
- Attachments are references, never bytes. A sender-owned path may be gone
  by read time (`missing`, resolved at read), or modified since it was sent
  (`changed`: size or `mtime` differ, resolved at read); a reader sees the
  file as it is now. `ask-human` takes attachments too, shown on the Inbox
  item: the way an agent shows the person a screenshot, report or log. A path put in the **stash**
  (`mailai stash put`) is daemon-owned and lives until the session ends;
  across machines the stash is copied over the link on `stash get`.
- Seen sits between unread and read. A peek of one's own inbox marks seen,
  so the sender can tell the message was looked at.
- Store bound: per mailbox, 2,000 messages or 16 MB, oldest evicted first,
  read before unread. An unread message is never evicted by eviction alone;
  a mailbox at its unread cap refuses new mail with `mailbox_full`.
- Keeping: a message read more than `[mail] keep_days` ago (7 by default;
  0 keeps it until evicted) is deleted. The daemon sweeps at start and
  hourly. Unread mail is never deleted by age.
- The person deletes by hand: a whole thread, or one message, from the web
  Mail view, the TUI or `mailai delete`. Agents cannot delete. Deleting
  works on this machine's store only, and leaves Inbox items as they are.
  It hides, from the person's views only (decided 2026-11-24, option A):
  the store keeps a per-thread (or, for `--message`, per-message) mark,
  and the person's thread lists, threads, mailbox reads, unread counts and
  filters skip what is marked. The agents keep their mail (their
  mailboxes, `reply <#>`, `read --thread`, grants and peeks) until expiry
  removes it as usual. New mail in a hidden thread stays hidden, unless it
  is to or from the person (on any machine): then the thread shows again.
  Expiry still really deletes, and drops the marks of what it removed.
  Several threads in one go (decided 2026-11-24): the person selects
  them, Gmail-style, read or unread, and deletes the selection after one
  question naming the count; each goes with the per-thread delete, same
  rules. Web: a checkbox on every row of a Mail thread list, shift+click
  for a range, `x` for the focused row, a select-all in the list header
  (every row shown, so the filter counts), and while anything is selected
  the header shows "N selected", Delete and Clear; Esc clears. A selection
  survives live updates and drops threads that leave the list (deleted,
  filtered out, another tab), so Delete never takes what is out of sight.
  TUI: space or `x` marks a row, `D` deletes the marked ones after a `y`
  (with none marked, `D` is the one thread as before), Esc clears. The CLI
  takes several threads (`mailai delete <thread>...`) and keeps
  `mailai delete --read-between-agents` (every read thread between
  agents: no message to or from the person, none unread; `--dry-run` only
  counts), for scripts.
- Every delete, the person's or expiry's, leaves one line in the daemon log
  (decided 2026-11-24): what it did (`hid ... from the person` or
  `deleted ... by expiry`), how many messages in how many threads, by whom
  (`human via web`, `tui`, `cli` or `socket`), and the thread ids, the
  first five then `+N more`. A dry run logs nothing.
- Rate limit: a token bucket per claimed sender (10 messages burst, 1/s),
  with one shared overflow bucket for senders from another machine.

## 7. Delivery (D3)

Mail never types into an agent. The agent is told, once, that mail exists,
and reads it itself.

1. **Hook notice.** With the harness integration installed (§8), the
   agent's own hooks (`SessionStart`, `UserPromptSubmit`, `PostToolUse`)
   ask the daemon and add one line to the agent's context, once per message:

   ```
   You have 2 unread mailai messages from reviewer, human. Read them with: mailai read --unread
   ```

   Count, senders and command only. Never a subject or body.

2. **Wake.** An agent at rest runs no hooks. When mail for it stays unread
   and unannounced for 5 s and the adapter reports it `idle` or `done`, the
   daemon queues the same one line to be typed at its prompt. Never while
   `working` or `blocked`. At most once per message, two wakes of one agent
   at least 30 s apart, the line rebuilt right before typing so a wake whose
   mail was read meanwhile is dropped. The wake carries the authority of
   the connection that sent the mail: a sender that may not `write` to the
   recipient wakes nothing. `[mail] wake = false` turns it off.

   **Listen (decided 2026-11-23).** Typing at the prompt collides with
   anything the person left typed there, so a harness that can hand its
   agent a message takes the wake itself: its add-on keeps
   `mailai listen` open, and the daemon sends the same line, under the same
   rules, down that connection instead of typing it. While a listener is
   open nothing is typed into that agent's pane. A listened agent is woken
   whatever its state (decided 2026-11-24: mail sat 1.5 h behind one long
   turn): unknown to the adapter, idle, `working` or `blocked`; its harness
   decides when the line lands. The other rules hold (5 s, once per
   message, 30 s apart, rebuilt before sending). The daemon
   knows no harness: anything that keeps `mailai listen` open takes the
   wakes, and the add-on only carries lines across. `mailai addon pi [DIR]`
   writes the Pi add-on (it hands each line over as a steer: Pi starts a
   turn when idle, and mid-turn it lands after the current tool calls; one
   wake waits at a time, since it says to read every unread message) with
   the skill beside it; `pi -e DIR`, or DIR under a project's
   `.pi/extensions/`, loads it. It is installed once for every session and
   switched on per agent: `[addon] agents` in the config, glob patterns
   over agent names (`["conductor", "lane-*"]`; nobody when empty, `"*"`
   for everyone). A steer handed to Pi cannot be taken back, and a tool
   call may read the mail before it lands (found by the UbCl watch: 8 of
   about 150 wakes came after their mail was read). So mid-turn the add-on
   holds the line until the turn's tool calls are done, where the steer
   would land, and asks the daemon for it again there (`mailai notice
   --json`): nothing unread sends nothing, and the count is the one now; a
   failed check sends the line as it came (decided 2026-11-25). At session
   start the add-on asks
   `mailai listen --check` (exit 0 on, 3 not switched on); an agent not
   switched on gets neither the listener nor the skill, so onboarding an
   agent is one config line and touches no repo or launch command.
   Without a listener: `mailai wait` (5), or
   typing as above. Costs the same as a typed wake: nothing while idle, one
   line per wake.

3. **Reading.** `mailai read` prints each body inside a fence:

   ```
   ── message #12 from reviewer (pane, this machine) · subject: retry budget ──
   │ The retry loop in api/retry.go never backs off.
   │ ...
   ── end ──
   ```

   Every body line starts with `│ `. The header says what the daemon knows
   about the sender (§9), because who wrote a message is the first thing
   that decides how much of it to believe.

4. **Ask.** `mailai ask <to> <text>` sends and blocks in the CLI process,
   so the model spends nothing while waiting. By default the reply text
   alone is printed, so a script branches on it without the model reading a
   header; `--verbose` adds the fence and sender proof. It blocks until a reply in the
   thread arrives or `--timeout` passes. The daemon keeps the open ask
   graph and refuses a send that would close a cycle (`ask_cycle`). At most
   16 concurrent asks per sender.

5. **Wait.** `mailai wait [--timeout D] [-w PRINCIPAL]` blocks the same way
   until the mailbox holds unread mail, then prints it as `read --unread`
   does; exit 2 on timeout. It is for mail that is not the reply to an ask
   ("send me the rate when you have it") and for agents nothing can wake (no
   adapter, a script, a harness that runs commands in the background). An
   agent in a pane mailai can wake should end its turn instead. Found in the
   first real trial: with no wait verb, an agent polled `unread` every 5 s
   with no end.

**How a reply reaches its agent.** The daemon picks the path from what the
target is doing; none puts reply text into a pane:
- blocked in `mailai ask`: pushed down the waiting connection, the CLI
  prints it and exits; no notice, no typing, no extra turn;
- working: the next hook injects the one-line notice (1), or its listener's
  harness hands over the wake mid-turn (2);
- at rest: the wake types the one-line notice (2); with `none`, nothing can
  type and the notice waits for the agent's next run;
- blocked on a harness permission prompt: not mail; the hook holding the
  prompt gets the Inbox decision and the harness continues;
- on another machine: routed over the link, or held in the outbox, then one
  of the above on arrival.
`queue` is never used for replies: queue pushes a task, mail is pulled.

5. **Queue.** `mailai queue <to> <text>` is the one thing that does type at
   an agent: text typed as a prompt when the agent next comes to rest,
   never over a prompt it is waiting on, never twice. It requires `write`
   on the target and is how a supervisor hands out work; mail is how it
   gets answers.

## 7a. Answered in the session (decided 2026-11-22)

The person sometimes answers an agent in its own pane instead of the
Inbox. mailai cannot see that answer, only its traces; it says what it
knows, and tells the agent nothing (D12).

- **A dropped ask is withdrawn (decided 2026-11-23).** The connection of a
  blocking `ask human` or `ask-human` closes before a reply: the item
  closes `closed_by: withdrawn`. It becomes `in_session` only if the
  person then types in that agent's session within 2 minutes: a `prompt`
  from its hook, or with Herdr the agent going from rest (idle, done,
  blocked) to working. Esc-and-answer kills the ask first and types
  second; an agent that cancels its own ask (found on UbCl: it stopped its
  waiting ask to re-ask with a file) does so mid-turn, with nobody typing
  after. Only that blocking connection counts: a `--no-wait` item, or one
  waited on with `--wait-for`, stays open. An ask to another agent has no
  item; its ask-graph edge is released.
- **Withdraw.** `mailai withdraw <id>` takes back the caller's own open
  question (ask or ask-human), nobody else's; the person dismisses
  instead. `ask-human --replaces <id>` withdraws the caller's own open
  question as the new one opens. A blocking ask still waiting on a
  withdrawn question ends (`not_found: #N was withdrawn`). The person sees
  "withdrawn by <asker>" in the strip, the detail and a thread note. Both
  are refused on a spoke for now (they come with v0.1).
- **Dismissed (decided 2026-11-24).** The person (or a supervisor with
  `respond`) dismisses an ask or ask-human: the item closes `dismissed`.
  A blocking ask still waiting on it ends as a withdrawn one does
  (`not_found: #N was dismissed by the person`, exit 1). The daemon also
  stores one fixed line in the ask's thread, addressed to the asker:
  from `mailai`, kind reply, `reply_to` the ask, text `#N dismissed by
  the person` (or `by supervisor:<name>`), never verified, no sender
  proof, not rate limited; nobody's words (D12). It wakes the asker like
  any mail, so a `--no-wait` asker learns of it; for a blocking asker,
  told by its error, it is stored already read. It is never an answer:
  the item stays `dismissed`, a waiting ask ignores it, and `--wait-for`
  still says it ended without one. The TUI and the web show a message
  from `mailai` with no sender proof (only the daemon writes those; a
  bounce too) as a one-line system note, like the item notes below.
  **Known gap:** an asker on another machine gets neither: its item lives
  on the inbox machine, the spoke waits for a reply in the thread (and
  would take any reply as the answer), and no dismiss goes back over the
  link yet; a blocking spoke ask-human ends only by its timeout.
- **Certain: the item closes, `closed_by: in_session`.**
  - A held approval of agent X while X's hooks say the tool ran
    (`tool_ran` for the same tool) or the turn ended (`turn_ended`), or
    the holding hook's connection closes: the person answered the
    harness's own dialog. The hook is released with no decision, so the
    harness keeps what the person chose.
- **Likely: the item is marked, not closed.** X's hook reports a new
  prompt (`prompt`), or with Herdr X goes `blocked` → `working`, while X
  has an open ask item: it gets `maybe_in_session_at`, shown as "maybe
  answered in session". It stays open and answerable; dismissing closes it.
- **Signals** are the manifest's (D14): an event may carry
  `signal = "prompt" | "tool_ran" | "turn_ended"` beside its state, which
  the hook sends with `report-state` (and the tool name for `tool_ran`).
  Identity is the connection's: an agent's signals touch only its own
  items, and a caller with no principal sends none.
- **What the person sees.** The Inbox (TUI and web) has a dim "Recently
  closed" strip: items closed in the last hour, newest first, with how (by
  you, by supervisor:<name>, timeout, in session, dismissed). Open items
  carry the maybe badge; an item closed `in_session` carries the
  "answered in session" badge (green) in the strip, the all list and its
  detail, so an answer given outside mailai stands out. A Mail thread shows a one-line note rendered from
  its item ("answered in the session at 10:42"); no message is stored
  (a dismiss is the one exception, above).
  `mailai inbox` shows the badge, `mailai inbox --closed` lists recently
  closed with how; `--json` carries `closed_by`, `decided_by` and
  `maybe_in_session_at`.
- **Star (§13a).** The item lives on the inbox machine. When a spoke's
  blocking ask connection closes, or a held approval's tool ran, turn
  ended or hook died, the spoke sends `link-withdraw {mid}` or
  `link-withdraw {from, tool}` (retried while the link is down, up to an
  hour). The inbox machine accepts it only for items whose asker is
  `name@<that link's machine>`. A likely mark is not relayed in v1; a
  spoke's dropped ask closes withdrawn there, and typing afterwards is not
  relayed either.
- **Known limit.** A harness that kills a blocking `ask-human` on its own
  tool timeout closes the item `withdrawn` (`in_session` if the person
  types there within the window); an agent
  that may wait longer than its tool timeout uses `--timeout` below it, or
  `--no-wait`; a timed-out ask-human leaves its item open and resumes with
  `--wait-for <id>` (on a spoke too). The skill text says so. An ask still
  in a spoke's outbox when its asker goes away is dropped there and never
  sent.

## 8. Adapters and harnesses (D8)

An adapter answers three questions: which agents exist and in which session,
what state each is in, and how to type one line at one of them.

- **`herdr`:** chosen when `HERDR_ENV=1` is in the daemon's or the client's
  environment, or set in config. Agents and sessions come from
  `herdr agent list` and `herdr pane list --workspace`; state from
  `herdr agent get` (`idle`, `working`, `blocked`, `done`, `unknown`);
  typing from `herdr agent prompt <name> "<line>"` only when the state is
  `idle` or `done`. `blocked` only stops wakes and queues (D13: it is not an
  Inbox item). The adapter polls `agent list` every 2 s and subscribes to
  state events if the installed Herdr offers them (`herdr status` says).
  `--machine` is never used: each machine's daemon talks to its own Herdr.
- **`none`:** no multiplexer. Agents are known only through hooks and
  tokens (§9). State comes from the hooks (`Stop` → done,
  `UserPromptSubmit` → working, a permission request → blocked). There is
  no wake, because nothing can type; hook notices do the work.
- Harness manifests (D14): `mailai integration install <harness>` reads the
  manifest and writes hook entries that run `mailai hook <harness> <event>`:
  it reports the mapped state, reads the unread count and prints the notice
  line. A manifest that declares a holdable permission event lets the hook
  hold the prompt and wait for an answer from the Inbox (`[approvals]`
  config), returning the harness's decision. Whether a request is **risky**
  (waits for the person, never a supervisor) is whatever the harness's own
  payload says; mailai keeps no rule list of its own. A harness that says
  nothing has no risky requests.
  `--mcp` registers `mailai mcp` as an MCP server with read-only tools by
  default; `--mcp-write` adds `queue`.
- The skill: install also writes the `mailai --skill` guide as an Agent
  Skill, `mailai/SKILL.md` (frontmatter `name: mailai` and a one-line
  description saying when to load it, a marker line, then the guide), into
  the directory the manifest's `skills` names: `~/.claude/skills` for Claude
  Code, `~/.agents/skills` for Codex (and Pi, which reads it too). The
  harness shows the agent only the name and description until it loads it.
  Install is idempotent and rewrites a SKILL.md an older mailai wrote;
  uninstall deletes only a file carrying mailai's marker, so a user's own
  `mailai` skill is never touched; status says installed/current/stale.
  `--project DIR` uses `DIR/.claude` and `DIR/.agents` instead of the home
  (Claude Code hooks go to `DIR/.claude/settings.json`), to try one
  repository first; `--skill-only` touches the skill and not the hooks.

## 9. Identity (D4)

The daemon reads every caller's pid from the socket (`SO_PEERCRED`,
`LOCAL_PEERPID`); nothing the caller sends can change it. From the pid it
derives `sender_proof`.

**Decided 2026-11-22: an agent is what an agent program runs, not what
runs in a pane.** The person works in Herdr too, and an agent started
outside Herdr was passing as the person. So a pane counts as an agent's only
when one runs there, and an agent program among a caller's parents makes it
an agent anywhere. Agent programs are recognised by argv: `claude`, `codex`,
`pi`, `gemini`, `opencode`, `aider`, `goose`, `amp`, `crush`,
`cursor-agent`, `qwen`, `droid`, `copilot`, `kiro-cli`, or a script of a
known agent package under `node`/`bun`/`deno`/`python`. `[identity]
detect_agents = false` turns the check off (test rigs, which run under
whatever started them).

- **`pane`:** the caller is inside a pane the adapter knows **and an agent
  runs there**: Herdr lists an agent in the pane (a pane missing from the
  last list is asked about afresh; if Herdr cannot say, it counts as an
  agent's), or an agent program sits between the caller and the pane's
  shell. A pane running no agent, a plain shell, is a terminal like any
  other: the person's TUI and `mailai answer` work there. Herdr: the
  caller's ancestry reaches the pane's shell pid when `herdr pane list`
  exposes it, else its controlling tty is that pane's. When neither can be
  established the caller falls back to `token`, and `HERDR_PANE_ID` is only a
  hint for labelling. The pane's principal is then the
  sender, whatever `--from` says; a `--from` naming another pane is refused
  with `forbidden`.
- **`token`:** the caller presents a per-agent token the daemon issued at
  spawn (`mailai token new <name>`, exported into the agent's environment by
  whoever starts it). Used with the `none` adapter and for headless agents.
- **`agent`:** outside every pane, no token, but an agent program is among
  the caller's parents: an agent with no name of its own. Like `detached`
  it must say `--from <name>` (a claim, never `human`), is never verified,
  has none of the person's rights and cannot open a link; the refusal tells
  it to start with `MAILAI_TOKEN`.
- **`outside_pane`:** same user, no token, no agent program among its
  parents, outside every agent's pane, **at a
  controlling terminal** (the kernel's `tty_nr` / `e_tdev`). On a
  single-user machine this is, as far as mailai can tell, the person. It is
  what `--from human` from a shell needs; an agent cannot get here from a
  pane except through the out-of-scope routes below.
- **`detached`** (decided 2026-11-19): outside every pane, no token and **no
  controlling terminal**: cron, launchd/systemd, a `setsid`/daemonized child
  an agent left behind, the ssh bridge. It must say `--from <name>` (a
  claim, never `human`), is never verified, and has none of the person's
  rights (answer, grant, tokens, web). It may carry a link (§13a).
- **`link`:** the send arrived over a fleet link. The remote daemon's own
  proof is carried along and shown (`pane@devbox`), but is a claim here.

**The person.** A reply from `human` is `verified: human` only when it comes
from an attached TUI client, which signs replies with a nonce issued at
attach and never shown, logged or put in an environment; or from an
`outside_pane` caller with `--from human`. `--from human` from a `pane` or
`token` caller is refused. A reply produced by driving the person's TUI
through `herdr pane send-keys` is stored as `claimed_human` and shown as
`automated reply`. Reading `human`'s inbox from a pane is served as a peek
(`peek_forced`): an agent cannot clear what the person has not seen.

**Names (decided 2026-11-23).** A pane agent's name is Herdr's: its agent
name when one is set, else its pane id. Herdr keeps names unique among live
agents only, and a name outliving its agent keeps its mailbox on purpose: a
re-spun `supervisor` reads the old one's mail. Token names are reserved:
`token new` refuses a name a live Herdr agent has (`bad_request`, naming
its pane), and fails closed when the adapter cannot say (`offline`, one
line naming the cause; no token is issued), and a Herdr agent that takes a name a token holds is placed
under its pane id instead, by identity and the adapter poll alike; the
daemon logs that once per pane and name. A `pane` sender's pane is recorded
on the message (`sender_pane`) and shown beside its name (`builder ·
w4Y:p1`) in the TUI, the web UI and `mailai read`, on thread rows and
Inbox rows alike (an item takes it from its ask's message, an approval
from its hook), never in a wake or notice line, so a name that moved panes is visible. Mail over a link may
leave it empty. Accepted for now: any agent can `herdr agent rename`
itself to a free name (an absent agent's) and read that mailbox; closing it
would bind a name to one process and break role re-spins.

**Supervisors.** `verified: supervisor:<name>` is set when the caller's
proof is `pane` or `token`, the principal holds a `respond` grant and the
target is in that grant's scope. Nothing in the request chooses it.

**Out of scope,** as in TUIOS: another user, root, a changed binary, and a
process that deliberately leaves its pane through a service manager or
scheduler, an agent that opens a new shell pane (`herdr pane split`) and
types into it, and an agent program mailai does not recognise. The checks
stop an agent acting as the person through mailai, not a determined local
attacker.

**Saying why.** `whoami` and `inbox` carry `not_person`, one line on why a
caller is not the person and where the person's Inbox is (a pane with no
agent, a plain terminal, `mailai web`). The CLI and the TUI show it instead
of an empty Inbox. Windows and the BSDs give no peer pid and
are not supported in v1.

## 10. Grants and supervisors (D5)

A grant is `{principal, rights, scope}` stored in fleet state. Rights:

- `read`: read own mailbox, list the Inbox filtered to what the scope
  allows, peek others in scope.
- `write`: send mail to, ask, queue and wake principals in scope.
- `respond`: answer attention items in scope: reply as a trusted answer to
  an ask-human, approve or deny a held permission request, dismiss items.
- `admin`: manage grants and sessions. Only `human`.

Scope: a session, a selector (`group:fan/retry`), a machine, or `all`.
Defaults: every agent gets `read` + `write` on its own session and nothing
on `human` beyond sending mail. The person has everything.

Supervisors are the one new idea against TUIOS. The rules:
- `human` (an `outside_pane` or attached-client caller) grants `respond`.
  A supervisor may delegate a grant to another principal, narrower only: a
  subset of its rights over a subset of its scope, and a delegated grant
  cannot be delegated again (depth 2). Delegation is recorded in
  `mailai grant ls` and raises nothing in the Inbox or Mail. A pane cannot
  give itself a grant or widen one; such a request is refused with
  `forbidden`. Revoking a grant revokes everything delegated from it.
- A supervisor's answer is `verified: supervisor:<name>`. The asking agent
  sees that in the fence header and can act on it; the person sees it in the
  Inbox under a separate colour and can override by replying later in the
  same thread, which supersedes.
- A supervisor's answer is final (D13): the harness continues and the
  Inbox item closes. The thread, with its `supervisor:<name>` answer, is in
  Mail. A request the harness marks risky never reaches a supervisor's
  Inbox and waits for the person.
- A supervisor that errors or exits loses nothing: grants are by name and
  come back when an agent of that name starts, unless `--once`.

## 11. Local storage

`$XDG_DATA_HOME/mailai/` (default `~/.local/share/mailai/`):
- `identity.toml`: machine_id, fleet_id, name.
- `store.db`: mailboxes, attention items, grants, sessions, outbox (§13),
  and what the person hid (§6).
- `stash/<session>/`: stashed attachments, removed when the session ends.
- `tokens/`: issued agent tokens, hashed.
- `machine.key` (`0600`): this machine's random 32-byte key, presented
  when it links (§13a). `machines.json`: on the inbox machine, the spokes
  that have linked and the sha256 of each one's pinned key.
- `mailai.sock` (`0700`), `daemon.log`, `daemon.pid`.

Config in `$XDG_CONFIG_HOME/mailai/config.toml`: `adapter`, `[mail] wake`, `[mail] keep_days`,
`[approvals]`, `[hosts]` policy (§13), `[notify]`, theme.
`store.db` is SQLite through `modernc.org/sqlite` (pure Go, no cgo; Q2).
Evicted and expired messages are gone; there is no archive. The person's
delete only hides mail from the person (§6).

## 12. Protocol and clients (D7)

Clients speak JSON lines over the socket: `{"verb": ..., "id": N, ...}` →
`{"id": N, "ok": true, ...}` or `{"id": N, "error": "<code>", "hint": ...}`.
Error codes are a closed set (`forbidden`, `not_human`, `mailbox_full`,
`ask_cycle`, `unknown_principal`, `offline`, ...). `subscribe` streams
events (`mail`, `attention`, `agent_state`, `link`).

CLI (every command takes `--json`):

| Command | What it does |
|---|---|
| `mailai send <to> [text] [--file PATH] [--subject S] [--reply-to MID] [--attach PATH]… [--select SEL]` | Leave a message. Text from `--file`, or stdin when omitted. Empty text is refused in one line naming why (an empty argument, nothing on stdin, an empty file). |
| `mailai read [<#\|mid>] [--unread] [--peek] [--summary] [--thread MID] [--from P] [-w PRINCIPAL]` | Read, inside the fence. `--summary` prints one line per message (id, sender, proof, subject) so an agent triages before paying for bodies. `read <#>` prints one message, named as `reply` names it: the number in its fence header (a message in the caller's own mailbox or one it sent) or its mid; it marks that message read when it is the caller's own, unless `--peek` (decided 2026-11-25). `--thread` takes `#` too. |
| `mailai ask <to> <text> [--timeout MS]` · `mailai ask-human <text> -o A -o B [--no-wait] [--attach PATH]… [--replaces ID]` · `mailai withdraw <item>` | Send and wait; take a question back (§7a). |
| `mailai wait [--timeout D] [-w PRINCIPAL]` | Block until mail arrives, then print it (§7 point 5). |
| `mailai reply <#\|mid> [text] [--file PATH] [--to P]` | Reply in a thread, to the message's other party: its sender, or, for the caller's own message, its recipient (any machine, `name@machine` too); `--to` overrides (decided 2026-11-24). `#` is the number in a message's fence header (`54` or `#54`), a message in the caller's own mailbox or one it sent; the daemon resolves it, so a full mid works too. An unknown number says to reply with its mid and `--to`, which works (a mid not held roots the reply at it). mailai's own notes are nobody to reply to: a reply to one goes to the thread's latest other party, and the TUI and web reply in a thread to the newest message with a party other than the person, to that party. |
| `mailai queue <to> <text>` · `queue ls` · `queue rm` | Typed when the agent comes to rest. |
| `mailai inbox [--all] [--closed] [--session S]` · `mailai answer <item> <text\|choice>` · `mailai dismiss <item>` | The Inbox from the CLI. `--closed`: what closed in the last hour, and how (§7a). |
| `mailai delete <thread>...` · `mailai delete --message <mid>...` · `mailai delete --read-between-agents [--dry-run]` | The person deletes mail from their own views; agents keep theirs until expiry (§6). |
| `mailai agents` · `mailai whoami` | What the adapter sees; how the daemon places this caller. |
| `mailai status` | Daemon, adapter, links, and per agent the context cost: notice lines injected, messages and bytes read, asks and their wait time (D12). |
| `mailai grant <principal> <rights> [--scope S] [--once]` · `grant ls` · `grant rm` | Grants. `human` only. |
| `mailai stash put\|get\|list` | Session files. |
| `mailai token new <name>` · `token ls` · `token revoke` | Agent tokens. |
| `mailai integration install\|uninstall\|status [harness]… [--project DIR] [--skill-only] [--mcp] [--mcp-write]` | Harness hooks and the skill (§8). |
| `mailai mcp [--write]` | MCP server over stdio. |
| `mailai init` · `machine add <ssh-alias>` · `machine list\|remove\|rename\|alias` · `status` · `doctor` | Fleet. |
| `mailai service install\|uninstall\|status` · `mailai update` | Daemon lifecycle (§4). |
| `mailai tui`, or plain `mailai` | The Inbox TUI. |
| `mailai --skill [TOPIC]` | The short guide an agent reads to use mailai. |

Desktop notifications (`[notify]`, on by default) fire for Human Inbox
items only, never for Mail traffic (§12b).

TUI (D13): two views, `i` Inbox and `m` Mail. The Inbox is one list of
attention items across sessions and machines, oldest first,
with the item's kind, who is waiting, where, and for how long. `enter` opens
the pane's thread, `r` replies (signed as the person), `a`/`d` approve or
deny a held request, `x` dismisses (`d` too, on an item marked maybe
answered in session), `o` jumps to the oldest item. Under the open items, a
dim Recently closed strip says what closed in the last hour and how (§7a). Mail
lists every thread with filters by session, machine, principal and unread;
`t` narrows it to the person's own threads (started from or to `human`) or
to those between agents, and the principal filter follows one agent through
every thread it is in. `D` deletes the selected thread after a `y`; space or `x` marks rows, and with any marked `D` deletes the marked threads after one `y` (§6). Opening a thread marks the person's own mail in it
read, never an agent's. In a thread between agents `r` joins it as the
person, writing to every agent in it by default (`tab` picks one); the
web UI reads along by default and shows when each agent read (or, on another
machine, received) a message.
Mail between agents carries no new or unread marks for the person; only mail to or from the person counts as new.
Plain mail from an agent to `human` is Mail, not Inbox: an agent that needs
an answer uses `ask-human` or `ask human`, and that is what the Inbox lists. Supervisor answers show in their own colour.
A reply typed in the TUI is verified; the TUI refuses to run from inside a
pane for replying (it can still browse) so a nonce never lands where an
agent can drive it.
When the daemon goes away (a restart, an upgrade), the TUI keeps what it
shows, says it is reconnecting, refuses to act meanwhile, and dials the
same socket again (after 0.5 s, 1 s, 2 s, then every 3 s); once back it asks
who it is again, subscribes again and fetches everything again.

## 12a. Web UI (D11)

`mailai web [--port N]` serves the Inbox over HTTP on `127.0.0.1` only, with
the static files embedded in the binary. No framework; live updates by
server-sent events fed from `subscribe`. Parity rule: whatever the TUI can
do, the web UI can do, and nothing more.

- **Identity:** `mailai web` requires `outside_pane` proof and prints a URL
  carrying a one-time token that becomes a session cookie. The token is
  never in an environment, log or screen other than that line. Replies
  through a valid session are `verified: human`. Sessions expire after 12 h
  or on `mailai web --revoke`. Cookies ignore ports, so a write must also
  come from the page itself: JSON only (a foreign origin cannot send it
  without a CORS preflight, never granted), and an `Origin` or
  `Sec-Fetch-Site` header, when the browser sends one, must say this origin.
  A page an agent serves on another localhost port, or an attachment in its
  sandbox, cannot act with the person's session.
- **Remote:** the daemon never binds a non-loopback address. Tailscale
  Serve or `ssh -L` is the supported story; the phone case is the point.
- **Desktop notifications (decided 2026-11-23):** a bell in the page's
  header turns them on per browser (kept in that browser, off until the
  person turns it on; turning it on asks the browser's permission). While
  the page is in the background, a new Inbox item raises a desktop
  notification with its kind, asker, question and options; clicking it
  brings the page forward on that item. No server and no third party: the
  page's own live updates drive it, so it works only while the page is open
  somewhere. The TUI has none (a terminal has no such thing); `[notify]`
  (§12b) is the always-on path.
- **Always on (decided 2026-11-23, v0):** `[web] port` serves the page from
  the daemon's start, so an upgrade does not take it away. `[web] url` names
  where the person reaches it through their proxy: login links use it, and
  a write whose `Origin` is that URL is the page's own. Logins are kept in
  `web-sessions.json` (0600, SHA-256 of the cookie only, never the cookie)
  and survive a restart; `[web] session` sets how long one lasts (12 h).
  `mailai web --notify` also sends the one-time link to the person's
  notifications, so a phone logs in without a terminal; the link is only as
  private as the notification channel, so that channel must be the
  person's own.
- **Agents list (decided 2026-11-24):** built from Mail as before, and
  marked with liveness from the adapter poll. The daemon remembers when it
  last saw each agent live, in memory only. A live agent (matched by name
  or pane id) gets a dot and sorts first; a closed one is dimmed with
  "closed 2h ago", or just "closed" when the daemon restarted since; one
  whose liveness cannot be known (on another machine, known only by token,
  or the `none` adapter) is shown neutral. An agent still leaves the list
  when its mail goes (§6).
- **Scope guard:** the web UI may show only what `inbox`, `read` and the
  mailbox verbs return. Anything about pane output belongs to Herdr.
- **Attachments:** `/api/artifact?mid=M&i=N` reads the Nth attachment of
  message M from its path when it is opened: only a path a message
  attached, only a regular file, ranges allowed, never cached. Pictures and
  PDFs open as themselves; anything else a browser could run (HTML, SVG,
  text) is served under `Content-Security-Policy: sandbox` without
  `allow-same-origin`, so its scripts run in an opaque origin that cannot
  reach the session (the cookie is `SameSite=Strict`). The TUI lists the
  same files, numbered, and `v` opens one with the system viewer, or says
  where it is when there is none (over ssh).

## 12b. Notifications

An agent blocked on the person must not wait unnoticed. When a new Inbox
item opens on a daemon (an ask to `human`, an `ask-human`, a held
approval), that daemon pops one desktop notification: title `mailai`, body
one line such as `ask_human from builder@s2: Merge PR 51? [yes|no]` or
`risky approval from w1:p3: Bash: git push`, at most 120 bytes, control and
bidi characters removed, the question cut before the options. Items from a
spoke open on the inbox machine (§13a), so only the machine holding the
item notifies. Mail, closed and answered items never notify. Bursts
coalesce: at most one notification per 3 s; the rest of a window becomes
one summary (`two? (+2 more waiting)`), leaving out items closed meanwhile.
The text goes to the person, never into an agent's context (D12).

- **Backends:** `[notify] command`, else `osascript` on macOS, else
  `notify-send` on Linux, whichever is installed; with none, nothing.
  `[notify] enabled` defaults to true; the daemon log names the backend.
- **`command`** is an argv, e.g. `["ntfy", "publish", "--title={title}",
  "mytopic", "{body}"]`: `{title}` and `{body}` are replaced wherever they
  appear (one pass); with neither placeholder, title and body are appended
  as the last two arguments.
- **Safety:** no shell, ever, and no text inside script source. osascript
  runs a fixed script reading `argv`:
  `osascript -e 'on run argv' -e 'display notification (item 1 of argv) with title (item 2 of argv)' -e 'end run' -- <body> <title>`;
  notify-send gets `--` before its text. Each run is asynchronous and
  killed after 5 s; a failing backend is logged and never blocks or stops
  the daemon.

## 13. Fleet (D6)

Pattern and, where it fits, code from Enigma (`internal/link`, `internal/proto` framing, `internal/fleet`, `internal/service`, machine add and upgrade), copied into this repo and kept MIT-attributed. Where this spec is silent, Enigma's
§5 and §12 are the intended behaviour.

- **Links:** `ssh -T -o BatchMode=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o ControlMaster=no -o ControlPath=none <target> mailai bridge`. One link per pair; both dial, the lower machine_id's wins. Heartbeat 15 s, dead at 45 s, reconnect with backoff 1–60 s. `HELLO {fleet_id, machine_id, proto_version, version, platform}`.
- **Machine records** replicated last-writer-wins with Lamport clocks, as are grants and sessions. Mailboxes are **not** replicated: a mailbox lives on its principal's machine.
- **Routing:** a send to `reviewer@devbox` goes to the link to devbox, or to any linked machine that has a link to devbox (one hop of relay, with a hop counter and a 6-hop cap). Each machine keeps an **outbox** per unreachable peer; the Inbox shows it under "Waiting to send", and it drains on reconnect in order. Delivery is at-least-once with the `mid` as the dedupe key.
- **Human across machines:** mail to `human` is delivered to the sender's machine's `human` mailbox and an attention item is announced fleet-wide. Answering from any machine replies into the thread, which routes back to the asker's machine. A second answer from another machine lands as a reply in the same thread, not an error.
- **Hold policy:** `[hosts] hold_mail = ["build"]` puts mail from that machine into the person's Inbox instead of the recipient's, until released (`mailai release <mid>`). What one machine's agents may do to another's is said per host: `write`, `queue`, `respond`, with `queue` and `respond` off by default across machines.
- **Setup:** `mailai machine add <ssh-alias>` detects the platform, copies the binary, runs `mailai join`, installs the service and reports which peers it could link to. Same-version-everywhere upgrade over the link, as Enigma does.
- **Trust:** joining a fleet means trusting every member with code on this machine. Only add machines you control.

## 13a. The star (v1 fleet, decided 2026-11-19)

§13 is the eventual design. v1 ships a cut of it, because the person runs
many machines but answers from one.

- **Shape.** Every machine runs its own daemon, so asks, hooks, notices and
  wakes stay local to the agent. One machine is the **inbox machine**. Each
  other machine (a **spoke**) holds one link to it:
  `ssh -T -o BatchMode=yes ... <inbox> mailai bridge`, the bridge copying
  stdio to the inbox daemon's socket. Nothing listens on the network.
- **Config.** `[fleet] name` is this machine's name (short hostname by
  default) and prefixes every mid. A spoke sets `[fleet] inbox = "<ssh target>"`.
  A machine with no `inbox` is an inbox machine and accepts links.
- **Addresses.** Local agents are bare names. Another machine's agent is
  `name@machine`. A spoke sends `human`, and every `name@other`, to the
  inbox machine; the inbox machine stores it and forwards `name@spoke` one
  hop. `name@<own name>` is local.
- **Identity.** A link says `link-hello {machine}` and is placed `link`.
  It is never the person and never verified. What it sends is from
  `<claimed>@<machine>`; a link cannot claim `human` or an `@`.
  Messages delivered to a spoke keep the inbox machine's `verified` mark:
  the spoke trusts its inbox machine (joining is trusting, §13).
- **Machine keys** (decided 2026-11-20). Every daemon creates a random
  32-byte key once, `machine.key` (`0600`) in its data dir, and a spoke
  presents it in `link-hello {machine, key}`. The inbox machine pins
  sha256(key) per machine name in `machines.json` on the first hello that
  carries a key (trust on first use). A later hello for that name with a
  different key, or none, is refused `forbidden`, with a hint naming
  `mailai machine forget <name>`; the linked machine is untouched. A name
  with no pin (a `machines.json` from before keys, which was a bare list of
  names and is read as such, or a keyless hello from an older spoke) is
  pinned by its next hello with a key. `mailai machine ls` lists each
  machine that has linked: linked now or not, key pinned or not.
  `mailai machine forget <name>` drops the record and closes its link, so
  the next hello pins afresh; until then `name@<it>` is refused as never
  linked, and mail already held for it stays. Both are the person's only:
  never a link, a detached caller or an agent (`not_human`).
- **Asks wait at home.** A spoke's `ask` and `ask-human` send over the link
  without blocking it, then wait on the spoke for the reply in the thread.
  A dropped link or a sleeping laptop does not end the wait. A spoke's
  `ask-human` resumes as a local one does (decided 2026-11-20): on
  timeout it fails `timeout` with "no answer yet; come back with
  --wait-for <id>", where `<id>` is the inbox machine's item id that
  `link-send` returned, or, for a question still in the outbox, a local
  handle (2^40 + the queued send's id, so it never meets an item id;
  `--no-wait` returns the same id). `ask-human --wait-for <id>` on the
  spoke waits on that thread again, returns at once when the answer is
  already held there (read or not; it is marked read), and gets the inbox
  machine's refusal if the queued question was refused. The spoke keeps
  these ids in memory: after a spoke restart `--wait-for` is `not_found`
  and the answer arrives as mail.
- **Asks across machines** (decided 2026-11-20). The inbox machine enters
  every `ask` that arrives over a link in its own ask graph (§7), as an edge
  from `name@spoke` to the target, beside its own agents' asks. An ask that
  would close a cycle there is refused `ask_cycle`, and a spoke relays the
  refusal to its waiting caller unchanged; so are A@s2 → B@s1 → A@s2 and
  A@s2 → C@s3 → A@s2. `link-send` carries the ask's `timeout_ms`; the edge
  goes when a reply in the ask's thread addressed to the asker reaches the
  inbox machine (what ends the wait on the spoke), or when the timeout
  passes, 24 h when none is given and never more. The ask limit (16 per
  sender) stays the spoke's to enforce. Edges are memory only, gone if the
  inbox machine restarts. A cycle that closes through an ask local to a
  spoke (A@s2 → B@s1 → C@s2, then C asks A on s2) is not seen by either
  graph and ends by timeout.
- **Delivery.** The inbox machine keeps mail for `name@spoke` in that box,
  unread until the spoke acknowledges it, and pushes it on the link. On
  every `link-hello` it resends everything still unacknowledged; on a
  heartbeat (15 s) only what was last pushed more than 60 s ago and is
  still unacknowledged (decided 2026-11-20; it was everything, every
  heartbeat). The spoke acknowledges in bursts: the first delivery opens a
  100 ms window and one `link-ack` carries every mid that arrived in it. A
  spoke drops a mid it already holds but acknowledges it again:
  at-least-once, mid-deduped.
- **Approvals.** A spoke's held permission request is held on the inbox
  machine through the link and decided from its Inbox. If the link drops,
  the hook gets no decision and the harness falls back to its own prompt.
- **Kept running.** Every machine, inbox or spoke, runs
  `mailai service install` once (§4); its daemon then starts at login and
  after a crash, and a spoke's link comes back with it. The person runs it
  on each machine by hand (no `machine add`).
- **Spoke outbox** (decided 2026-11-20; it replaces "a spoke whose link is
  down refuses a send off the machine with `offline`"). Every mail, reply,
  ask and ask-human a spoke sends off the machine is first validated and
  queued durably in its outbox (`store.db`, table `outbox`) under a mid the
  spoke mints from its own id counter (`<spoke>:<id>`), with its thread
  resolved from what the spoke holds. With the link up and nothing older
  queued it is sent at once, so a refusal from the inbox machine still
  reaches the caller and nothing stays queued. Otherwise the caller gets
  `queued: true` and the outbox drains in order whenever the link comes up,
  and survives a spoke restart. An `ask` or `ask-human` sent while down
  waits on the spoke as any ask does; its Inbox item opens on the inbox
  machine when it arrives, and a queued ask carries what is left of its
  `timeout_ms`. `link-send` carries the spoke's `mid` and `thread_id`; the
  inbox machine accepts only mids prefixed with the link's own machine
  name (`forbidden` otherwise), stores the message under that mid, takes
  the spoke's thread only for a reply whose parent it does not hold, and
  drops a mid it already holds (returning the held copy, no second ask
  edge, no rate-limit token): at-least-once both ways, mid-deduped. While
  draining, `rate_limited` and `mailbox_full` keep the entry and retry on a
  later heartbeat; any other refusal drops it and is told: an ask still
  waiting gets the error itself; anything else becomes a **bounce** in the
  sender's own mailbox on the spoke, from `mailai`, kind reply, `reply_to`
  the queued mid, its body written by the daemon (`not delivered to <to>:
  <code>: <hint>`), never verified and not rate limited. The agent learns
  of it through the usual one-line notice, which names only `mailai`.
  `mailai ping` on a spoke shows the outbox count. Approvals are never
  queued: with the link down `hold-approval` is refused `offline` and the
  harness falls back to its own prompt.
- **Offline.** The inbox machine refuses `name@m` for a machine that never
  linked.
- **Not in v1.** Answering from a spoke, relay between spokes without the
  inbox machine, grants that cross machines, hold policy, `machine add`,
  upgrades over the link.

## 14. Engineering

- **Language:** Go (current stable). **Dependencies (D9):** stdlib,
  bubbletea/bubbles/lipgloss, BurntSushi/toml, fsnotify. SQLite is Q2.
- **Package layout:**
  - `cmd/mailai`: commands only
  - `internal/store`: mailboxes, items, grants, eviction; pure
  - `internal/identity`: peer pid, ancestry, tty, tokens, nonce
  - `internal/delivery`: notices, wakes, the queue
  - `internal/adapter`: interface + `herdr`, `none`
  - `internal/harness`: hook entrypoints per harness, skill text
  - `internal/proto`: frames and verbs (socket and link share one codec)
  - `internal/link`: ssh dialing, bridge, heartbeat
  - `internal/fleet`: records, routing, outbox, catch-up
  - `internal/daemon`: the process, socket server
  - `internal/mcp`
  - `internal/notify`: desktop notifications (§12b)
  - `internal/service`: launchd and systemd
  - `internal/tui`
- **Testing:**
  - Unit tests: store eviction and threading, identity placement (fake
    proc tree), grant resolution, fence rendering, and the D12 bound: a
    test that the hook notice for any inbox state is one line under 200
    bytes holding no sender-chosen text.
  - Simulation: N daemons in one process over in-memory pipes with a fake
    adapter; random sends, asks, disconnects, restarts. Invariants: every
    sent message is read exactly once or sits in exactly one outbox or
    mailbox; no message is ever delivered to a principal outside its
    scope; no `verified` is ever set on a message whose connection could
    not prove it.
  - Herdr adapter: contract tests against recorded `herdr` JSON, plus a
    live test behind `HERDR_ENV=1`.
  - End-to-end: 3 Linux Docker containers with sshd and a mailai daemon
    plus the Mac, as Enigma does; cut links to test relay and outbox.
    For the star (§13a), `e2e/fleet.sh` (`make e2e-fleet`) runs against
    the sandbox (`e2e/sandbox.sh`): ask-human from a spoke answered on the
    inbox machine, mail both ways, spoke-to-spoke ask, an approval held
    on a spoke, a link drop under a waiting ask, the spoke outbox across
    an inbox-machine restart, and a refused machine key; one PASS/FAIL
    line per flow, non-zero exit on any failure.
  - Always `go test -race ./...`, `gofmt -l .`, `go vet ./...`.
- **Releases:** goreleaser, darwin/linux × amd64/arm64, `checksums.txt`,
  `install.sh`, `mailai update`.

### Development method: TDD
Same contract as Enigma: red, green, refactor; bugs start as tests; existing
tests are never weakened; every decision D1–D10 and every rule in §6–§10 has
a test named after it (`TestD4_FromIsAClaim`); pre-commit runs tests, gofmt
and vet.

### Milestones
1. **Store and protocol:** `internal/store`, `internal/proto`, the daemon
   with the socket, `send`/`read`/`reply`/`ask`, fence rendering, eviction,
   threads. One machine, `none` adapter, no identity beyond `outside_pane`.
2. **Identity and the person:** peer pid, tokens, `human`, nonce, forced
   peek, the Inbox list and `answer`. `ask-human`.
3. **Herdr adapter:** agents and sessions from Herdr, state, wake via
   `agent prompt`, blocked panes as attention items. Harness hooks for
   Claude Code and Codex, `integration install`, the skill text.
4. **Supervisors:** grants, `respond`, `verified: supervisor`, the
   approvals hold path, Q1 resolved.
5. **TUI and web UI:** one view model, two renderers (§12, §12a).
6. **Fleet:** links, bridge, records, routing, outbox, `machine add`, hold
   policy, upgrades, `doctor`, releases.
7. **Polish:** MCP server, selectors and `--select`, stash across machines,
   notifications, end-to-end suite.

Each milestone ends with all tests passing and a review in tuicr.

## 15. Resolved questions

| Question | Resolution |
|---|---|
| Supervisor approvals: final or lingering? | Final; visible in Mail; harness-marked risky waits for the person (D13). |
| Store backend | SQLite, `modernc.org/sqlite` (D9). |
| Name | `mailai`. |
| Repo and license | `github.com/nadavushka-dev/mailai`, MIT. |
| Milestone order | Fleet stays at 6; single machine end to end first. |
| Human across machines | One person fleet-wide; answer from any machine (§5, §13). |
| Supervisor delegation | Narrower only, depth 2, silent (§10). |
| Harness support | Agnostic core, TOML manifests, graceful degradation (D14). |
| Herdr pane identity without a shell pid | tty match, then token fallback (§9). |
| Headless agents | Tokens only in v1 (§5). |
| Retention | Read mail goes after `keep_days` (7); evicted. The person's delete hides it from the person only (§6). |
| Desktop notifications | Yes, Inbox items only, `[notify]` (§12b). |
| Herdr `blocked` panes without mailai hooks | Not Inbox items (D13). |
| Plain mail to `human` | Mail view only; asks are the Inbox (D13). |
| What is risky | Whatever the harness's payload says; no mailai rule list (§8). |
| The person answered in the agent's own session | Certain signs (the blocking ask's connection died; the held approval's tool ran, turn ended or hook died) close the item `in_session`; likely ones (new prompt, Herdr blocked → working) mark it `maybe_in_session_at`. A Recently closed strip and a render-only thread note show it; the agent is told nothing (§7a). |
