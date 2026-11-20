# Homeserver

The manager of the home server: the reverse proxy (nginx), the home
certificate authority (CA) that gives every service a trusted HTTPS
certificate, the login service, and the deployer. Apps are git
submodules (media and Expenser) and know nothing about how they're
hosted.

Right now it runs:

- **certs**: issues nginx's TLS certificate from a private home CA, then
  exits. Runs on every `docker compose up`, before nginx.
- **nginx**: serves the dashboard over HTTPS, hands out the CA certificate
  over HTTP, redirects everything else on HTTP to HTTPS, and asks the auth
  service whether requests to protected sites are logged in.
- **db** and **db-setup**: Postgres for the home server's services.
  `db-setup` creates each service's database and role on every `up`.
- **auth**: the login service at `https://auth.<LAN_DOMAIN>/`.
- **media** (submodule `media/`): films and series at
  `https://media.<LAN_DOMAIN>/`, with **jellyfin** and **transmission**
  behind it (see Media below).
- **expenser** (submodule `expenser/`): the home's expense tracker at
  `https://expenser.<LAN_DOMAIN>/` (see Expenser below).
- **deployer** (opt-in, `make autodeploy`): deploys `main` after CI
  passes, one service at a time, and runs the certificate check monthly.

## Setup

```sh
git submodule update --init   # the apps (media, expenser)
cp .env.template .env         # then fill it in
make up
```

Open `https://<LAN_IP>/` once your device trusts the CA (below).

### Upgrading an existing install

The login service added Postgres. Before pulling it onto a server that
already runs Homeserver, add `POSTGRES_PASSWORD` and `AUTH_DB_PASSWORD`
to `.env` (see `.env.template`): without them `make up` stops at `db` or
`db-setup`, and nginx isn't restarted with the new configuration.

Expenser joined later: add `EXPENSER_DB_PASSWORD` and `EXPENSER_LINK_SECRET`
(and the other `EXPENSER_` settings you want) to `.env` before pulling it,
then `git submodule update --init` and `make up`; without them `db-setup`
stops `make up` the same way.

### Moving from Expenser

Expenser used to run nginx and the home CA itself. Bringing its data and
users over from its own server is in `docs/moving-from-expenser.md`. To
bring over only the CA your devices already trust, before the first
`make up` here:

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
| `MEDIA_DIR` | The media folder on the media drive (default `./media-data`); see Media | `/mnt/media` |
| `MEDIA_UID`, `MEDIA_GID` | Owner of the files in it (default 1000) | `1000` |
| `RENDER_GID` | Group of the Intel GPU device, `stat -c %g /dev/dri/renderD128` | `109` |
| `JELLYFIN_HWACCEL` | `vaapi` (default): convert video on the Intel GPU; `none`: on the CPU | `vaapi` |
| `MEDIA_DB_PASSWORD` | Password of the media app's database role | long random string |
| `JELLYFIN_USER`, `JELLYFIN_PASSWORD` | Jellyfin's service account (the media app creates it) | `media`, long random string |
| `TRANSMISSION_USER`, `TRANSMISSION_PASSWORD` | Transmission's login (only the media app uses it) | `media`, long random string |
| `TRANSMISSION_PEER_PORT` | Port other peers reach Transmission on, open on the host (default 51413) | `51413` |

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

The deployer runs the certificate check every 30 days and has nginx load
a renewed certificate. Without it, run `make certs` now and then: it
issues a new certificate only when the current one expires within 45 days
(or no longer matches `.env`), and has nginx load it.

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

The app also gets the request's cookies, minus the session token
(`homeserver_session`), which only nginx and the auth service see. If
the auth service can't reach its database, protected sites answer with
an error (500) instead of sending people to log in.

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

