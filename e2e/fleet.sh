#!/bin/sh
# Cross-machine end-to-end checks against the running sandbox (SPEC §13a):
# s1 is the inbox machine, s2 and s3 are spokes linked to it over real ssh.
# Run `e2e/sandbox.sh restart` first after a code change, then
#
#   e2e/fleet.sh            (or: make e2e-fleet)
#
# Prints one PASS/FAIL line per flow and exits non-zero if any failed. Every
# run tags its texts, so earlier mail in the boxes does not confuse it. The
# sandbox is left as it was found: daemons up, original machine keys.
set -u
cd "$(dirname "$0")/.."
TAG=e2e$(date +%s)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fails=0

cname() { echo "mailai-sandbox-$1"; }
# dx runs a command on a box with no terminal: a detached caller, which
# must say --from (SPEC §9).
dx() { b=$1; shift; docker exec -i -u dev -e HOME=/home/dev "$(cname "$b")" "$@" </dev/null; }
# asperson runs a command at a terminal on a box: the person.
asperson() {
	b=$1; shift
	cmd=""
	for a in "$@"; do cmd="$cmd '$(printf '%s' "$a" | sed "s/'/'\\\\''/g")'"; done
	# TERM=dumb skips bubbletea's terminal colour probe, which waits 5 s on a
	# script(1) terminal that never answers it.
	docker exec -i -u dev -e HOME=/home/dev -e TERM=dumb "$(cname "$b")" script -qec "$cmd" /dev/null </dev/null | tr -d '\r'
}
start_daemon() {
	docker exec -d -u dev -e HOME=/home/dev "$(cname "$1")" sh -c 'mailai daemon run >>/home/dev/mailai.log 2>&1'
	until_ok 10 dx "$1" mailai ping
}
stop_daemon() {
	dx "$1" sh -c 'pkill -INT -x mailai 2>/dev/null; while pgrep -x mailai >/dev/null; do sleep 0.1; done'
}
# until_ok SECONDS CMD...: retry CMD every 0.2 s until it succeeds.
until_ok() {
	n=$(($1 * 5)); shift
	while [ "$n" -gt 0 ]; do
		"$@" >/dev/null 2>&1 && return 0
		sleep 0.2; n=$((n - 1))
	done
	return 1
}
has() { "$@" 2>/dev/null | grep -q -- "$PATTERN"; }
# wait_for SECONDS PATTERN CMD...: until CMD's output contains PATTERN.
wait_for() { s=$1; PATTERN=$2; shift 2; until_ok "$s" has "$@"; }
# item_id PATTERN: the id of the open Inbox item on s1 whose line has PATTERN.
item_id() {
	PATTERN=$1
	until_ok 15 has asperson s1 mailai inbox || return 1
	asperson s1 mailai inbox | grep -F -- "$1" | sed -n 's/^#\([0-9][0-9]*\) .*/\1/p' | head -1
}
# mid_of BOX MAILBOX TEXT: the mid of the message in MAILBOX holding TEXT.
mid_of() {
	dx "$1" mailai read -w "$2" --peek --json | awk -v t="$3" '
		/"mid":/ { gsub(/[",]/, "", $2); mid = $2 }
		index($0, t) && /"text":/ { print mid; exit }'
}
step() { echo "  $*" >>"$tmp/log"; }
flow() {
	name=$1; shift
	: >"$tmp/log"
	if "$@"; then
		echo "PASS $name"
	else
		echo "FAIL $name"
		sed 's/^/     /' "$tmp/log"
		fails=$((fails + 1))
	fi
}

# 1. An agent on a spoke asks the person; the person answers on the inbox machine.
ask_human() {
	q="deploy $TAG?"
	dx s2 mailai ask-human --from w2 --timeout 30s -o yes -o no "$q" >"$tmp/ah" 2>&1 &
	pid=$!
	id=$(item_id "$q") && [ -n "$id" ] || { step "no Inbox item on s1"; kill $pid 2>/dev/null; return 1; }
	asperson s1 mailai answer "$id" yes >/dev/null || { step "answer failed"; return 1; }
	wait $pid || { step "ask-human exited non-zero: $(cat "$tmp/ah")"; return 1; }
	[ "$(cat "$tmp/ah")" = yes ] || { step "ask-human printed: $(cat "$tmp/ah")"; return 1; }
}

# 2. Mail from the inbox machine to a spoke's agent, and its reply back.
mail_and_reply() {
	dx s1 mailai send --from r1 w2@s2 "hello $TAG" >/dev/null || { step "send failed"; return 1; }
	wait_for 10 "hello $TAG" dx s2 mailai read -w w2 --peek || { step "mail never reached s2"; return 1; }
	mid=$(mid_of s2 w2 "hello $TAG")
	[ -n "$mid" ] || { step "no mid"; return 1; }
	dx s2 mailai reply --from w2 "$mid" "back $TAG" >/dev/null || { step "reply failed"; return 1; }
	wait_for 10 "back $TAG" dx s1 mailai read -w r1 --peek || { step "reply never reached s1"; return 1; }
	dx s1 mailai read -w r1 --peek --json | grep -q '"from": "w2@s2"' || { step "reply not from w2@s2"; return 1; }
}

# 3. A spoke's agent asks another spoke's agent, through the inbox machine.
spoke_to_spoke_ask() {
	dx s3 mailai ask --from a3 --timeout 30s w2@s2 "q3 $TAG" >"$tmp/ask3" 2>&1 &
	pid=$!
	wait_for 10 "q3 $TAG" dx s2 mailai read -w w2 --peek || { step "ask never reached s2"; kill $pid 2>/dev/null; return 1; }
	mid=$(mid_of s2 w2 "q3 $TAG")
	dx s2 mailai reply --from w2 "$mid" "a3 $TAG" >/dev/null || { step "reply failed"; return 1; }
	wait $pid || { step "ask exited non-zero: $(cat "$tmp/ask3")"; return 1; }
	[ "$(cat "$tmp/ask3")" = "a3 $TAG" ] || { step "ask printed: $(cat "$tmp/ask3")"; return 1; }
}

# 4. A permission request held on s3 is decided from s1's Inbox.
approval() {
	# grep -o: at a terminal the CLI's first line may carry terminal queries.
	tok=$(asperson s3 mailai token new "hook-$TAG" | grep -o 'mt_[0-9a-f]*' | head -1)
	[ -n "$tok" ] || { step "no token"; return 1; }
	printf '{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"echo %s"}}' "$TAG" |
		docker exec -i -u dev -e HOME=/home/dev -e MAILAI_TOKEN="$tok" "$(cname s3)" mailai hook claude-code PermissionRequest >"$tmp/hook" 2>&1 &
	pid=$!
	id=$(item_id "echo $TAG") && [ -n "$id" ] || { step "no approval item on s1"; kill $pid 2>/dev/null; return 1; }
	asperson s1 mailai answer "$id" allow >/dev/null || { step "answer failed"; return 1; }
	wait $pid
	asperson s3 mailai token revoke "hook-$TAG" >/dev/null
	grep -q '"behavior":"allow"' "$tmp/hook" || { step "hook printed: $(cat "$tmp/hook")"; return 1; }
}

# 5. The link drops while a spoke's ask waits; the reply still arrives.
link_drop() {
	dx s2 mailai ask --from w2 --timeout 60s r1@s1 "drop $TAG" >"$tmp/drop" 2>&1 &
	pid=$!
	wait_for 10 "drop $TAG" dx s1 mailai read -w r1 --peek || { step "ask never reached s1"; kill $pid 2>/dev/null; return 1; }
	mid=$(mid_of s1 r1 "drop $TAG")
	dx s2 pkill -x ssh
	wait_for 5 "is down" dx s2 mailai ping || step "(link was back before it was seen down)"
	dx s1 mailai reply --from r1 "$mid" "after drop $TAG" >/dev/null || { step "reply failed"; return 1; }
	wait $pid || { step "ask exited non-zero: $(cat "$tmp/drop")"; return 1; }
	[ "$(cat "$tmp/drop")" = "after drop $TAG" ] || { step "ask printed: $(cat "$tmp/drop")"; return 1; }
}

# 6. The inbox machine's daemon is down: a spoke's mail waits in its outbox
# and is delivered once when it is back.
outbox() {
	stop_daemon s1
	wait_for 10 "is down" dx s2 mailai ping || { step "s2 never saw the link down"; start_daemon s1; return 1; }
	out=$(dx s2 mailai send --from w2 r1@s1 "outbox $TAG")
	echo "$out" | grep -q '^queued' || { step "send said: $out"; start_daemon s1; return 1; }
	dx s2 mailai ping | grep -q 'outbox: 1 waiting' || { step "ping: $(dx s2 mailai ping)"; start_daemon s1; return 1; }
	start_daemon s1 || { step "s1 did not come back"; return 1; }
	wait_for 20 "outbox $TAG" dx s1 mailai read -w r1 --peek || { step "never delivered"; return 1; }
	wait_for 10 "outbox: 0 waiting" dx s2 mailai ping || { step "outbox not drained"; return 1; }
	n=$(dx s1 mailai read -w r1 --peek | grep -c "outbox $TAG")
	[ "$n" = 1 ] || { step "delivered $n times"; return 1; }
}

# 7. A machine that comes back with another machine key is refused, and
# its old key links again.
machine_key() {
	before=$(dx s3 wc -l /home/dev/mailai.log | cut -d' ' -f1)
	stop_daemon s3
	dx s3 mv /home/dev/.local/share/mailai/machine.key /home/dev/.local/share/mailai/machine.key.e2e
	start_daemon s3
	ok=0
	if wait_for 10 "mailai machine forget s3" dx s3 sh -c "tail -n +$((before + 1)) /home/dev/mailai.log"; then
		dx s3 mailai ping | grep -q 'is down' && asperson s1 mailai machine ls | grep -q '^s3.*down.*key pinned' && ok=1
	fi
	[ $ok = 1 ] || step "new key not refused; s3 log: $(dx s3 tail -n 3 /home/dev/mailai.log)"
	stop_daemon s3
	dx s3 mv /home/dev/.local/share/mailai/machine.key.e2e /home/dev/.local/share/mailai/machine.key
	start_daemon s3
	wait_for 15 "is up" dx s3 mailai ping || { step "s3 did not link again with its own key"; return 1; }
	[ $ok = 1 ]
}

for b in s1 s2 s3; do
	dx "$b" mailai ping >/dev/null 2>&1 || { echo "FAIL sandbox: no daemon on $b (e2e/sandbox.sh up, or restart)"; exit 1; }
done
for b in s2 s3; do
	wait_for 15 "is up" dx "$b" mailai ping || { echo "FAIL sandbox: $b is not linked to s1"; exit 1; }
done

flow "ask-human from s2 answered on s1" ask_human
flow "mail s1 -> s2 and reply" mail_and_reply
flow "s3 asks s2 through s1" spoke_to_spoke_ask
flow "approval held on s3 decided on s1" approval
flow "link drop with a waiting ask" link_drop
flow "s1 down: outbox on s2, delivered when back" outbox
flow "mismatched machine key refused" machine_key

[ "$fails" = 0 ] && echo "fleet e2e: all passed" || echo "fleet e2e: $fails failed"
[ "$fails" = 0 ]
