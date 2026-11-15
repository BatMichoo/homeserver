# Shortcuts for running the home server. Settings come from .env (see
# .env.template).

SHELLCHECK := $(shell command -v shellcheck 2>/dev/null || echo docker run --rm -v "$(CURDIR)":/mnt -w /mnt koalaman/shellcheck-alpine:v0.10.0 shellcheck)

# How to run compose (make smoke points it at a throwaway project). When
# the deployer holds services on their previous version, its override file
# is included, so `make up` doesn't start a held-back version.
OVERRIDE := .deploy/override.yaml
COMPOSE ?= docker compose $(if $(wildcard $(OVERRIDE)),-f docker-compose.yaml -f $(OVERRIDE))

# Where the certificate goes (compose reads CERTS_DIR too). Created here
# rather than by Docker, so it belongs to you and make clean can empty it.
CERT_FOLDER := $(or $(CERTS_DIR),nginx/certs)
KEY_FILE := $(CERT_FOLDER)/homeserver.lan.key
CRT_FILE := $(CERT_FOLDER)/homeserver.lan.crt

# reload_nginx makes a running nginx load the certificate from disk.
reload_nginx = if [ -n "$$($(COMPOSE) ps -q --status running nginx)" ]; then $(COMPOSE) exec -T nginx nginx -s reload; fi

.PHONY: up down check test smoke theme certs renew-certs ca-backup ca-restore clean \
	autodeploy autodeploy-stop autodeploy-logs deploy-e2e

# The shared look (github.com/BatMichoo/theme) the dashboard uses. Its files
# are copied into www/html/theme/ and committed; `make theme` refreshes them
# after changing this version.
THEME_VERSION := v0.1.0
THEME_FILES := css/tokens.css css/theme.css css/mobile.css css/tablet.css js/theme.js

# up starts everything (issuing the certificate first if needed).
# up and down take the deployer's lock, so they never run in the middle of
# a deploy (and wait up to 10 minutes for one to finish).
LOCK := flock -w 600 .deploy/lock

up:
	@mkdir -p $(CERT_FOLDER) .deploy/public
	$(LOCK) $(COMPOSE) up -d --build

down:
	@mkdir -p .deploy
	$(LOCK) $(COMPOSE) down

# check validates the scripts and configuration without starting anything.
# nginx -t runs against a throwaway certificate.
check:
	$(SHELLCHECK) certs/issue-certs.sh db/setup.sh scripts/*.sh
	docker compose --env-file .env.template config -q
	scripts/check-sites.sh nginx/sites/*.conf scripts/smoke/whoami.conf
	@test -z "$$(gofmt -l auth deploy)" || { gofmt -l auth deploy; echo "run gofmt on the files above"; exit 1; }
	cd auth && go vet ./...
	cd deploy && go vet ./...
	docker compose --env-file .env.template config --format json | (cd deploy && go run ./cmd/deploy lint --project-dir "$(CURDIR)")
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	openssl req -x509 -newkey rsa:2048 -noenc -days 1 -subj /CN=check \
		-keyout "$$tmp/homeserver.lan.key" -out "$$tmp/homeserver.lan.crt" 2>/dev/null && \
	cp "$$tmp/homeserver.lan.crt" "$$tmp/rootCA.crt" && chmod 644 "$$tmp"/* && \
	docker run --rm \
		-v "$(CURDIR)/nginx/nginx.conf:/etc/nginx/nginx.conf:ro" \
		-v "$(CURDIR)/nginx/ssl-params.conf:/etc/nginx/ssl-params.conf:ro" \
		-v "$(CURDIR)/nginx/sites:/etc/nginx/sites:ro" \
		-v "$(CURDIR)/nginx/snippets:/etc/nginx/snippets:ro" \
		-v "$(CURDIR)/nginx/templates:/etc/nginx/templates:ro" \
		-e HTTPS_PORT=443 \
		-v "$$tmp:/etc/nginx/certs:ro" \
		--add-host auth:127.0.0.1 \
		nginx:stable-alpine nginx -t

# test runs the auth service's Go tests against a throwaway Postgres (the
# container is started with --rm, so stopping it removes it).
TEST_DB := homeserver-auth-test
test:
	@docker stop $(TEST_DB) >/dev/null 2>&1 || true
	@docker run -d --rm --name $(TEST_DB) -e POSTGRES_PASSWORD=test -p 127.0.0.1:55433:5432 postgres:16-alpine >/dev/null
	@trap 'docker stop $(TEST_DB) >/dev/null' EXIT; \
	for i in $$(seq 30); do docker exec $(TEST_DB) pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 && break; sleep 1; done; \
	cd auth && TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55433/postgres?sslmode=disable' go test -race -count=1 ./...
	cd deploy && go test -race -count=1 ./...

# deploy-e2e runs the deployer for real on a small fixture project.
deploy-e2e:
	scripts/deploy-e2e.sh

# theme copies the theme's files at THEME_VERSION into www/html/theme/.
theme:
	@for f in $(THEME_FILES); do \
		mkdir -p "www/html/theme/$$(dirname $$f)" && \
		curl -fsSL "https://raw.githubusercontent.com/BatMichoo/theme/$(THEME_VERSION)/static/$$f" -o "www/html/theme/$$f" || exit 1; \
	done
	@echo "$(THEME_VERSION)" > www/html/theme/VERSION
	@echo "Copied theme $(THEME_VERSION) into www/html/theme/."

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

# --- Auto-deploy: deploy main once CI passes (see deploy/ and the README) ---
AUTODEPLOY_ENV := AUTODEPLOY_DIR=$(CURDIR) \
	AUTODEPLOY_UID=$$(id -u) AUTODEPLOY_GID=$$(id -g) \
	DOCKER_GID=$$(stat -c %g /var/run/docker.sock)

autodeploy:
	@mkdir -p .deploy/public
	$(AUTODEPLOY_ENV) docker compose --profile autodeploy up -d --build deployer

autodeploy-stop:
	$(AUTODEPLOY_ENV) docker compose --profile autodeploy stop deployer

autodeploy-logs:
	docker compose --profile autodeploy logs -f deployer
