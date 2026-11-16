#!/bin/sh
# Smoke test: runs the stack under a throwaway project with its own volume,
# certificate directory and ports, checks it, and removes it all on exit.
set -eu

cd "$(dirname "$0")/.."

# Settings exported in the caller's shell would override the env file below.
unset COUNTRY_ISO PROVINCE CITY ORG_NAME LAN_DOMAIN LAN_IP CERTS_UID CERTS_GID \
	POSTGRES_PASSWORD AUTH_DB_PASSWORD SESSION_TTL \
	MEDIA_DIR MEDIA_UID MEDIA_GID RENDER_GID MEDIA_DB_PASSWORD JELLYFIN_USER JELLYFIN_PASSWORD \
	JELLYFIN_HWACCEL TRANSMISSION_USER TRANSMISSION_PASSWORD TRANSMISSION_PEER_PORT \
	EXPENSER_DB_PASSWORD EXPENSER_APP_URL EXPENSER_LINK_SECRET EXPENSER_SMTP_HOST EXPENSER_GEMINI_API_KEY \
	COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES

project=homeserver-smoke
tmp=$(mktemp -d)
export CERTS_DIR="$tmp/certs" HTTP_PORT=18080 HTTPS_PORT=18443
# What the deployer publishes for the dashboard (a sample here).
export DEPLOY_PUBLIC_DIR="$tmp/deploy-public"
mkdir -p "$DEPLOY_PUBLIC_DIR"
cat >"$DEPLOY_PUBLIC_DIR/status.json" <<'JSON'
{"version": 1, "last_result": "partial", "deployed": {"commit": "abc1234", "subject": "smoke", "at": "2026-11-15T10:00:00Z"},
 "held": [{"service": "web", "commit": "abc1234", "since": "2026-11-15T10:00:00Z", "reason": "unhealthy"}],
 "cert_expiry": "2027-12-17T10:00:00Z", "events": [], "updated_at": "2026-11-15T10:00:00Z"}
JSON

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
MEDIA_DIR=$tmp/media
MEDIA_UID=$(id -u)
MEDIA_GID=$(id -g)
MEDIA_DB_PASSWORD=smoke-media
JELLYFIN_USER=media
JELLYFIN_PASSWORD=smoke-jellyfin
TRANSMISSION_USER=media
TRANSMISSION_PASSWORD=smoke-transmission
TRANSMISSION_PEER_PORT=51499
EXPENSER_DB_PASSWORD=smoke-expenser
EXPENSER_LINK_SECRET=smoke-link-secret-0123456789abcdefghij
ENV
mkdir -p "$tmp/media/downloads" "$tmp/media/library/Movies" "$tmp/media/library/Shows"

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
	# Jellyfin and Transmission may leave files owned by other users.
	docker run --rm -v "$tmp:/t" alpine:3.20 rm -rf /t/media >/dev/null 2>&1 || true
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

# An apps list whose last line has no newline still counts; reserved names
# are refused; one app's role can't connect to another app's database.
printf 'auth\nsmokeapp' >"$tmp/apps"
setup_with() {
	compose run --rm -T -v "$tmp/apps:/tmp/apps:ro" -e APPS_FILE=/tmp/apps \
		-e SMOKEAPP_DB_PASSWORD=smoke-app -e POSTGRES_DB_PASSWORD=x db-setup 2>&1
}
out=$(setup_with) || fail "db: db-setup with a second app failed: $out"
echo "$out" | grep -q 'created smokeapp' || fail "db: last line without newline skipped: $out"
if compose run --rm -T --no-deps -e PGPASSWORD=smoke-auth-2 --entrypoint psql db-setup \
	-h db -U auth -d smokeapp -tAc 'select 1' >/dev/null 2>&1; then
	fail "db: the auth role can connect to another app's database"
