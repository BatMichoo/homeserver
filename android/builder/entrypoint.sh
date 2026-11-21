#!/bin/sh
# Entrypoint of the Android builder image. It starts as root only to make the
# folders the build loop writes usable, then runs the loop as the builder user
# (uid 1000) for good.
#
# Docker creates a missing bind-mount source as root, and the autodeployer
# never creates TV_APP_DIR, so /out can arrive owned by root; on a host whose
# user isn't uid 1000 it belongs to that user instead. Either way the builder
# could not publish. /out is handed to OUT_UID:OUT_GID (the host user, who
# must be able to read and delete what is published); when that owner isn't
# the builder, the folder is opened to its group and the builder joins it.
# /data and the Gradle cache are the builder's own.
set -eu

BUILDER=1000
OUT_UID=${OUT_UID:-1000}
OUT_GID=${OUT_GID:-1000}
OUT_DIR=${OUT_DIR:-/out}
DATA_DIR=${DATA_DIR:-/data}
GRADLE_USER_HOME=${GRADLE_USER_HOME:-/cache/gradle}
LOOP=${LOOP:-/src/builder/build-loop.sh}

# Already unprivileged (docker run --user): nothing to fix or drop.
if [ "$(id -u)" != 0 ]; then
	exec "$LOOP"
fi

if [ "$OUT_GID" = "$BUILDER" ]; then
	groups=--init-groups
else
	groups=--groups=$BUILDER,$OUT_GID
fi
as_builder() { setpriv --reuid="$BUILDER" --regid="$BUILDER" "$groups" -- "$@"; }
writable() { as_builder test -w "$1"; }

mkdir -p "$OUT_DIR" "$DATA_DIR" "$GRADLE_USER_HOME"
# Only touched when broken, so a healthy folder keeps its owner and mode.
for d in "$DATA_DIR" "$GRADLE_USER_HOME"; do
	writable "$d" || chown -R "$BUILDER:$BUILDER" "$d"
done
if ! writable "$OUT_DIR"; then
	chown "$OUT_UID:$OUT_GID" "$OUT_DIR"
	writable "$OUT_DIR" || chmod g+rwx "$OUT_DIR"
fi
for d in "$OUT_DIR" "$DATA_DIR" "$GRADLE_USER_HOME"; do
	writable "$d" || echo "entrypoint: $d is still not writable by uid $BUILDER" >&2
done

# HOME as `USER builder` would have set it (Gradle and keytool look there).
export HOME=/home/builder
exec setpriv --reuid="$BUILDER" --regid="$BUILDER" "$groups" -- "$LOOP"
