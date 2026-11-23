#!/bin/sh
# The Android builder image's loop (started as the builder user by
# entrypoint.sh): keeps /out/tv.apk in step with the server's own settings.
#
# Every $CHECK_EVERY seconds it hashes everything that goes into the app (the
# sources, the home CA, the LAN domain and the ports). When the hash differs
# from the one of the last published build, it compiles a new APK signed with
# a keystore that is created once in $DATA_DIR, and publishes it together with
# tv.json (read by the web app to offer the download). The version code is one
# above both a counter kept in $DATA_DIR and the published tv.json, so every
# published build can update the previous.
#
# A build that fails or is killed changes nothing: the previous tv.apk, tv.json,
# counter and hash stay as they were and the next pass retries.
set -eu

LAN_DOMAIN=${LAN_DOMAIN:-}
HTTPS_PORT=${HTTPS_PORT:-443}
HTTP_PORT=${HTTP_PORT:-80}
CA_FILE=${CA_FILE:-/ca/rootCA.crt}
DATA_DIR=${DATA_DIR:-/data}
OUT_DIR=${OUT_DIR:-/out}
SRC_DIR=${SRC_DIR:-/src}
CHECK_EVERY=${CHECK_EVERY:-3600}
MIN_FREE_KB=${MIN_FREE_KB:-3145728} # a cold Gradle build needs about 3 GB
OUT_UID=${OUT_UID:-1000}            # the host user, who must be able to read
OUT_GID=${OUT_GID:-1000}            # and delete the published files
GRADLE_CMD=${GRADLE_CMD:-gradle}
GRADLE_USER_HOME=${GRADLE_USER_HOME:-/cache/gradle}
ONCE=${ONCE:-}
READY_FILE=${READY_FILE:-/tmp/ready}

work=
log() { echo "build-loop: $*"; }

cleanup() {
	[ -z "$work" ] || rm -rf "$work"
}
# Remove the temp build dir however we stop; exit explicitly on signals so
# `docker stop` does not have to wait for the sleep.
trap cleanup EXIT
trap 'exit 143' TERM INT

# inputs_hash prints one SHA-256 over every input of the build. Files are
# listed in a fixed order and hashed with their relative paths, so renaming a
# file changes the hash as well as editing it. Left out is what changes
# without the app changing: build output, the docs, this builder's scripts
# and the unit tests. A new hash means a new version code, and every TV is
# then asked to update.
inputs_hash() {
	{
		(
			cd "$SRC_DIR"
			find . -type d \( -name build -o -name .gradle -o -name out -o -path ./builder -o -path ./app/src/test \) -prune \
				-o -type f ! -path ./README.md ! -path ./.dockerignore -print |
				LC_ALL=C sort |
				while IFS= read -r f; do sha256sum "$f"; done
		)
		sha256sum <"$CA_FILE"
		echo "lanDomain=$LAN_DOMAIN httpsPort=$HTTPS_PORT httpPort=$HTTP_PORT"
	} | sha256sum | cut -d' ' -f1
}

# make_keystore creates the signing key if there is none. Both files are
# written under temporary names and moved into place, the keystore last, so
# an interruption never leaves a keystore without its password.
make_keystore() {
	[ ! -e "$DATA_DIR/keystore.p12" ] || return 0
	log "creating the signing keystore"
	(
		umask 077
		od -An -tx1 -N24 /dev/urandom | tr -d ' \n' >"$DATA_DIR/.keystore.pass.tmp" || exit 1
		rm -f "$DATA_DIR/.keystore.p12.tmp"
		keytool -genkeypair -keystore "$DATA_DIR/.keystore.p12.tmp" -storetype PKCS12 \
			-storepass:file "$DATA_DIR/.keystore.pass.tmp" -keypass:file "$DATA_DIR/.keystore.pass.tmp" \
			-alias tv -keyalg RSA -keysize 3072 -validity 36500 \
			-dname "CN=Home Server TV" >/dev/null || exit 1
		mv "$DATA_DIR/.keystore.pass.tmp" "$DATA_DIR/keystore.pass" || exit 1
		mv "$DATA_DIR/.keystore.p12.tmp" "$DATA_DIR/keystore.p12" || exit 1
	)
}

# give_away hands a published file to OUT_UID:OUT_GID when running as root.
# The builder (not root) can't, and doesn't need to: the files are world
# readable and OUT_DIR belongs to the host user (entrypoint.sh), so the host
# user can read and delete them whoever owns them.
give_away() {
	[ "$(id -u)" = 0 ] || return 0
	chown "$OUT_UID:$OUT_GID" "$1" || log "cannot change the owner of $1 to $OUT_UID:$OUT_GID"
}

# write_atomic stores stdin in $1 through a temp file, so a reader never sees
# half a file.
write_atomic() {
	cat >"$1.tmp" && mv "$1.tmp" "$1"
}

# publish moves the built APK and its tv.json into OUT_DIR, apk first, each
# through a temp name. Every step must succeed.
publish() {
	cp "$1" "$OUT_DIR/.tv.apk.tmp" || return 1
	printf '{"versionCode": %s, "versionName": "%s", "apk": "/tv.apk"}\n' "$2" "$3" >"$OUT_DIR/.tv.json.tmp" || return 1
	give_away "$OUT_DIR/.tv.apk.tmp"
	give_away "$OUT_DIR/.tv.json.tmp"
	chmod 644 "$OUT_DIR/.tv.apk.tmp" "$OUT_DIR/.tv.json.tmp" || return 1
	mv "$OUT_DIR/.tv.apk.tmp" "$OUT_DIR/tv.apk" || return 1
	mv "$OUT_DIR/.tv.json.tmp" "$OUT_DIR/tv.json" || return 1
}

