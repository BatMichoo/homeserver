#!/bin/sh
# Creates each app's database and role (listed in /db/apps) on the running
# Postgres, and sets the role's password from <NAME>_DB_PASSWORD. Safe to
# run on every `up`: existing databases are left alone, and only passwords
# are set again, so a changed password in .env takes effect.
set -eu

status=0
while read -r name; do
	case "$name" in
	'' | '#'*) continue ;;
	esac
	case "$name" in
	*[!a-z0-9_]*)
		echo "db/apps: '$name' must be lowercase letters, digits and _" >&2
		status=1
		continue
		;;
	esac

	var=$(echo "$name" | tr '[:lower:]' '[:upper:]')_DB_PASSWORD
	eval "password=\${$var:-}"
	if [ -z "$password" ]; then
		echo "$var is not set (see .env.template)" >&2
		status=1
		continue
	fi

	exists=$(echo "SELECT 1 FROM pg_roles WHERE rolname = :'name'" |
		psql -v ON_ERROR_STOP=1 -tA -v name="$name")
	# Identifiers and the password go through psql variables, which quote
	# them, so nothing from .env is pasted into SQL text.
	psql -q -v ON_ERROR_STOP=1 -v name="$name" -v password="$password" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN', :'name')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'name') \gexec
ALTER ROLE :"name" WITH LOGIN PASSWORD :'password';
SELECT format('CREATE DATABASE %I OWNER %I', :'name', :'name')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'name') \gexec
SQL
	if [ -n "$exists" ]; then
		echo "ok $name"
	else
		echo "created $name"
	fi
done </db/apps
exit $status
