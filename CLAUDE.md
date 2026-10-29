# mailai

A mail system for coding agents and the people who run them, written in Go.
**`SPEC.md` is the design of record.** Read it before writing code.
Prior art lives in `reference/tuios/` (TUIOS agent mail, MIT); read `reference/tuios/NOTES.md` first.

## Rules
- **TDD, always** (SPEC §14): write a failing test first, then the code. Every bug fix starts with a test that reproduces it.
- Never delete, skip or weaken an existing test to make a change pass. Raise it and ask.
- Discuss design changes before implementing them. If code and SPEC disagree, stop and ask.
- `go test -race ./...`, `gofmt -l .` and `go vet ./...` must be clean before anything is called done.
- Dependencies (D9): stdlib, fsnotify, BurntSushi/toml, bubbletea/bubbles/lipgloss, modernc.org/sqlite. Ask before adding anything else.
- Full separation (D2): no code or runtime dependency on Enigma, Herdr or TUIOS. Herdr is reached only through its CLI, as an optional adapter.
  Code from `../enigma` may be copied in verbatim where it fits (link, bridge, proto framing, fleet, service), never imported.
- Token economy (D12): nothing mailai injects into an agent's context may exceed one line, and never sender-chosen text.

## Dev workflow (not part of the product)
- Tasks: tuxedo, `TODO_FILE=$PWD/todo.txt tuxedo ...`
- Review: `tuicr -w` before committing.
- Parallel agent work: herdr workspaces.