Add its name to `db/apps` (e.g. `expenser`; not `postgres`, `template0`,
`template1` or `pg_…`), add `EXPENSER_DB_PASSWORD`
to `.env` (and to `db-setup`'s environment in `docker-compose.yaml`),
then `make up`. `db-setup` creates the database and role on the running
server, and sets the password again on every `up`. Changing the
password in `.env` is applied the same way. Each app's role can only
connect to its own database.

## Media

`https://media.<LAN_DOMAIN>/` (the [media](https://github.com/BatMichoo/media)
submodule) is the only way in: browse films and series, play them in the
browser with your place saved across devices, and add a magnet link or a
`.torrent` file (saying whether it's a movie or a series, its title and
year). Finished downloads are filed into the library with hard links, so
nothing is stored twice and the torrent keeps seeding, for 72 hours after
it finishes. The downloads page shows progress and lets you fix a wrong
title (filed again, not downloaded again) or remove a download, with or
without its files.

Behind it, neither reachable from the LAN:

- **Jellyfin** (pinned by digest) reads the library, finds posters and
  descriptions, and converts video a browser can't play, on the Intel GPU
  (`/dev/dri`). The media app sets it up the first time (its account, the
  libraries, the GPU); nobody opens Jellyfin itself.
- **Transmission** (pinned by digest) downloads into the media folder.

The media folder (`MEDIA_DIR`) holds `downloads/` and `library/Movies`,
`library/Shows`; `make up` creates them. It must be **one filesystem with
hard links** (ext4, btrfs or xfs; not exFAT, FAT or NTFS, common on
external drives): otherwise nothing can be filed, and the downloads page
says so. Back up the drive itself; Jellyfin's own data is only a cache of
it.

If videos that need converting stutter or won't play, check `RENDER_GID`
matches `stat -c %g /dev/dri/renderD128`, and as a last resort set
`JELLYFIN_HWACCEL=none` (CPU only, slow on this laptop).

Watching on a TV (Google TV) isn't set up yet: TV browsers mostly don't
trust a home CA.

## Expenser

`https://expenser.<LAN_DOMAIN>/` (the
[Expenser](https://github.com/BatMichoo/Expenser) submodule): house bills,
the car and groceries (typed in or read off receipts by Gemini), recurring
bills with email reminders, and charts. It keeps no accounts: everyone's
expenses are kept by their auth user id. Its settings are the `EXPENSER_`
ones in `.env.template`; without `EXPENSER_SMTP_HOST` reminders are only
logged, and without `EXPENSER_APP_URL` their emails have no links.

The only parts open without login are the snooze links in reminder emails
(signed with `EXPENSER_LINK_SECRET`) and the page's CSS and scripts;
`nginx/sites/expenser.conf` has them in a `public.conf` location.

## Dashboard

`www/html/index.html` is the page at `https://<LAN_IP>/`: a clock with
status chips, a card per app (with a Running/Down badge) and a Server card
(last deploy, services held back, certificate expiry, from the deployer's
`/deploy/status.json`), then how to trust the CA. The badges come from
`/status/<app>`, which nginx answers by asking the app's `/healthz`
(`nginx/sites/default.conf`); a new app gets a card, a `/go/<app>` redirect
and a `/status/<app>` route there. It uses the shared theme
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

## Deploying (the deployer)

On the server, once:

```sh
make autodeploy
```

This starts the `deployer` container (code in `deploy/`). Every 30 minutes
(`DEPLOY_INTERVAL`) it checks GitHub for a new commit on `main`. Pushing to
`main` is the only way anything gets deployed. For each new commit:

1. **It waits for CI.** A commit whose CI failed is skipped for good. If
   GitHub can't be reached, it waits.
2. **It won't touch a modified checkout.** If the checkout on the server
   has local changes, isn't on `main`, or has diverged from it, it does
   nothing and says so.
3. **It backs up** every database of services labelled
   `homeserver.backup: postgres` into `backups/` (newest 14 per database,
   `DEPLOY_BACKUP_KEEP`). If the backup fails, it doesn't deploy, and
   tries again at the next check.
4. **It works out which services the commit changed:** a changed service
   definition, a file in its build folder (a submodule bump counts), or a
   file it mounts.
5. **It deploys only those,** one at a time in dependency order, waiting
   for each one's healthcheck.
   - **A service that fails** goes back to its previous version (image and
     settings) and is listed as **held**. The other services keep their new
     versions. It stays held until a later commit changes it again.
   - **A change only to mounted files** (e.g. an nginx site) is checked
     and reloaded in place when the service says how (labels below).
   - **One-shots** (like `certs`) that changed are run, and the services
     that depend on them are reloaded.
   - **The whole commit is put back** (everything brought up again, the
     commit skipped for good) when the change is shared (networks,
     volumes), a changed one-shot fails, a config check fails, a held
     service's previous version won't start either, or a failing service
     has nothing to go back to.
   - **Building or pulling an image fails** (registry or network down):
     the commit is put back but not skipped, and tried again next time.

Status is in `.deploy/` (not in git):

- `state.json`: what the deployer remembers.
- `override.yaml`: the held services' previous versions. `make up` and
  `make down` use it. **A plain `docker compose up` doesn't**, and would
  start the held-back versions; use `make up`.
- `lock`: taken during a deploy and by `make up` / `make down`, so they
  never overlap.
- `public/status.json`: what the dashboard shows. nginx serves it at
  `/deploy/status.json`.

`state.json` and `override.yaml` contain services' settings (passwords
from `.env`), so only their owner can read them. Unused images are
pruned after each deploy that built or pulled something.

#### `status.json` (version 1)

For the dashboard and future tools (e.g. a notification service). Fields
are only added, never renamed, within version 1.

| Field | Meaning |
|---|---|
| `version` | `1` |
| `updated_at` | when the deployer last wrote it (every poll) |
| `deployed` | `{commit, subject, at}`: the commit last deployed |
| `last_result` | `deployed`, `partial`, `rolled-back`, `rollback-failed`, `build-failed`, `ci-failed`, `blocked` or `backup-failed` |
| `held` | `[{service, commit, since, reason}]`: services on their previous version |
| `cert_expiry` | when the TLS certificate expires, or `null` |
| `events` | the last 20 `{time, kind, commit, service, message}`, oldest first |

- `make autodeploy-logs` shows what it's doing. `make autodeploy-stop`
  stops it.
- Changes to `deploy/` itself take effect after `make autodeploy` again
  (the status shows a "deployer-outdated" event until then).
- **A held service** is retried by pushing a commit that changes it. To
  give up on holding one by hand: stop the deployer, delete
  `.deploy/override.yaml` and the service's entry under `Held` in
  `.deploy/state.json`, run `make up`, then `make autodeploy`.
- **Rolling back a database** isn't automatic. A failed version's
  migrations stay; restore from `backups/` if needed:

  ```sh
  gunzip -c backups/db-<database>-<time>-<commit>.sql.gz | docker compose exec -T db psql -U postgres <database>
  ```

  (into an empty database: drop and recreate it first.)

### Adding a service

Nothing in the deployer changes. In `docker-compose.yaml`:

- **A `healthcheck`** for every service that keeps running (`make check`
  fails without one). One-shots are marked `restart: "no"`.
- **A web UI:** a site file in `nginx/sites/` (see "Protecting a site").
- **Its own database:** a line in `db/apps` and `<NAME>_DB_PASSWORD`.
- **Built from an app repo:** add it as a git submodule (e.g.
  `apps/<name>`) with `build.context` pointing into it. Bumping the
  submodule deploys it.
- **A third-party image:** pin it by digest (`image: name@sha256:…`), so
  changing the pin is what deploys it.
- **Optional labels:**

| Label | Example | Meaning |
|---|---|---|
| `homeserver.check` | `nginx -t` | run before applying a mounted-file change; failure = don't apply |
| `homeserver.reload` | `nginx -s reload` | apply a mounted-file change without recreating the container; mount folders, not single files (`deploy lint` checks) |
| `homeserver.backup` | `postgres` | dump this service's databases before every deploy |
| `homeserver.every` | `720h` | run this one-shot that often, then reload what depends on it |
| `homeserver.tls` | `443` | read the certificate's expiry from this port, for the status |

## Checks

- `make check`: lints the shell scripts, validates the compose file,
  checks every site includes the security headers, runs `gofmt` and
  `go vet` on the auth service and the deployer, checks every long-running
  service has a healthcheck, and runs `nginx -t` against a throwaway
  certificate.
- `make test`: runs the auth service's Go tests against a throwaway
  Postgres container, and the deployer's tests.
- `make deploy-e2e`: runs the real deployer on a small fixture project
  (`deploy/testdata/e2e`) with a local git remote: one service changed,
  one held back, a shared change rolled back, a held service fixed.
- `make smoke`: starts the whole stack under a throwaway project (its own
  volumes, certificate folder and ports 18080/18443) with a test-only
  protected site (`scripts/smoke/whoami.conf`). It checks the
  certificate, the database setup, nginx's answers, and logging in end to
  end, then removes it all.