fi
printf 'auth\npostgres\n' >"$tmp/apps"
if out=$(setup_with); then fail "db: reserved name accepted: $out"; fi
echo "$out" | grep -q postgres || fail "db: no message about the reserved name: $out"
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
# The dashboard uses the shared theme, served locally.
page=$(https "https://homeserver.lan:$HTTPS_PORT/")
for f in css/tokens.css css/theme.css css/mobile.css css/tablet.css js/theme.js; do
	echo "$page" | grep -q "/theme/$f" || fail "nginx: dashboard doesn't load theme/$f"
	[ "$(https -o /dev/null -w '%{http_code}' "https://homeserver.lan:$HTTPS_PORT/theme/$f")" = 200 ] ||
		fail "nginx: theme/$f not served"
done
echo "$page" | grep -q 'class="header-section"' || fail "nginx: dashboard has no theme header"
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

# The deployer's status for the dashboard, never cached.
status_url="https://homeserver.lan:$HTTPS_PORT/deploy/status.json"
https "$status_url" | grep -q '"last_result": "partial"' || fail "nginx: deploy status not served"
headers=$(https -s -o /dev/null -D - "$status_url" | tr -d '\r')
for h in "Cache-Control: no-cache" "X-Frame-Options: DENY" "X-Content-Type-Options: nosniff"; do
	echo "$headers" | grep -qx "$h" || fail "nginx: deploy status lacks '$h'"
done
https "https://homeserver.lan:$HTTPS_PORT/" | grep -q 'id="server-status"' || fail "nginx: dashboard has no server status panel"
page=$(https "https://homeserver.lan:$HTTPS_PORT/")
for app in media expenser auth; do
	echo "$page" | grep -q "data-app=\"$app\"" || fail "nginx: dashboard has no $app card"
	echo "$page" | grep -q "href=\"/go/$app\"" || fail "nginx: dashboard doesn't link /go/$app"
done
echo "$page" | grep -q 'id="device"' || fail "nginx: dashboard has no device section"
echo "nginx: ok"

# --- auth -------------------------------------------------------------------
auth_url="https://auth.homeserver.lan:$HTTPS_PORT"
authc() { curl -s --cacert "$ca" --resolve "auth.homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }
# The health check is for the deployer (inside the network), not the LAN.
[ "$(compose exec -T auth wget -qO- http://127.0.0.1:8080/healthz)" = ok ] || fail "auth: /healthz"
for p in /verify /redirect-to-login /healthz; do
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

# A public location passes no user, whatever the client claims.
r=$(whoc -H 'X-User-Id: forged' -H 'X-User-Name: forged' "$who/public/x")
[ "$r" = "|" ] || fail "auth: forged user headers reached a public location ($r)"

r=$(post -d username=smoke-user -d password=secret1 -d confirm_password=secret1 "$auth_url/register")
[ "$r" = "303 $auth_url/account" ] || fail "auth: register ($r)"
body=$(whoc -b "$jar" -H 'X-User-Id: forged' -H 'X-User-Name: forged' "$who/")
id=${body%%|*}
echo "$id" | grep -Eq '^[0-9a-f-]{36}$' || fail "auth: whoami got no user id ($body)"
[ "$body" = "$id|smoke-user" ] || fail "auth: whoami got '$body'"
# The dashboard's health checks pass no cookies on (the session token stays
# with nginx and auth).
body=$(whoc -H "Cookie: homeserver_session=secret; other=1" "$who/status-check")
[ -z "$body" ] || fail "nginx: the health-check proxy passes cookies on ('$body')"

post -d display_name=Иван -d email= "$auth_url/account" >/dev/null
[ "$(whoc -b "$jar" "$who/")" = "$id|%D0%98%D0%B2%D0%B0%D0%BD" ] || fail "auth: display name not passed encoded"

# The app gets the request's cookies minus the session token.
token=$(awk '$6 == "homeserver_session" {print $7}' "$jar")
[ -n "$token" ] || fail "auth: no session cookie in the jar"
cookies=$(whoc -H "Cookie: a=1; homeserver_session=$token; homeserver_lang=bg" "$who/cookies")
[ "$cookies" = "a=1; homeserver_lang=bg" ] || fail "auth: app got cookies '$cookies'"
cookies=$(whoc -H "Cookie: a=1; homeserver_session=$token; homeserver_lang=bg" "$who/public/cookies")
[ "$cookies" = "a=1; homeserver_lang=bg" ] || fail "auth: a public location passed cookies '$cookies'"

# nginx finds the auth service again after it's recreated.
compose up -d --force-recreate --no-deps --wait auth >/dev/null 2>&1 || fail "auth: recreate failed"
[ "$(whoc -b "$jar" "$who/")" = "$id|%D0%98%D0%B2%D0%B0%D0%BD" ] || fail "auth: protected site broken after auth was recreated"

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
# The dashboard's health routes and its link to the login site (by IP too).
[ "$(https "https://homeserver.lan:$HTTPS_PORT/status/auth")" = ok ] || fail "nginx: /status/auth"
https -s -o /dev/null -D - "https://homeserver.lan:$HTTPS_PORT/status/auth" | tr -d '\r' |
	grep -qx "Cache-Control: no-store" || fail "nginx: /status/auth is cacheable"
r=$(curl -s --cacert "$ca" -o /dev/null -w '%{http_code} %{redirect_url}' "https://127.0.0.1:$HTTPS_PORT/go/auth")
[ "$r" = "302 $auth_url/" ] || fail "nginx: /go/auth by IP ($r)"
echo "auth: ok"

# --- media ------------------------------------------------------------------
# media.<domain> (the media submodule) behind the login; Jellyfin and
# Transmission only reachable by it. No GPU here: conversion on the CPU
# (scripts/smoke/compose.yaml).
compose up -d --build --wait media jellyfin transmission >/dev/null 2>&1 || fail "media: did not start"
media_url="https://media.homeserver.lan:$HTTPS_PORT"
mediac() { curl -s --cacert "$ca" --resolve "media.homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }
[ "$(compose exec -T media wget -qO- http://127.0.0.1:8080/healthz)" = ok ] || fail "media: /healthz"
r=$(mediac -o /dev/null -w '%{http_code} %{redirect_url}' "$media_url/")
[ "$r" = "302 $auth_url/login?next=https%3A%2F%2Fmedia.homeserver.lan%3A$HTTPS_PORT%2F" ] ||
	fail "media: anonymous page load not sent to login ($r)"
r=$(post -d username=smoke-user -d password=secret1 "$auth_url/login")
[ "${r%% *}" = 303 ] || fail "media: login ($r)"
page=$(mediac -b "$jar" "$media_url/")
echo "$page" | grep -q 'class="header-section"' || fail "media: no theme header"
echo "$page" | grep -q '<title>Home · Media</title>' || fail "media: not the media app"
for p in /stream/System/Info /stream/Users/x /healthz; do
	[ "$(mediac -b "$jar" -o /dev/null -w '%{http_code}' "$media_url$p")" = 404 ] || fail "media: $p reachable"
done
for host in jellyfin transmission; do
	page=$(curl -s --cacert "$ca" --resolve "$host.homeserver.lan:$HTTPS_PORT:127.0.0.1" "https://$host.homeserver.lan:$HTTPS_PORT/")
	echo "$page" | grep -q '<title>Home server</title>' || fail "media: $host.<domain> isn't the dashboard"
done
for svc in jellyfin:8096 transmission:9091; do
	if docker port "$(compose ps -q "${svc%:*}")" "${svc#*:}/tcp" >/dev/null 2>&1; then
		fail "media: ${svc%:*}'s port ${svc#*:} is published"
	fi
done
https -s "https://homeserver.lan:$HTTPS_PORT/" | grep -q 'href="/go/media"' || fail "media: the dashboard doesn't link to it"
r=$(https -o /dev/null -w '%{http_code} %{redirect_url}' "https://homeserver.lan:$HTTPS_PORT/go/media")
[ "$r" = "302 $media_url/" ] || fail "media: /go/media doesn't lead to it ($r)"
# A .torrent of nearly 10 MB reaches the app (which answers), not nginx's 413.
head -c $((10 * 1024 * 1024 - 1024)) /dev/zero >"$tmp/big.torrent"
code=$(mediac -b "$jar" -H "Origin: $media_url" -o /dev/null -w '%{http_code}' -F kind=movie -F title=Big -F year=2000 \
	-F "torrent=@$tmp/big.torrent" "$media_url/add")
[ "$code" != 413 ] || fail "media: nginx refuses a 10 MB .torrent"
[ "$(https "https://homeserver.lan:$HTTPS_PORT/status/media")" = ok ] || fail "media: /status/media"
# A stopped app shows as down on the dashboard, quickly.
compose stop media >/dev/null 2>&1
start=$(date +%s)
code=$(https -o /dev/null -w '%{http_code}' "https://homeserver.lan:$HTTPS_PORT/status/media")
[ "$code" != 200 ] || fail "media: /status/media says ok while stopped"
[ $(($(date +%s) - start)) -le 5 ] || fail "media: /status/media took over 5s while stopped"
compose start media >/dev/null 2>&1
echo "media: ok"

# --- expenser ---------------------------------------------------------------
# expenser.<domain> (the expenser submodule) behind the login; only its
# snooze links and static files open without one.
compose up -d --build --wait expenser >/dev/null 2>&1 || fail "expenser: did not start"
exp_url="https://expenser.homeserver.lan:$HTTPS_PORT"
expc() { curl -s --cacert "$ca" --resolve "expenser.homeserver.lan:$HTTPS_PORT:127.0.0.1" "$@"; }
[ "$(compose exec -T expenser wget -qO- http://127.0.0.1:8080/healthz)" = ok ] || fail "expenser: /healthz"
r=$(expc -o /dev/null -w '%{http_code} %{redirect_url}' "$exp_url/bills")
[ "$r" = "302 $auth_url/login?next=https%3A%2F%2Fexpenser.homeserver.lan%3A$HTTPS_PORT%2Fbills" ] ||
	fail "expenser: anonymous page load not sent to login ($r)"
page=$(expc -b "$jar" "$exp_url/")
echo "$page" | grep -q 'class="header-section"' || fail "expenser: no theme header"
echo "$page" | grep -q '<title>Expense Tracker</title>' || fail "expenser: not Expenser"
echo "$page" | grep -q 'data-path="/bills"' || fail "expenser: no navigation for the logged-in user"
[ "$(expc -b "$jar" -o /dev/null -w '%{http_code}' "$exp_url/healthz")" = 404 ] || fail "expenser: /healthz reachable"
# A snooze link opens without login (a bad one says so), styled.
page=$(expc "$exp_url/snooze/not-a-token")
echo "$page" | grep -q 'header-section' || fail "expenser: snooze link sent to login"
css=$(echo "$page" | grep -o '/static/expenser/css/styles.css?v=[0-9a-z]*' | head -1)
[ -n "$css" ] || fail "expenser: snooze page has no stylesheet"
[ "$(expc -o /dev/null -w '%{http_code}' "$exp_url$css")" = 200 ] || fail "expenser: its CSS needs a login ($css)"
# Its port isn't published.
if docker port "$(compose ps -q expenser)" 8080/tcp >/dev/null 2>&1; then fail "expenser: port 8080 is published"; fi
r=$(https -o /dev/null -w '%{http_code} %{redirect_url}' "https://homeserver.lan:$HTTPS_PORT/go/expenser")
[ "$r" = "302 $exp_url/" ] || fail "expenser: /go/expenser doesn't lead to it ($r)"
[ "$(https "https://homeserver.lan:$HTTPS_PORT/status/expenser")" = ok ] || fail "expenser: /status/expenser"
echo "expenser: ok"
