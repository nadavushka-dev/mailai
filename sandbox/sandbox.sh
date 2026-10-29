#!/bin/sh
# A throwaway mailai on this machine, nowhere near your real one.
#
#   sandbox/sandbox.sh up        build, start a daemon in a temp home, print how to reach it
#   sandbox/sandbox.sh shell     a shell where `mailai` is the sandbox binary and daemon
#   sandbox/sandbox.sh run ...   run one mailai command against the sandbox
#   sandbox/sandbox.sh log       the daemon's log
#   sandbox/sandbox.sh down      stop the daemon and delete everything
#
# Everything lives under $SANDBOX (default /tmp/mailai-sandbox). Your
# ~/.local/share/mailai and ~/.config/mailai are never touched.
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SANDBOX=${SANDBOX:-/tmp/mailai-sandbox}
BIN="$SANDBOX/bin/mailai"
export MAILAI_HOME="$SANDBOX/home"
export MAILAI_SOCKET="$SANDBOX/mailai.sock"
PIDFILE="$SANDBOX/daemon.pid"
LOG="$SANDBOX/daemon.log"

running() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }

up() {
  mkdir -p "$SANDBOX/bin" "$MAILAI_HOME"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/mailai)
  if running; then
    echo "daemon already running (pid $(cat "$PIDFILE")); rebuilt binary, restart with: $0 down && $0 up"
  else
    nohup "$BIN" daemon run >"$LOG" 2>&1 &
    echo $! >"$PIDFILE"
    for _ in $(seq 1 100); do [ -S "$MAILAI_SOCKET" ] && break; sleep 0.05; done
    "$BIN" ping >/dev/null || { echo "daemon did not come up; see $LOG"; exit 1; }
    echo "sandbox daemon up (pid $(cat "$PIDFILE"))"
  fi
  cat <<MSG

  $0 shell          # a shell wired to the sandbox
  $0 run whoami     # one command

or in your own shell:
  export PATH="$SANDBOX/bin:\$PATH" MAILAI_HOME="$MAILAI_HOME" MAILAI_SOCKET="$MAILAI_SOCKET"
MSG
}

down() {
  if running; then kill "$(cat "$PIDFILE")" 2>/dev/null || true; sleep 0.2; fi
  rm -rf "$SANDBOX"
  echo "sandbox removed"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  shell)
    running || { echo "sandbox not running; $0 up first"; exit 1; }
    export PATH="$SANDBOX/bin:$PATH"
    echo "mailai sandbox shell: exit to leave. Home: $MAILAI_HOME"
    exec "${SHELL:-/bin/sh}" ;;
  run)
    shift; running || { echo "sandbox not running; $0 up first"; exit 1; }
    exec "$BIN" "$@" ;;
  log) cat "$LOG" ;;
  status)
    if running; then echo "running (pid $(cat "$PIDFILE")) at $MAILAI_SOCKET"; else echo "not running"; fi ;;
  *) sed -n '2,13p' "$0"; exit 2 ;;
esac
