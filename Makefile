# Shortcuts for running the home server. Settings come from .env (see
# .env.template).

SHELLCHECK := $(shell command -v shellcheck 2>/dev/null || echo docker run --rm -v "$(CURDIR)":/mnt -w /mnt koalaman/shellcheck-alpine:v0.10.0 shellcheck)

# How to run compose (make smoke points it at a throwaway project).
COMPOSE ?= docker compose

# Where the certificate goes (compose reads CERTS_DIR too). Created here
# rather than by Docker, so it belongs to you and make clean can empty it.
CERT_FOLDER := $(or $(CERTS_DIR),nginx/certs)
KEY_FILE := $(CERT_FOLDER)/homeserver.lan.key
CRT_FILE := $(CERT_FOLDER)/homeserver.lan.crt

# reload_nginx makes a running nginx load the certificate from disk.
reload_nginx = if [ -n "$$($(COMPOSE) ps -q --status running nginx)" ]; then $(COMPOSE) exec -T nginx nginx -s reload; fi

.PHONY: up down check smoke certs renew-certs ca-backup ca-restore clean

# up starts everything (issuing the certificate first if needed).
up:
	@mkdir -p $(CERT_FOLDER)
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

# check validates the scripts and configuration without starting anything.
# nginx -t runs against a throwaway certificate.
check:
	$(SHELLCHECK) certs/issue-certs.sh scripts/*.sh
	docker compose --env-file .env.template config -q
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	openssl req -x509 -newkey rsa:2048 -noenc -days 1 -subj /CN=check \
		-keyout "$$tmp/homeserver.lan.key" -out "$$tmp/homeserver.lan.crt" 2>/dev/null && \
	cp "$$tmp/homeserver.lan.crt" "$$tmp/rootCA.crt" && chmod 644 "$$tmp"/* && \
	docker run --rm \
		-v "$(CURDIR)/nginx/nginx.conf:/etc/nginx/nginx.conf:ro" \
		-v "$(CURDIR)/nginx/ssl-params.conf:/etc/nginx/ssl-params.conf:ro" \
		-v "$(CURDIR)/nginx/sites:/etc/nginx/sites:ro" \
		-v "$$tmp:/etc/nginx/certs:ro" \
		nginx:stable-alpine nginx -t

# smoke runs the whole stack under a throwaway project and checks it.
smoke:
	scripts/smoke.sh

# TLS certificates are made in the certs container (see certs/issue-certs.sh)
# from LAN_DOMAIN and LAN_IP in .env. `docker compose up` runs it too, so
# these targets are for doing it by hand.

# certs issues the certificate if it is missing, expiring or out of date,
# and has nginx load it.
certs:
	@mkdir -p $(CERT_FOLDER)
	$(COMPOSE) run --rm --build certs
	@$(reload_nginx)

# renew-certs issues a new certificate now and has nginx load it.
renew-certs:
	@mkdir -p $(CERT_FOLDER)
	$(COMPOSE) run --rm --build -e FORCE=1 certs
	@$(reload_nginx)

# ca-backup saves the home CA (keep it private: it can sign any site).
ca-backup:
	$(COMPOSE) build -q certs
	$(COMPOSE) run --rm -T --entrypoint tar certs czf - -C /ca . > ca-backup.tgz
	@chmod 600 ca-backup.tgz
	@echo "Saved the home CA to ca-backup.tgz; keep it somewhere safe and private."

# ca-restore puts a ca-backup.tgz back (e.g. on a new server), so devices
# that trust the old CA keep working.
ca-restore:
	$(COMPOSE) build -q certs
	$(COMPOSE) run --rm -T --entrypoint tar certs xzf - -C /ca < ca-backup.tgz
	@echo "Restored the home CA; run make certs to issue a certificate from it (nginx reloads it)."

# clean removes the server certificate; the home CA (ca-data volume) stays.
clean:
	@rm -f $(KEY_FILE) $(CRT_FILE)
	@echo "Removed the server certificate."
