#!/bin/sh
# Smoke test: runs the stack under a throwaway project with its own volume,
# certificate directory and ports, checks it, and removes it all on exit.
set -eu

cd "$(dirname "$0")/.."

# Settings exported in the caller's shell would override the env file below.
unset COUNTRY_ISO PROVINCE CITY ORG_NAME LAN_DOMAIN LAN_IP CERTS_UID CERTS_GID \
	POSTGRES_PASSWORD AUTH_DB_PASSWORD SESSION_TTL \
	COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES

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
POSTGRES_PASSWORD=smoke-root
AUTH_DB_PASSWORD=smoke-auth
ENV

# The smoke override adds a test-only site (scripts/smoke/whoami.conf).
export SMOKE_SITES="$tmp/sites"
mkdir -p "$SMOKE_SITES"
cp nginx/sites/*.conf scripts/smoke/whoami.conf "$SMOKE_SITES/"
files="-f docker-compose.yaml -f scripts/smoke/compose.yaml"
# shellcheck disable=SC2086 # files is a list of flags
compose() { docker compose -p "$project" $files --env-file "$tmp/env" "$@"; }
mk() { make -s "$@" COMPOSE="docker compose -p $project $files --env-file $tmp/env" >/dev/null 2>&1; }

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
# shellcheck disable=SC2086
if docker compose -p "$project" $files --env-file "$tmp/env-no-ip" run --rm certs >/dev/null 2>&1; then
	fail "certs: ran without LAN_IP"
fi
echo "certs: ok"

# --- db --------------------------------------------------------------------
# db-setup creates each app's database and role, and is safe to rerun: it
# only (re)sets passwords from .env.
out=$(compose run --rm db-setup 2>&1) || fail "db: first db-setup failed: $out"
echo "$out" | grep -q 'created auth' || fail "db: auth not created: $out"
out=$(compose run --rm db-setup 2>&1) || fail "db: second db-setup failed: $out"
echo "$out" | grep -q 'ok auth' || fail "db: second run not ok: $out"
if echo "$out" | grep -q created; then fail "db: second run created something: $out"; fi
# Over the network, as the auth service connects (inside the db container
# the image trusts local connections, so any password would work there).
psql_as() {
	compose run --rm -T --no-deps -e PGPASSWORD="$1" --entrypoint psql db-setup \
		-h db -U auth -d auth -tAc 'select 1' >/dev/null 2>&1
}
psql_as smoke-auth || fail "db: auth can't log in"

sed -i 's/^AUTH_DB_PASSWORD=.*/AUTH_DB_PASSWORD=smoke-auth-2/' "$tmp/env"
compose run --rm db-setup >/dev/null 2>&1 || fail "db: db-setup after password change failed"
psql_as smoke-auth-2 || fail "db: new password not applied"
if psql_as smoke-auth; then fail "db: old password still works"; fi

sed 's/^AUTH_DB_PASSWORD=.*/AUTH_DB_PASSWORD=/' "$tmp/env" >"$tmp/env-no-pw"
# shellcheck disable=SC2086
if docker compose -p "$project" $files --env-file "$tmp/env-no-pw" run --rm db-setup >/dev/null 2>&1; then
	fail "db: db-setup ran without AUTH_DB_PASSWORD"
fi
echo "db: ok"

# --- nginx ---------------------------------------------------------------
compose up -d --build --wait nginx >/dev/null 2>&1 || fail "nginx: did not start"
ca="$CERTS_DIR/rootCA.crt"
https() { curl -s --cacert "$ca" --resolve "homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }

# The CA certificate is served over plain HTTP (devices don't trust HTTPS yet).
[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HTTP_PORT/rootCA.crt")" = 200 ] ||
	fail "nginx: /rootCA.crt not served over HTTP"
curl -s "http://127.0.0.1:$HTTP_PORT/rootCA.crt" | cmp -s - "$ca" ||
	fail "nginx: /rootCA.crt is not the CA certificate"

# Everything else over HTTP redirects to HTTPS: same host, path and query,
# and HTTPS_PORT when it isn't 443.
redirect=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:$HTTP_PORT/x?y=1")
case "$redirect" in
"301 https://127.0.0.1:$HTTPS_PORT/x?y=1") ;;
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

# Every response carries the security headers and doesn't name nginx's version.
for url in "http://127.0.0.1:$HTTP_PORT/rootCA.crt" "http://127.0.0.1:$HTTP_PORT/x" \
	"https://homeserver.lan:$HTTPS_PORT/" "https://homeserver.lan:$HTTPS_PORT/nope"; do
	headers=$(https -s -o /dev/null -D - "$url" | tr -d '\r')
	for h in "X-Frame-Options: DENY" "X-Content-Type-Options: nosniff" "X-XSS-Protection: 1; mode=block" "Server: nginx"; do
		echo "$headers" | grep -qx "$h" || fail "nginx: $url lacks '$h'"
	done
