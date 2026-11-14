# Shortcuts for running the home server. Settings come from .env (see
# .env.template).

SHELLCHECK := $(shell command -v shellcheck 2>/dev/null || echo docker run --rm -v "$(CURDIR)":/mnt -w /mnt koalaman/shellcheck-alpine:v0.10.0 shellcheck)

CERTS_DIR := nginx/certs
KEY_FILE := $(CERTS_DIR)/homeserver.lan.key
CRT_FILE := $(CERTS_DIR)/homeserver.lan.crt

.PHONY: check smoke certs renew-certs ca-backup ca-restore clean

# check validates the scripts and configuration without starting anything.
check:
	$(SHELLCHECK) certs/issue-certs.sh scripts/*.sh
	docker compose --env-file .env.template config -q

# smoke runs the whole stack under a throwaway project and checks it.
smoke:
	scripts/smoke.sh

# TLS certificates are made in the certs container (see certs/issue-certs.sh)
# from LAN_DOMAIN and LAN_IP in .env. `docker compose up` runs it too, so
# these targets are for doing it by hand.

# certs issues the certificate if it is missing, expiring or out of date.
certs:
	docker compose run --rm --build certs

# renew-certs issues a new certificate now and reloads nginx.
renew-certs:
	docker compose run --rm --build -e FORCE=1 certs
	docker compose restart nginx

# ca-backup saves the home CA (keep it private: it can sign any site).
ca-backup:
	docker compose build -q certs
	docker compose run --rm -T --entrypoint tar certs czf - -C /ca . > ca-backup.tgz
	@chmod 600 ca-backup.tgz
	@echo "Saved the home CA to ca-backup.tgz; keep it somewhere safe and private."

# ca-restore puts a ca-backup.tgz back (e.g. on a new server), so devices
# that trust the old CA keep working.
ca-restore:
	docker compose build -q certs
	docker compose run --rm -T --entrypoint tar certs xzf - -C /ca < ca-backup.tgz
	@echo "Restored the home CA; run make certs to issue a certificate from it."

# clean removes the server certificate; the home CA (ca-data volume) stays.
clean:
	@rm -f $(KEY_FILE) $(CRT_FILE)
	@echo "Removed the server certificate."
