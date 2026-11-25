package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMailai stands in for mailai under the Pi add-on: `listen` prints
// whatever the test drops in $FAKE_DIR/wake; `notice --json` prints
// $FAKE_DIR/notice.json, or fails when there is none (the daemon is down).
const fakeMailai = `#!/bin/sh
d="$FAKE_DIR"
case "$1" in
listen)
  [ "$2" = "--check" ] && exit 0
  while :; do
    if [ -f "$d/wake" ]; then cat "$d/wake"; rm -f "$d/wake"; fi
    sleep 0.05
  done;;
notice) cat "$d/notice.json";;
esac
`

// piHarness loads the add-on with a stand-in for Pi's extension API and
// plays a session at it.
const piHarness = `import addon from "./index.ts";
import { writeFileSync, renameSync, rmSync } from "node:fs";
const dir = process.env.FAKE_DIR;
const handlers = {};
const sent = [];
addon({
  on(ev, fn) { (handlers[ev] ??= []).push(fn); },
  sendMessage(m, o) { sent.push({ line: m.content, customType: m.customType, ...o }); },
});
const emit = async (ev, e = {}) => { for (const fn of handlers[ev] ?? []) await fn(e, {}); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wake = async (line) => {
  writeFileSync(dir + "/wake.tmp", line + "\n");
  renameSync(dir + "/wake.tmp", dir + "/wake");
  await sleep(600);
};
const unread = (count, line) => writeFileSync(dir + "/notice.json", JSON.stringify({ count, line, new: false }));
const landed = () => emit("message_start", { message: { role: "custom", customType: "mailai" } });
const fail = (msg) => { console.log("FAIL: " + msg + "; handed to Pi: " + JSON.stringify(sent)); process.exit(1); };
const L1 = "You have 1 unread mailai message from lead. Read it with: mailai read --unread";
const L2 = "You have 2 unread mailai messages from lead, qa. Read them with: mailai read --unread";

await emit("session_start");
// At rest: the line goes at once, as a steer that starts a turn.
await wake(L1);
if (sent.length !== 1 || sent[0].line !== L1 || sent[0].deliverAs !== "steer" || sent[0].triggerTurn !== true) fail("at rest");
await landed();
// Mid-turn: held until the turn's tools are done (where a steer lands),
// since a steer handed to Pi cannot be taken back.
await emit("agent_start");
await wake(L1);
if (sent.length !== 1) fail("handed to Pi mid-turn, before the tool calls ended");
// The agent read the mail meanwhile (the UbCl case): nothing.
unread(0, "");
await emit("turn_end");
if (sent.length !== 1) fail("woke for mail already read");
// Still unread at the boundary: the line as it is now.
await wake(L1);
unread(2, L2);
await emit("turn_end");
if (sent.length !== 2 || sent[1].line !== L2 || sent[1].deliverAs !== "steer" || sent[1].triggerTurn !== true) fail("mid-turn wake");
// One wake waits at a time, until it lands.
await wake(L1);
await emit("turn_end");
if (sent.length !== 2) fail("a second wake while one waits");
await landed();
// A run that ends with a line held: checked, then handed over.
await wake(L1);
unread(1, L1);
await emit("agent_end");
if (sent.length !== 3 || sent[2].line !== L1) fail("held when the run ended");
await landed();
// The check fails (daemon down): the held line goes as it came; a wake is
// never lost to a failed check.
await emit("agent_start");
await wake(L1);
rmSync(dir + "/notice.json");
await emit("turn_end");
if (sent.length !== 4 || sent[3].line !== L1) fail("check failed");
await emit("session_shutdown");
console.log("ok");
process.exit(0);
`

// Found by the UbCl watch (2026-11-25): 8 of about 150 wakes came after the
// agent had read that mail. The daemon rebuilds the line before it goes
// (TestListen_LineRebuiltBeforeItGoes), but mid-turn the add-on handed it
// to Pi at once as a steer, which lands after the current tool calls and
// cannot be recalled; a tool call that read the mail made it stale. The
// add-on now holds a mid-turn line until those tool calls are done and
// asks the daemon for the line as it is then; nothing unread sends nothing.
func TestPiAddonDropsAWakeReadBeforeItLands(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available to run the Pi add-on")
	}
	dir := t.TempDir()
	if _, err := WriteAddon("pi", dir); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "mailai")
	if err := os.WriteFile(bin, []byte(fakeMailai), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.mjs"), []byte(piHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", "--no-warnings", "run.mjs")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MAILAI_BIN="+bin, "FAKE_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
