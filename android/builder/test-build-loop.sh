#!/bin/sh
# Tests for build-loop.sh. Run inside the builder image (it has keytool):
#   docker run --rm --entrypoint sh hs-android /src/builder/test-build-loop.sh
# A fake GRADLE_CMD stands in for Gradle: it logs its arguments, writes a fake
# APK where the real build puts it, and can be told to fail or to kill itself.
set -u

here=$(cd "$(dirname "$0")" && pwd)
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
failures=0

# new_env makes a fresh sandbox for one test and exports the loop's settings.
new_env() {
	t=$root/$1
	mkdir -p "$t/src/app" "$t/data" "$t/out" "$t/gradle"
	echo "class A" >"$t/src/app/A.kt"
	echo "fake ca" >"$t/ca.crt"
	cat >"$t/gradle.sh" <<'FAKE'
#!/bin/sh
echo "$*" >>"$FAKE_LOG"
case "${FAKE_MODE:-ok}" in
fail) exit 1 ;;
kill) kill -9 $$ ;;
esac
mkdir -p app/build/outputs/apk/release
echo "apk built from $*" >app/build/outputs/apk/release/app-release.apk
FAKE
	chmod +x "$t/gradle.sh"
	: >"$t/calls"
	export SRC_DIR="$t/src" DATA_DIR="$t/data" OUT_DIR="$t/out" CA_FILE="$t/ca.crt"
	export GRADLE_USER_HOME="$t/gradle" GRADLE_CMD="$t/gradle.sh" FAKE_LOG="$t/calls"
	export LAN_DOMAIN=home.lan HTTPS_PORT=443 HTTP_PORT=80 ONCE=1
	export MIN_FREE_KB=1 FAKE_MODE=ok
}

run() { sh "$here/build-loop.sh" >"$t/log" 2>&1; }
calls() { wc -l <"$t/calls" | tr -d ' '; }
counter() { cat "$t/data/counter" 2>/dev/null; }
sum() { sha256sum "$1" | cut -d' ' -f1; }

check() { # check <description> <command...>
	d=$1
	shift
	if "$@"; then :; else
		echo "  FAIL: $d"
		sed 's/^/    | /' "$t/log" 2>/dev/null
		return 1
	fi
}
has() { grep -q -e "$1" "$2"; }
no_files() { [ -z "$(find "$1" -maxdepth 1 -name "$2" ! -name . ! -path "$1" 2>/dev/null)" ]; }

test_first_build_publishes() {
	new_env first
	run || return 1
	check "apk published" test -s "$OUT_DIR/tv.apk" || return 1
	check "versionCode 1" has '"versionCode": 1[,}]' "$OUT_DIR/tv.json" || return 1
	check "apk path" has '"apk": "/tv.apk"' "$OUT_DIR/tv.json" || return 1
	check "counter 1" test "$(counter)" = 1 || return 1
	check "all -P flags passed" has '-PlanDomain=home.lan.*-PhttpsPort=443.*-PhttpPort=80.*-PcaFile=.*-PversionCode=1.*-PversionName=1-' "$t/calls" || return 1
	check "name is code-hash7" has '"versionName": "1-[0-9a-f]\{7\}"' "$OUT_DIR/tv.json" || return 1
	check "no temp files left" no_files "$OUT_DIR" '.*' || return 1
	check "keystore pass private" test "$(stat -c %a "$DATA_DIR/keystore.pass")" = 600
}

test_unchanged_inputs_skip() {
	new_env skip
	run && run || return 1
	check "one gradle call" test "$(calls)" = 1 || return 1
	check "counter 1" test "$(counter)" = 1 || return 1
	check "says up to date" has 'up to date' "$t/log"
}

test_counter_increases() {
	new_env counter
	run || return 1
	LAN_DOMAIN=other.lan run || return 1
	check "domain change -> 2" test "$(counter)" = 2 || return 1
	check "json 2" has '"versionCode": 2[,}]' "$OUT_DIR/tv.json" || return 1
	echo "class B" >"$SRC_DIR/app/A.kt"
	run || return 1
	check "source change -> 3" test "$(counter)" = 3 || return 1
	# a rename changes the hash too, and build/ output is ignored
	mv "$SRC_DIR/app/A.kt" "$SRC_DIR/app/B.kt"
	run || return 1
	check "rename -> 4" test "$(counter)" = 4 || return 1
	mkdir -p "$SRC_DIR/app/build" "$SRC_DIR/.gradle"
	echo junk >"$SRC_DIR/app/build/x" && echo junk >"$SRC_DIR/.gradle/y"
	run || return 1
	check "build/ and .gradle/ ignored" test "$(counter)" = 4
}

test_keystore_kept() {
	new_env keystore
	run || return 1
	before=$(sum "$DATA_DIR/keystore.p12")
	LAN_DOMAIN=b.lan run && LAN_DOMAIN=c.lan run || return 1
	check "keystore unchanged after 3 builds" test "$(sum "$DATA_DIR/keystore.p12")" = "$before" || return 1
	# a pre-existing keystore is used as is
	new_env keystore2
	echo "mine" >"$DATA_DIR/keystore.p12"
	echo "pw" >"$DATA_DIR/keystore.pass"
	run || return 1
	check "existing keystore kept" test "$(cat "$DATA_DIR/keystore.p12")" = mine
}

