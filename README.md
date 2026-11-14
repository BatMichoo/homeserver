# Homeserver

The manager of the home server: the reverse proxy (nginx), the home
certificate authority (CA) that gives every service a trusted HTTPS
certificate, the login service, and soon the auto-deployer. Apps
(Expenser, media) join later and know nothing about how they're hosted.

Right now it runs:

- **certs**: issues nginx's TLS certificate from a private home CA, then
  exits. Runs on every `docker compose up`, before nginx.
- **nginx**: serves the dashboard over HTTPS, hands out the CA certificate
  over HTTP, redirects everything else on HTTP to HTTPS, and asks the auth
  service whether requests to protected sites are logged in.
- **db** and **db-setup**: Postgres for the home server's services.
  `db-setup` creates each service's database and role on every `up`.
- **auth**: the login service at `https://auth.<LAN_DOMAIN>/`.

## Setup

```sh
cp .env.template .env   # then fill it in
make up
```

Open `https://<LAN_IP>/` once your device trusts the CA (below).

### Moving from Expenser

Expenser used to run nginx and the home CA itself. To keep the CA your
devices already trust, bring it over before the first `make up` here:

1. In the Expenser checkout: `make ca-backup` (writes `ca-backup.tgz`).
2. Copy `ca-backup.tgz` into this folder and run `make ca-restore`.
3. Stop Expenser's stack (`docker compose down` there) if it runs on this
   machine: its nginx holds ports 80 and 443.
4. `make up`.

Skip step 1 and 2 to start with a new CA instead; every device then has to
trust it once.

If `make up` stops at the certs service, `docker compose logs certs` says
why (usually a missing `LAN_DOMAIN` or `LAN_IP`).

## Settings (`.env`)

| Variable | What it is | Example |
| :--- | :--- | :--- |
| `COUNTRY_ISO` | Country code in the CA's details | `BG` |
| `PROVINCE` | Province or state in the CA's details | `Sofia` |
| `CITY` | City in the CA's details | `Sofia` |
| `ORG_NAME` | Name of the CA (`<ORG_NAME> home CA`) | `Home` |
| `LAN_DOMAIN` | Domain the home server is reached at; apps live on its subdomains | `homeserver.lan` |
| `LAN_IP` | This server's LAN IP | `192.168.1.123` |
| `CERTS_UID`, `CERTS_GID` | Owner of the files in `nginx/certs` (default 1000) | `1000` |
| `POSTGRES_PASSWORD` | Postgres superuser password (only `db-setup` uses it) | long random string |
| `AUTH_DB_PASSWORD` | Password of the auth service's database role | long random string |
| `SESSION_TTL` | How long a login lasts without use (default `720h`, at least `1h`) | `720h` |
| `HTTP_PORT`, `HTTPS_PORT` | Ports nginx listens on (default 80 and 443); redirects keep a non-default HTTPS port | `8443` |

## TLS Certificate

nginx's certificate comes from a private certificate authority (CA) for your home, made and used by the `certs` container (`certs/issue-certs.sh`). `docker compose up` runs it before nginx starts. The first time, it creates the CA in the `ca-data` volume. After that, it issues a new certificate for `LAN_DOMAIN` and `LAN_IP` only when the current one is missing, expires within 45 days, or no longer matches them. Certificates last 397 days.

The certificate covers `LAN_DOMAIN`, its subdomains (e.g. `auth.<LAN_DOMAIN>`), `LAN_IP` and `localhost`.