# build runs Gradle in a copy of the sources (the sources may be read-only and
# Gradle writes next to them), then publishes the result. Returns 1 on failure.
build() {
	code=$1 name=$2 hash=$3
	# Every step is checked with || return 1: this runs inside `||`, where
	# the shell ignores `set -e`, so a failed step would otherwise be skipped.
	work=$(mktemp -d "${TMPDIR:-/tmp}/tv-build.XXXXXX") || return 1
	cp -a "$SRC_DIR/." "$work/" || return 1
	find "$work" -depth -type d \( -name build -o -name .gradle -o -name out \) -exec rm -rf {} + || return 1

	# The version code is spent before anything is built or published. A
	# crash while publishing then never lets the next build reuse a code that
	# may already be installed on a TV (Android refuses a different APK with
	# the same code). A code lost to a failed build is harmless.
	echo "$code" | write_atomic "$DATA_DIR/counter" || return 1

	log "building version $name"
	if ! (
		cd "$work"
		export GRADLE_OPTS=-Xmx1536m TV_KEYSTORE="$DATA_DIR/keystore.p12"
		TV_KEYSTORE_PASS=$(cat "$DATA_DIR/keystore.pass")
		export TV_KEYSTORE_PASS
		nice -n 19 "$GRADLE_CMD" --no-daemon --max-workers=1 assembleRelease \
			"-PlanDomain=$LAN_DOMAIN" "-PhttpsPort=$HTTPS_PORT" "-PhttpPort=$HTTP_PORT" \
			"-PcaFile=$CA_FILE" "-PversionCode=$code" "-PversionName=$name"
	); then
		log "the build failed; keeping the previous app, will retry"
		return 1
	fi

	apk=$work/app/build/outputs/apk/release/app-release.apk
	if [ ! -s "$apk" ]; then
		log "the build produced no $apk; keeping the previous app, will retry"
		return 1
	fi

	if ! publish "$apk" "$code" "$name"; then
		rm -f "$OUT_DIR/.tv.apk.tmp" "$OUT_DIR/.tv.json.tmp"
		log "could not publish the new app; keeping the previous one, will retry"
		return 1
	fi
	# Recorded last: a crash before this rebuilds on the next pass.
	echo "$hash" | write_atomic "$DATA_DIR/inputs.sha256" || return 1
	log "published version $name"
}

# next_code prints the highest version code already used: the counter or the
# published tv.json, whichever is higher. The counter alone can be behind (a
# lost android-data volume, a restored backup), and a code at or below the
# published one would never install over it.
next_code() {
	counter=$(cat "$DATA_DIR/counter" 2>/dev/null || true)
	published=$(sed -n 's/.*"versionCode": *\([0-9][0-9]*\).*/\1/p' "$OUT_DIR/tv.json" 2>/dev/null | head -n 1)
	case $counter in '' | *[!0-9]*) counter=0 ;; esac
	case $published in '' | *[!0-9]*) published=0 ;; esac
	if [ "$published" -gt "$counter" ]; then echo "$published"; else echo "$counter"; fi
}

# pass does one check and, if the inputs changed, one build. Returns 1 only
# when a build failed.
pass() {
	if [ -z "$LAN_DOMAIN" ]; then
		log "LAN_DOMAIN is not set; waiting"
		return 0
	fi
	if [ ! -s "$CA_FILE" ]; then
		log "$CA_FILE is missing; waiting for the certificate authority"
		return 0
	fi
	for d in "$DATA_DIR" "$OUT_DIR"; do
		if [ ! -d "$d" ] || [ ! -w "$d" ]; then
			log "$d is not writable by uid $(id -u); fix its owner (chown $(id -u) it); waiting"
			return 0
		fi
	done

	hash=$(inputs_hash)
	if [ "$hash" = "$(cat "$DATA_DIR/inputs.sha256" 2>/dev/null || true)" ] &&
		[ -s "$OUT_DIR/tv.apk" ] && [ -s "$OUT_DIR/tv.json" ]; then
		log "up to date"
		return 0
	fi

	mkdir -p "$GRADLE_USER_HOME" 2>/dev/null || true
	free=$(df -Pk "$GRADLE_USER_HOME" | awk 'NR==2 {print $4}')
	if [ "${free:-0}" -lt "$MIN_FREE_KB" ]; then
		log "not enough free disk on $GRADLE_USER_HOME (${free:-0} KB free, $MIN_FREE_KB KB needed); skipping this pass"
		return 0
	fi

	if [ -e "$DATA_DIR/keystore.p12" ] && [ ! -s "$DATA_DIR/keystore.pass" ]; then
		log "$DATA_DIR/keystore.p12 exists but keystore.pass does not; waiting"
		return 0
	fi
	make_keystore || return 1

	code=$(($(next_code) + 1))
	rc=0
	build "$code" "$code-$(echo "$hash" | cut -c1-7)" "$hash" || rc=1
	cleanup
	work=
	return $rc
}

touch "$READY_FILE"
rc=0
while true; do
	rc=0
	pass || rc=$?
	[ -z "$ONCE" ] || exit "$rc"
	sleep "$CHECK_EVERY" &
	wait $!
done