done
echo "nginx: ok"

# --- auth -------------------------------------------------------------------
auth_url="https://auth.homeserver.lan:$HTTPS_PORT"
authc() { curl -s --cacert "$ca" --resolve "auth.homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }
[ "$(authc "$auth_url/healthz")" = ok ] || fail "auth: /healthz"
for p in /verify /redirect-to-login; do
	[ "$(authc -o /dev/null -w '%{http_code}' "$auth_url$p")" = 404 ] || fail "auth: $p reachable from outside"
done
echo "auth: up"

# Logging in through auth.<domain>, and a site protected by
# snippets/require-login.conf (whoami.<domain> echoes the user headers it
# receives as "<id>|<name>").
jar="$tmp/jar"
who="https://whoami.homeserver.lan:$HTTPS_PORT"
whoc() { curl -s --cacert "$ca" --resolve "whoami.homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }
post() { authc -b "$jar" -c "$jar" -H "Origin: https://auth.homeserver.lan" -o /dev/null -w '%{http_code} %{redirect_url}' "$@"; }

r=$(whoc -o /dev/null -w '%{http_code} %{redirect_url}' "$who/page?x=1")
[ "$r" = "302 $auth_url/login?next=https%3A%2F%2Fwhoami.homeserver.lan%3A$HTTPS_PORT%2Fpage%3Fx%3D1" ] ||
	fail "auth: anonymous page load not sent to login ($r)"
h=$(whoc -o /dev/null -D - -H 'HX-Request: true' -H "HX-Current-URL: $who/full" "$who/fragment" | tr -d '\r')
echo "$h" | grep -q '^HTTP/[0-9.]* 401' || fail "auth: htmx request not 401"
echo "$h" | grep -qix "HX-Redirect: $auth_url/login?next=https%3A%2F%2Fwhoami.homeserver.lan%3A$HTTPS_PORT%2Ffull" ||
	fail "auth: htmx request has no HX-Redirect to login: $h"
[ "$(whoc -o /dev/null -w '%{http_code}' -H 'X-User-Id: forged' "$who/")" = 302 ] ||
	fail "auth: a forged X-User-Id got through without a session"

r=$(post -d username=smoke-user -d password=secret1 -d confirm_password=secret1 "$auth_url/register")
[ "$r" = "303 $auth_url/account" ] || fail "auth: register ($r)"
body=$(whoc -b "$jar" -H 'X-User-Id: forged' -H 'X-User-Name: forged' "$who/")
id=${body%%|*}
echo "$id" | grep -Eq '^[0-9a-f-]{36}$' || fail "auth: whoami got no user id ($body)"
[ "$body" = "$id|smoke-user" ] || fail "auth: whoami got '$body'"

post -d display_name=Иван -d email= "$auth_url/account" >/dev/null
[ "$(whoc -b "$jar" "$who/")" = "$id|%D0%98%D0%B2%D0%B0%D0%BD" ] || fail "auth: display name not passed encoded"

headers=$(whoc -b "$jar" -s -o /dev/null -D - "$who/" | tr -d '\r')
for hdr in "X-Frame-Options: DENY" "X-Content-Type-Options: nosniff" "X-XSS-Protection: 1; mode=block"; do
	echo "$headers" | grep -qx "$hdr" || fail "auth: whoami lacks '$hdr'"
done

r=$(post -X POST "$auth_url/logout")
[ "$r" = "303 $auth_url/login" ] || fail "auth: logout ($r)"
[ "$(whoc -b "$jar" -o /dev/null -w '%{http_code}' "$who/")" = 302 ] || fail "auth: still in after logout"

r=$(post -d username=smoke-user -d password=wrong "$auth_url/login")
[ "${r%% *}" = 401 ] || fail "auth: wrong password accepted ($r)"
r=$(post -d username=smoke-user -d password=secret1 --data-urlencode next=https://evil.com/ "$auth_url/login")
[ "$r" = "303 $auth_url/account" ] || fail "auth: unsafe next followed ($r)"
r=$(authc -b "$jar" -H "Origin: https://evil.com" -o /dev/null -w '%{http_code}' -X POST "$auth_url/logout")
[ "$r" = 403 ] || fail "auth: cross-site logout allowed ($r)"
echo "auth: ok"