**Trust the CA once on every device.** Download its public certificate from `http://<LAN_IP>/rootCA.crt` (plain HTTP on purpose, since HTTPS isn't trusted yet). It is also `nginx/certs/rootCA.crt` on the server. To be sure it's yours, compare the SHA-256 fingerprint the phone shows with `openssl x509 -in nginx/certs/rootCA.crt -noout -fingerprint -sha256`.

- **iPhone/iPad:** open the link in Safari (other browsers can't install profiles) and allow the download. Install it under Settings > General > VPN & Device Management, then turn on full trust under Settings > General > About > Certificate Trust Settings.
- **Android:** download it, then go to Settings > Security > Encryption & credentials > Install a certificate > CA certificate and pick the file. Chrome trusts it right away. Firefox needs Settings > About Firefox (tap the logo 5 times), then Secret Settings > "Use third party CA certificates".
- **Desktop:** import it as a trusted authority (Firefox: Settings > Certificates > Authorities > Import).

Renewals don't need this again; only a new CA does.

**Commands:**

- `make certs`: issue a certificate now, if needed.
- `make renew-certs`: issue one now regardless, and restart nginx.
- `make ca-backup` / `make ca-restore`: save the CA to `ca-backup.tgz` and put it back, for example on a new server. Keep the backup private: the CA's key can sign a certificate for any website, and your devices would believe it. If the CA is lost, every device has to trust a new one.

Until the auto-deployer is added, nothing checks the certificate on its
own: run `make certs` now and then. It issues a new certificate only when
the current one expires within 45 days (or no longer matches `.env`), and
has nginx load it.

## Login (auth service)

`https://auth.<LAN_DOMAIN>/` is where people create an account, log in,
and manage their account (display name, email, password, "log out
everywhere else"). Anyone who can reach it can create an account.

- **Session:** logging in sets the cookie `homeserver_session` for
  `LAN_DOMAIN` and all its subdomains, so one login covers every app. It
  lasts `SESSION_TTL` (30 days) since it was last used. Only a hash of it
  is stored, in the auth database. Changing your password logs you out
  everywhere else.
- **Language and theme:** `homeserver_lang` (`en`/`bg`) and
  `homeserver_theme` (`light`/`dark`) cookies, shared by every app.
- **Too many wrong passwords:** after 5 failures for one username in 15
  minutes, the next try is accepted only 1s after the last failure, then
  2s, 4s… up to 30s. Tries inside that wait are refused ("too many
  tries"), parallel ones included.
- **Forgotten password:** there's no email reset. On the server:

  ```sh
  docker compose exec auth auth reset-password <username>
  ```

  It prints a new password once and logs that user out everywhere.

## Protecting a site

An app behind login needs no login code: nginx asks the auth service
about every request, sends people without a session to the login page
(and back afterwards), and passes the user to the app in two headers:

- `X-User-Id`: the user's UUID.
- `X-User-Name`: their display name, else username, percent-encoded
  UTF-8. Decode it with `url.QueryUnescape` (Go).

nginx replaces whatever the client sent in those headers, so the app can
trust them, provided every location that proxies to it includes either
`require-login.conf` (login needed) or `public.conf` (no login; the
headers arrive empty). nginx inherits `proxy_set_header` only into blocks
that set none of their own, so a location, nested ones included, that
includes neither would pass the client's headers through. The app must
also be reachable only through nginx: no published port.

A site file for an app (in `nginx/sites/`):

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name expenser.*;

    include /etc/nginx/snippets/security-headers.conf;
    include /etc/nginx/snippets/auth-server.conf;

    location / {
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        include /etc/nginx/snippets/require-login.conf;
        proxy_pass http://expenser:8080;
    }
}
```

A part of the app open without login gets its own location:

```nginx
    location /public/ {
        proxy_set_header Host $host;
        include /etc/nginx/snippets/public.conf;
        proxy_pass http://expenser:8080;
    }
```

`require-login.conf` sets its own `add_header`, so headers the site adds at
server level (other than the security headers, which it re-adds) must be
repeated in the location. For htmx requests, a missing session answers `401` with
`HX-Redirect`, so htmx loads the login page as a whole page.

## Adding an app's database

Add its name to `db/apps` (e.g. `expenser`), add `EXPENSER_DB_PASSWORD`
to `.env` (and to `db-setup`'s environment in `docker-compose.yaml`),
then `make up`. `db-setup` creates the database and role on the running
server, and sets the password again on every `up`. Changing the
password in `.env` is applied the same way.

## Dashboard

`www/html/index.html` is the page at `https://<LAN_IP>/`: clock, services
(none yet) and how to trust the CA. It uses the shared theme
([BatMichoo/theme](https://github.com/BatMichoo/theme)), whose files are
copied into `www/html/theme/`. To move to a newer theme, change
`THEME_VERSION` in the Makefile and run `make theme`.

## Adding a site

Each site has its own file in `nginx/sites/`. `default.conf` handles
anything no other site claims (the bare IP, unknown names). To add one,
drop a file with a `server` block for its name (e.g.
`server_name auth.homeserver.lan;`) and run `docker compose restart nginx`.
The certificate already covers every subdomain of `LAN_DOMAIN`.

Every `server` block includes the security headers:

```nginx
include /etc/nginx/snippets/security-headers.conf;
```

`make check` fails if one doesn't. A `location` that sets its own
`add_header` must include it again: nginx drops inherited `add_header`
lines as soon as a block sets one. To redirect to HTTPS, use
`https://$host$https_port_suffix$request_uri` so a non-default
`HTTPS_PORT` is kept.

## Checks

- `make check`: lints the shell scripts, validates the compose file,
  checks every site includes the security headers, runs `gofmt` and
  `go vet` on the auth service, and runs `nginx -t` against a throwaway
  certificate.
- `make test`: runs the auth service's Go tests against a throwaway
  Postgres container.
- `make smoke`: starts the whole stack under a throwaway project (its own
  volumes, certificate folder and ports 18080/18443) with a test-only
  protected site (`scripts/smoke/whoami.conf`). It checks the
  certificate, the database setup, nginx's answers, and logging in end to
  end, then removes it all.
