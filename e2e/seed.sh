#!/bin/sh
# Fill a sandbox box with realistic traffic: three agents (worker, reviewer
# as a supervisor, gemini-cli), mail threads, open questions and two held
# approvals whose hooks block until answered. Idempotent enough to rerun.
set -eu
cd "$(dirname "$0")/.."
box=${1:-s1}
R() { e2e/sandbox.sh run "$box" "$@"; }
# P runs as the person: at a terminal, outside every pane (SPEC §9).
P() { e2e/sandbox.sh person "$box" "$@" | tr -d '\r'; }
# script(1) merges stderr into stdout, so keep only the token's own line.
tok() { P mailai token new "$1" 2>/dev/null | head -1; }
W=$(tok worker); V=$(tok reviewer); G=$(tok gemini-cli)
P mailai grant reviewer read,respond >/dev/null 2>&1 || true

# States, as the hooks would report them.
echo '{"hook_event_name":"UserPromptSubmit"}' | R env MAILAI_TOKEN="$W" mailai hook claude-code UserPromptSubmit >/dev/null
echo '{"hook_event_name":"Stop","last_assistant_message":"Review posted: one nit on the warn line."}' | R env MAILAI_TOKEN="$V" mailai hook claude-code Stop >/dev/null
echo '{"hook_event_name":"SessionStart"}' | R env MAILAI_TOKEN="$G" mailai hook claude-code SessionStart >/dev/null

# Mail.
P mailai send worker --subject "retry loop" "The retry loop in api/retry.go never backs off. Please add exponential backoff with jitter." >/dev/null
R env MAILAI_TOKEN="$W" mailai send human --subject "retry loop" "Done: backoff 100ms..5s with jitter, tests in retry_test.go." >/dev/null
R env MAILAI_TOKEN="$W" mailai send reviewer --subject "please review" "PR 42 is ready: api/retry.go, retry_test.go." >/dev/null
R env MAILAI_TOKEN="$V" mailai send worker --subject "please review" "Looks good. One nit: log the attempt number in the warn line." >/dev/null
R env MAILAI_TOKEN="$G" mailai send human --subject "nightly" "Nightly e2e: 212 passed, 0 failed, 14m." >/dev/null

# Questions and approvals, left blocking in the background.
R env MAILAI_TOKEN="$W" mailai ask-human "Deploy PR 42 to staging?" -o yes -o no -o later --timeout 12h >/dev/null 2>&1 &
R env MAILAI_TOKEN="$G" mailai ask-human "Rotate the staging DB password now?" -o yes -o no --timeout 12h >/dev/null 2>&1 &
R env MAILAI_TOKEN="$V" mailai ask human "Should the backoff cap stay at 5s or go to 30s for batch jobs?" --timeout 12h >/dev/null 2>&1 &
echo '{"tool_name":"Bash","tool_input":{"command":"rm -rf node_modules && npm ci"}}' | R env MAILAI_TOKEN="$W" mailai hook claude-code PermissionRequest >/dev/null 2>&1 &
echo '{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}' | R env MAILAI_TOKEN="$G" mailai hook claude-code PermissionRequest >/dev/null 2>&1 &
sleep 2
P mailai inbox
echo
echo "a supervisor token for reviewer (read,respond): MAILAI_TOKEN=$V"
