#!/bin/sh
# Every server block in nginx/sites/ must include the security headers
# snippet. nginx drops inherited add_header lines as soon as a block sets
# its own, so the headers can't simply live at the http level.
set -eu

cd "$(dirname "$0")/.."

status=0
[ $# -gt 0 ] || set -- nginx/sites/*.conf
for f in "$@"; do
	servers=$(grep -cE '^[[:space:]]*server[[:space:]]*\{' "$f" || true)
	included=$(grep -cE '^[[:space:]]*include[[:space:]]+/etc/nginx/snippets/security-headers\.conf;' "$f" || true)
	if [ "$servers" -ne "$included" ]; then
		echo "$f: $servers server blocks but $included include /etc/nginx/snippets/security-headers.conf" >&2
		status=1
	fi
done
exit $status
