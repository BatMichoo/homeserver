#!/bin/sh
# Smoke test: runs the stack under a throwaway project with its own volume,
# certificate directory and ports, checks it, and removes it all on exit.
set -eu

cd "$(dirname "$0")/.."

project=homeserver-smoke
tmp=$(mktemp -d)
export CERTS_DIR="$tmp/certs" HTTP_PORT=18080 HTTPS_PORT=18443
mkdir -p "$CERTS_DIR"

cat >"$tmp/env" <<ENV
COUNTRY_ISO=BG
PROVINCE=Smoke
CITY=Smoke
ORG_NAME=Smoke
LAN_DOMAIN=homeserver.lan
LAN_IP=127.0.0.1
CERTS_UID=$(id -u)
CERTS_GID=$(id -g)
ENV

compose() { docker compose -p "$project" --env-file "$tmp/env" "$@"; }

cleanup() {
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# --- certs ---------------------------------------------------------------
compose run --rm --build certs >/dev/null 2>&1 || fail "certs: first run failed"
for f in homeserver.lan.crt homeserver.lan.key rootCA.crt; do
	[ -s "$CERTS_DIR/$f" ] || fail "certs: $f missing"
done
san=$(openssl x509 -in "$CERTS_DIR/homeserver.lan.crt" -noout -ext subjectAltName)
echo "$san" | grep -q 'DNS:\*\.homeserver\.lan' || fail "certs: no wildcard in $san"
echo "$san" | grep -q 'IP Address:127\.0\.0\.1' || fail "certs: no IP in $san"

ca_before=$(openssl x509 -in "$CERTS_DIR/rootCA.crt" -noout -fingerprint -sha256)
crt_before=$(cksum <"$CERTS_DIR/homeserver.lan.crt")
compose run --rm certs >/dev/null 2>&1 || fail "certs: second run failed"
[ "$(openssl x509 -in "$CERTS_DIR/rootCA.crt" -noout -fingerprint -sha256)" = "$ca_before" ] ||
	fail "certs: CA changed on second run"
[ "$(cksum <"$CERTS_DIR/homeserver.lan.crt")" = "$crt_before" ] ||
	fail "certs: certificate reissued on second run"

grep -v '^LAN_IP=' "$tmp/env" >"$tmp/env-no-ip"
if docker compose -p "$project" --env-file "$tmp/env-no-ip" run --rm certs >/dev/null 2>&1; then
	fail "certs: ran without LAN_IP"
fi
echo "certs: ok"
