#!/bin/sh
# Sandbox: three throwaway Linux machines (s1 s2 s3) running the mailai
# built from this working tree, each with its own daemon, reachable from
# each other over real ssh (for the fleet, milestone 6). Nothing on this
# Mac is touched: no daemon, no ~/.local/share/mailai.
#
#   e2e/sandbox.sh up          build, start the boxes and their daemons
#   e2e/sandbox.sh shell s1    a shell on s1 (then: mailai whoami, mailai read ...)
#   e2e/sandbox.sh run s2 mailai unread
#   e2e/sandbox.sh log s1      s1's daemon log
#   e2e/sandbox.sh restart     rebuild from the tree and restart every daemon, mail kept
#   e2e/sandbox.sh seed        fill s1 with agents, mail, questions and held approvals
#   e2e/sandbox.sh web         start the web UI on s1 and print a URL that works from this Mac
#   e2e/sandbox.sh down        remove the boxes
set -eu
cd "$(dirname "$0")/.."
image=mailai-e2e:latest
net=mailai-sandbox-net
dir=${MAILAI_SANDBOX_DIR:-/tmp/mailai-sandbox}
boxes="s1 s2 s3"

cname() { echo "mailai-sandbox-$1"; }
sh_in() { box=$1; shift; docker exec -it -u dev -e HOME=/home/dev -e TERM="${TERM:-xterm-256color}" "$(cname "$box")" "$@"; }
run() { box=$1; shift; docker exec -i -u dev -e HOME=/home/dev "$(cname "$box")" "$@"; }
# WEBPORT on this Mac reaches the web UI on s1 (loopback inside the box, forwarded by socat).
WEBPORT=${MAILAI_SANDBOX_WEBPORT:-8471}

build() {
	mkdir -p "$dir/bin"
	version=${VERSION:-v0.0.0-dev.$(TZ=UTC git show -s --format=%cd --date=format-local:%Y%m%d%H%M%S HEAD).$(git rev-parse --short=7 HEAD)}
	arch=$(docker info --format '{{.Architecture}}' | sed 's/x86_64/amd64/; s/aarch64/arm64/')
	echo "building $version for linux/$arch"
	CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -ldflags "-X github.com/nadavushka-dev/mailai/internal/buildinfo.Version=$version" -o "$dir/bin/mailai.new" ./cmd/mailai
	# The boxes bind-mount the file, so write into the same inode: cp, not rename.
	cp "$dir/bin/mailai.new" "$dir/bin/mailai" && rm -f "$dir/bin/mailai.new"
}

start_daemon() {
	# No systemd in the box: the daemon runs detached under the dev user.
	docker exec -d -u dev -e HOME=/home/dev "$(cname "$1")" sh -c 'mailai daemon run >>/home/dev/mailai.log 2>&1'
	for _ in $(seq 1 100); do run "$1" mailai ping >/dev/null 2>&1 && return 0; sleep 0.05; done
	echo "$1: daemon did not come up; e2e/sandbox.sh log $1" >&2; return 1
}

stop_daemon() { run "$1" sh -c 'pkill -INT -x mailai 2>/dev/null || true'; }

# ensure brings stopped boxes back (Docker restarted, machine rebooted) with
# their mail kept, and their daemons with them. Missing boxes need `up`.
ensure() {
	for b in $boxes; do
		state=$(docker inspect -f '{{.State.Running}}' "$(cname "$b")" 2>/dev/null) || { echo "no sandbox: e2e/sandbox.sh up" >&2; exit 1; }
		if [ "$state" != true ]; then
			docker start "$(cname "$b")" >/dev/null
			start_daemon "$b"
		elif ! run "$b" mailai ping >/dev/null 2>&1; then
			start_daemon "$b"
		fi
	done
}

up() {
	docker image inspect "$image" >/dev/null 2>&1 || docker build -q -t "$image" e2e/docker
	mkdir -p "$dir/keys"
	if [ ! -f "$dir/keys/id_ed25519" ]; then
		ssh-keygen -q -t ed25519 -N "" -f "$dir/keys/id_ed25519"
		cp "$dir/keys/id_ed25519.pub" "$dir/keys/authorized_keys"
	fi
	build
	docker network inspect "$net" >/dev/null 2>&1 || docker network create "$net" >/dev/null
	for b in $boxes; do
		docker rm -f "$(cname "$b")" >/dev/null 2>&1 || true
		publish=""
		[ "$b" = s1 ] && publish="-p 127.0.0.1:$WEBPORT:8472"
		# shellcheck disable=SC2086
		docker run -d --name "$(cname "$b")" --hostname "$b" --network "$net" --network-alias "$b" $publish \
			-v "$dir/bin/mailai:/usr/local/bin/mailai:ro" -v "$dir/keys:/keys:ro" "$image" >/dev/null
		docker exec "$(cname "$b")" sh -c 'cp /keys/id_ed25519 /home/dev/.ssh/id && chown dev:dev /home/dev/.ssh/id && chmod 600 /home/dev/.ssh/id'
		start_daemon "$b"
	done
	echo
	echo "sandbox is up: $boxes, one mailai daemon each. try:"
	echo "  e2e/sandbox.sh shell s1      # then: mailai whoami; mailai send worker hi; mailai read -w worker"
	echo "  e2e/sandbox.sh run s2 mailai unread"
}

restart() {
	ensure
	build
	for b in $boxes; do stop_daemon "$b"; done
	sleep 0.3
	for b in $boxes; do start_daemon "$b"; done
	echo "daemons restarted on $boxes"
}

web() {
	ensure
	# The daemon serves 127.0.0.1:8471 inside s1; socat exposes it on the box's
	# eth0 as 8472, which docker publishes to 127.0.0.1:$WEBPORT here. That is
	# the "your own tunnel" of SPEC §12a, sandbox edition.
	docker exec -d "$(cname s1)" sh -c 'pgrep -x socat >/dev/null || socat TCP-LISTEN:8472,bind=0.0.0.0,fork,reuseaddr TCP:127.0.0.1:8471'
	url=$(run s1 mailai web --port 8471 --json | sed -n 's/.*"url": "\([^"]*\)".*/\1/p')
	[ -n "$url" ] || { echo "could not start the web UI; e2e/sandbox.sh log s1"; exit 1; }
	echo "open on this Mac (one-time link, session lasts 12 h):"
	echo "  $(echo "$url" | sed "s#127.0.0.1:8471#127.0.0.1:$WEBPORT#")"
}

seed() { ensure; e2e/seed.sh s1; }

down() {
	for b in $boxes; do docker rm -f "$(cname "$b")" >/dev/null 2>&1 || true; done
	docker network rm "$net" >/dev/null 2>&1 || true
	echo "sandbox removed"
}

case "${1:-}" in
up) up ;;
down) down ;;
restart) restart ;;
web) web ;;
seed) seed ;;
shell) ensure; sh_in "${2:?box}" sh -l ;;
run) shift; ensure; run "$@" ;;
log) run "${2:?box}" cat /home/dev/mailai.log ;;
*) sed -n '2,14p' "$0"; exit 2 ;;
esac
