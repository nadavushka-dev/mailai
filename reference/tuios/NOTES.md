# TUIOS agent mail — reference notes

Source: https://github.com/Gaurav-Gosain/tuios (MIT), commit ff0a068, cloned 2026-10-29.
Only mail-related docs and Go sources are copied here; the rest of the repo is not.

## What it is
An inter-agent messaging system living inside a terminal multiplexer's daemon.
Every pane (agent) and the person (`human`) has an inbox. Not email.

## Files copied
- `AGENT_STATE.md` — the design guide. Key sections: "The Inbox", "Mail that wakes
  an agent at rest", "Who can act as the person" (threat model).
- `protocol.md` — JSON verb protocol (list-attention, dismiss-attention, mark-attention,
  respond, ask-human, release-agent-message, agent-mail-notice, delivery queue).
- `CLI_REFERENCE.md` — send-agent-message, read-agent-messages, ask-agent, ask-human, queue.
- `HOOKS.md`, `README.md`, `LICENSE`.
- `src/` — daemon sources (saved as `.go.txt` so the Go toolchain ignores them): verb_mailbox.go (AgentMessage, agentBus, agentMailbox ring),
  mail_notice.go, mail_wake.go, attention*.go (Inbox items), agent_queue.go / verb_queue.go
  (delivery queue), stash.go / verb_stash.go (shared files), human_sender.go /
  human_origin.go (verified vs claimed human), host_outbox.go (store-and-forward across
  machines), ask_human.go, verb_agents.go, hook.go (harness hooks), tools.go (MCP),
  agent_mail.go (client UI).

## Data model (verb_mailbox.go)
- Per-session bounded ring of `AgentMessage`, byte-capped, oldest evicted.
- Message: id, kind, from/to (window ids + labels), subject (<=120 chars), text,
  reply_to, thread_id (resolved at send time, = root message id), attachments
  (path references, never bytes; `stashed` = daemon-owned), sent_at, read_at, seen_at.
- Trust flags set only by the daemon: origin (local vs link), origin_host (claim),
  verified_human / claimed_human, held / released (link policy hold_mail).
- `from` is a claim; identity comes from kernel peer pid (SO_PEERCRED / LOCAL_PEERPID).
- Per-sender token-bucket rate limiting.
- Read marks, never consumes. "Seen" sits between unread and read.

## Delivery design (the interesting part)
- Mail never types into an agent. Hooks add one context line per message: count,
  senders, read command. Never subject or body.
- Agent at rest: after 5s unread, daemon queues one wake line (count + command only),
  once per message, >=30s apart, never into a pane on needs_input.
- Bodies print inside an "untrusted" fence, every line prefixed `│ `.
- `ask-agent` = send and wait for reply; cycle detection on the ask graph.
- `queue` = text typed as a prompt when the agent comes to rest.

## Trust model (human_sender.go, human_origin.go)
- Person's replies signed with an attach nonce only the attached client holds -> verified_human.
- `--from human` from inside a pane -> refused. UI driven by send-keys -> claimed_human,
  labelled "automated reply".
- Agents cannot clear the person's unread mail (forced peek), dismiss Inbox items, or
  approve tool calls (respond grant).

## Ideas worth borrowing for mailai
- Separate "who sent this" (kernel-proven) from "who it claims to be".
- Count-only notifications; content pulled by the reader, inside a fence.
- Thread id resolved at send time so a bounded store stays consistent.
- Attachments as references with liveness resolved at read time.
- Seen vs read distinction.
