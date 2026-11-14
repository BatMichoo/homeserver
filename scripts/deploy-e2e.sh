#!/bin/sh
# End-to-end test of the deployer: a small fixture project
# (deploy/testdata/e2e) in a throwaway git repository with a local "remote".
# Each round pushes a commit and runs the real deployer once (CI check off),
# then checks what it did to the running containers. Everything is removed
# on exit.
set -eu

cd "$(dirname "$0")/.."
root=$(pwd)
tmp=$(mktemp -d)
bare="$tmp/remote.git"
work="$tmp/work"     # where "commits are pushed from"
server="$tmp/server" # the deployer's checkout
bin="$tmp/deploy"

export GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.invalid
export GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.invalid
# The deployer adds a git safe.directory entry; keep it out of the real
# user's config.
export HOME="$tmp/home"
mkdir -p "$HOME"

compose() { (cd "$server" && docker compose "$@"); }

cleanup() {
	if [ -d "$server" ]; then
		compose down -v --rmi local --remove-orphans >/dev/null 2>&1 || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

fail() {
	echo "FAIL: $*" >&2
	if [ -f "$server/.deploy/state.json" ]; then
		echo "--- state.json:" >&2
		cat "$server/.deploy/state.json" >&2
		echo >&2
	fi
	exit 1
}

(cd deploy && go build -o "$bin" ./cmd/deploy)

git init -q --bare -b main "$bare"
cp -r "$root/deploy/testdata/e2e" "$work"
git -C "$work" init -q -b main
git -C "$work" add -A
git -C "$work" commit -qm "first"
git -C "$work" remote add origin "$bare"
git -C "$work" push -q origin main
git clone -q "$bare" "$server"
compose up -d --build --wait >/dev/null 2>&1 || fail "the fixture doesn't start"

push() {
	git -C "$work" add -A
	git -C "$work" commit -qm "$1"
	git -C "$work" push -q origin main
	git -C "$work" rev-parse HEAD
}
deploy() {
	(cd "$server" && DEPLOY_REMOTE="file://$bare" DEPLOY_CI=off DEPLOY_ONCE=1 DEPLOY_REPO=e2e/e2e \
		"$bin" run) >"$tmp/deploy.log" 2>&1 || {
		cat "$tmp/deploy.log" >&2
		fail "deployer run failed"
	}
}
cid() { compose ps -q "$1"; }
health() { docker inspect --format '{{.State.Health.Status}}' "$(cid "$1")"; }
result() { grep -o '"last_result": "[a-z-]*"' "$server/.deploy/public/status.json" | cut -d'"' -f4; }
set_env() { sed -i "s/HEALTHY: \".*\"/HEALTHY: \"$1\"/" "$work/docker-compose.yaml"; }

# 1. Only alpha changes: only alpha is recreated.
alpha=$(cid alpha)
beta=$(cid beta)
echo v2 >"$work/alpha/version"
push "alpha v2" >/dev/null
deploy
[ "$(cid alpha)" != "$alpha" ] || fail "round 1: alpha not recreated"
[ "$(cid beta)" = "$beta" ] || fail "round 1: beta was recreated"
[ "$(result)" = deployed ] || fail "round 1: status $(result)"
echo "round 1 (one service changed): ok"

# 2. beta's new definition is unhealthy; alpha changes too. beta goes back
#    to its previous definition, alpha's change stays.
alpha=$(cid alpha)
echo v3 >"$work/alpha/version"
set_env no
round2=$(push "alpha v3, broken beta")
deploy
[ "$(cid alpha)" != "$alpha" ] || fail "round 2: alpha not deployed"
[ "$(result)" = partial ] || fail "round 2: status $(result)"
[ -f "$server/.deploy/override.yaml" ] || fail "round 2: no override for the held service"
[ "$(health beta)" = healthy ] || fail "round 2: beta not healthy on its previous version"
grep -q '"service": "beta"' "$server/.deploy/public/status.json" || fail "round 2: beta not listed as held"
echo "round 2 (one service held back): ok"

# 3. A shared change (a new network) that breaks alpha: the whole commit
#    is rolled back and never tried again.
# (compose leaves unused networks out of its config, so alpha joins it.)
sed -i 's/^  alpha:$/  alpha:\n    networks: [default, extra]/' "$work/docker-compose.yaml"
printf '\nnetworks:\n  extra: {}\n' >>"$work/docker-compose.yaml"
echo bad >"$work/alpha/version"
round3=$(push "network and broken alpha")
deploy
[ "$(git -C "$server" rev-parse HEAD)" = "$round2" ] || fail "round 3: checkout not rolled back"
[ "$(result)" = rolled-back ] || fail "round 3: status $(result)"
grep -q "$round3" "$server/.deploy/state.json" || fail "round 3: commit not skipped"
[ "$(health alpha)" = healthy ] || fail "round 3: alpha not healthy after the rollback"
echo "round 3 (shared change rolled back): ok"

# 4. Undo round 3 and fix beta: beta is tried again and no longer held.
git -C "$work" checkout -q "$round2" -- docker-compose.yaml alpha/version
echo v4 >"$work/alpha/version"
set_env yes
push "fix beta" >/dev/null
deploy
[ "$(result)" = deployed ] || fail "round 4: status $(result)"
[ ! -f "$server/.deploy/override.yaml" ] || fail "round 4: override still there"
grep -q '"held": \[\]' "$server/.deploy/public/status.json" || fail "round 4: something still held"
[ "$(health beta)" = healthy ] || fail "round 4: beta unhealthy"
echo "round 4 (held service fixed): ok"

echo "deploy-e2e: ok"