test_interrupted_build_keeps_previous() {
	new_env interrupted
	run || return 1
	apk=$(sum "$OUT_DIR/tv.apk") json=$(sum "$OUT_DIR/tv.json") hash=$(cat "$DATA_DIR/inputs.sha256")
	echo "class B" >"$SRC_DIR/app/A.kt"
	FAKE_MODE='kill' run
	check "apk unchanged" test "$(sum "$OUT_DIR/tv.apk")" = "$apk" || return 1
	check "json unchanged" test "$(sum "$OUT_DIR/tv.json")" = "$json" || return 1
	check "hash unchanged" test "$(cat "$DATA_DIR/inputs.sha256")" = "$hash" || return 1
	# the code of an unfinished build is spent, never reused
	check "counter burned (2)" test "$(counter)" = 2 || return 1
	FAKE_MODE='fail' run
	check "failed build burns 3" test "$(counter)" = 3 || return 1
	run || return 1
	check "next run builds 4" has '"versionCode": 4[,}]' "$OUT_DIR/tv.json" || return 1
	check "temp build dirs removed" no_files "${TMPDIR:-/tmp}" 'tv-build.*'
}

# shim <name> <script>: a command placed first on PATH, to break one step.
shim() {
	mkdir -p "$t/bin"
	printf '#!/bin/sh\n%s\n' "$2" >"$t/bin/$1"
	chmod +x "$t/bin/$1"
}

test_publish_failure_keeps_previous() {
	new_env pubfail
	export TMPDIR="$t/tmp" && mkdir -p "$TMPDIR"
	run || return 1
	apk=$(sum "$OUT_DIR/tv.apk") json=$(sum "$OUT_DIR/tv.json") hash=$(cat "$DATA_DIR/inputs.sha256")
	echo "class B" >"$SRC_DIR/app/A.kt"
	# copying the APK to the output dir fails (as a full disk would)
	# shellcheck disable=SC2016 # the shim body must stay literal
	shim cp 'for a; do last=$a; done; case $last in *.tv.apk.tmp) exit 1;; esac; exec /bin/cp "$@"'
	rc=0
	PATH="$t/bin:$PATH" run || rc=$?
	check "pass reports failure" test "$rc" != 0 || return 1
	check "not logged as published" test -z "$(grep 'published version' "$t/log")" || return 1
	check "apk unchanged" test "$(sum "$OUT_DIR/tv.apk")" = "$apk" || return 1
	check "json unchanged" test "$(sum "$OUT_DIR/tv.json")" = "$json" || return 1
	check "hash unchanged" test "$(cat "$DATA_DIR/inputs.sha256")" = "$hash" || return 1
	check "code 2 burned" test "$(counter)" = 2 || return 1
	check "no temp files left" no_files "$OUT_DIR" '.*' || return 1
	run || return 1
	check "retry publishes 3" has '"versionCode": 3[,}]' "$OUT_DIR/tv.json" || return 1
	check "apk matches json" has 'PversionCode=3' "$OUT_DIR/tv.apk"
}

test_keystore_failure_stops() {
	new_env kfail
	shim keytool 'exit 1'
	rc=0
	PATH="$t/bin:$PATH" run || rc=$?
	check "pass reports failure" test "$rc" != 0 || return 1
	check "no keystore" test ! -e "$DATA_DIR/keystore.p12" || return 1
	check "no pass file" test ! -e "$DATA_DIR/keystore.pass" || return 1
	check "no gradle call" test "$(calls)" = 0
}

# The loop itself is killed right after tv.apk is moved into place, before
# tv.json: the published pair is torn. Codes must never repeat.
test_killed_between_publish_steps() {
	new_env torn
	export TMPDIR="$t/tmp" && mkdir -p "$TMPDIR"
	run || return 1
	echo "class B" >"$SRC_DIR/app/A.kt"
	# shellcheck disable=SC2016 # the shim body must stay literal
	shim mv 'case $2 in */tv.apk) /bin/mv "$@"; kill -9 $PPID; exit 0;; esac; exec /bin/mv "$@"'
	PATH="$t/bin:$PATH" run
	check "new apk is published (code 2)" has 'PversionCode=2' "$OUT_DIR/tv.apk" || return 1
	check "json still the old one" has '"versionCode": 1[,}]' "$OUT_DIR/tv.json" || return 1
	check "counter covers the published apk" test "$(counter)" = 2 || return 1
	# inputs unchanged since, so without the hash being recorded it rebuilds
	run || return 1
	check "next build is 3, above the published 2" has '"versionCode": 3[,}]' "$OUT_DIR/tv.json" || return 1
	check "apk matches json" has 'PversionCode=3' "$OUT_DIR/tv.apk" || return 1
	check "counter 3" test "$(counter)" = 3
}

test_low_disk_skips() {
	new_env disk
	MIN_FREE_KB=999999999999 run || return 1
	check "no gradle call" test "$(calls)" = 0 || return 1
	check "message" has 'not enough free disk' "$t/log"
}

test_missing_ca_waits() {
	new_env noca
	CA_FILE=$t/nope.crt run || return 1
	check "no gradle call" test "$(calls)" = 0 || return 1
	check "no outputs" test ! -e "$OUT_DIR/tv.apk"
}

test_empty_domain_waits() {
	new_env nodomain
	LAN_DOMAIN='' run || return 1
	check "no gradle call" test "$(calls)" = 0
}

for name in test_first_build_publishes test_unchanged_inputs_skip test_counter_increases \
	test_keystore_kept test_interrupted_build_keeps_previous test_publish_failure_keeps_previous \
	test_keystore_failure_stops test_killed_between_publish_steps test_low_disk_skips \
	test_missing_ca_waits test_empty_domain_waits; do
	if "$name"; then echo "ok   $name"; else
		echo "FAIL $name"
		failures=$((failures + 1))
	fi
done
[ "$failures" -eq 0 ]
