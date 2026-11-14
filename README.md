# Homeserver

The manager of the home server: the reverse proxy (nginx), the home
certificate authority (CA) that gives every service a trusted HTTPS
certificate, and soon the login service and the auto-deployer. Apps
(Expenser, media) join later and know nothing about how they're hosted.

Right now it runs:

- **certs**: issues nginx's TLS certificate from a private home CA, then
  exits. Runs on every `docker compose up`, before nginx.
- **nginx**: serves the dashboard over HTTPS, hands out the CA certificate
  over HTTP, and redirects everything else on HTTP to HTTPS.

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

## Adding a site

Each site has its own file in `nginx/sites/`. `default.conf` handles
anything no other site claims (the bare IP, unknown names). To add one,
drop a file with a `server` block for its name (e.g.
`server_name auth.homeserver.lan;`) and run `docker compose restart nginx`.
The certificate already covers every subdomain of `LAN_DOMAIN`.

## Checks

- `make check`: lints the shell scripts, validates the compose file, and
  runs `nginx -t` against a throwaway certificate.
- `make smoke`: starts the whole stack under a throwaway project (its own
  CA volume, certificate folder and ports 18080/18443), checks the
  certificate and nginx's answers, then removes it all.
