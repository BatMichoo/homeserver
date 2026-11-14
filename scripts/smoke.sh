#!/bin/sh
# Smoke test: runs the stack under a throwaway project with its own volume,
# certificate directory and ports, checks it, and removes it all on exit.
set -eu

cd "$(dirname "$0")/.."

project=homeserver-smoke
tmp=$(mktemp -d)
export CERTS_DIR="$tmp/certs" HTTP_PORT=18080 HTTPS_PORT=18443

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
mk() { make -s "$@" COMPOSE="docker compose -p $project --env-file $tmp/env" >/dev/null 2>&1; }

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
# make certs creates the certificate folder itself, owned by this user (if
# Docker created it, it would be root's and make clean couldn't empty it).
mk certs || fail "certs: first run failed"
[ "$(stat -c %u "$CERTS_DIR")" = "$(id -u)" ] || fail "certs: $CERTS_DIR not owned by $(id -u)"
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

# --- nginx ---------------------------------------------------------------
compose up -d --build --wait nginx >/dev/null 2>&1 || fail "nginx: did not start"
ca="$CERTS_DIR/rootCA.crt"
https() { curl -s --cacert "$ca" --resolve "homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }

# The CA certificate is served over plain HTTP (devices don't trust HTTPS yet).
[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HTTP_PORT/rootCA.crt")" = 200 ] ||
	fail "nginx: /rootCA.crt not served over HTTP"
curl -s "http://127.0.0.1:$HTTP_PORT/rootCA.crt" | cmp -s - "$ca" ||
	fail "nginx: /rootCA.crt is not the CA certificate"

# Everything else over HTTP redirects to HTTPS, same host and path.
redirect=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:$HTTP_PORT/x?y=1")
case "$redirect" in
"301 https://127.0.0.1/x?y=1") ;;
*) fail "nginx: HTTP not redirected to HTTPS ($redirect)" ;;
esac

# The dashboard over HTTPS, trusted through the home CA, by name and by IP.
[ "$(https -o /dev/null -w '%{http_code}' "https://homeserver.lan:$HTTPS_PORT/")" = 200 ] ||
	fail "nginx: dashboard not served over HTTPS"
https "https://homeserver.lan:$HTTPS_PORT/" | grep -q '</html>' || fail "nginx: dashboard body"
[ "$(curl -s --cacert "$ca" -o /dev/null -w '%{http_code}' "https://127.0.0.1:$HTTPS_PORT/")" = 200 ] ||
	fail "nginx: dashboard not served by IP"
# The dashboard uses the shared theme, served locally, and links no app yet.
page=$(https "https://homeserver.lan:$HTTPS_PORT/")
for f in css/tokens.css css/theme.css css/mobile.css css/tablet.css js/theme.js; do
	echo "$page" | grep -q "/theme/$f" || fail "nginx: dashboard doesn't load theme/$f"
	[ "$(https -o /dev/null -w '%{http_code}' "https://homeserver.lan:$HTTPS_PORT/theme/$f")" = 200 ] ||
		fail "nginx: theme/$f not served"
done
echo "$page" | grep -q 'class="header-section"' || fail "nginx: dashboard has no theme header"
if echo "$page" | grep -qi expenser; then fail "nginx: dashboard still mentions Expenser"; fi
if echo "$page" | grep -Eq '(src|href)="https?://'; then fail "nginx: dashboard loads files from another site"; fi
[ "$(https -o /dev/null -w '%{http_code}' "https://homeserver.lan:$HTTPS_PORT/nope")" = 404 ] ||
	fail "nginx: unknown path is not 404"

# make certs also reloads nginx, so a certificate renewed on disk is served.
serial() { openssl x509 -noout -serial; }
compose run --rm -e FORCE=1 certs >/dev/null 2>&1 || fail "certs: forced reissue failed"
mk certs || fail "nginx: make certs failed"
sleep 1 # nginx reloads its workers in the background
served=$(echo | openssl s_client -connect "127.0.0.1:$HTTPS_PORT" -servername homeserver.lan 2>/dev/null | serial)
[ "$served" = "$(serial <"$CERTS_DIR/homeserver.lan.crt")" ] ||
	fail "nginx: still serving the old certificate after make certs ($served)"
echo "nginx: ok"
