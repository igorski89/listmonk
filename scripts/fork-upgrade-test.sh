#!/usr/bin/env bash
# Upgrade test for release images (SQLite, as SGNL runs it).
#
#   scripts/fork-upgrade-test.sh OLD_IMAGE NEW_IMAGE
#
# Installs OLD_IMAGE on a fresh volume, logs in and creates a list, then
# boots NEW_IMAGE on the same volume (a -beta/-rc build applies pending
# migrations on boot) and checks: it is healthy, login works, the list
# survived, the database passes integrity and foreign key checks, and its
# migration ledger matches a fresh install of NEW_IMAGE.
set -euo pipefail

OLD=$1 NEW=$2
PORT=${PORT:-59000}
B=http://127.0.0.1:$PORT
APP=lm-upgrade-app VOL=lm-upgrade-data FRESH=lm-upgrade-fresh
PASS=adminpass123

cleanup() {
	docker rm -f "$APP" >/dev/null 2>&1 || true
	docker volume rm -f "$VOL" "$FRESH" >/dev/null 2>&1 || true
}
cleanup
TMP=$(mktemp -d)
J=$TMP/cookies
trap 'cleanup; rm -rf "$TMP"' EXIT

# start IMAGE VOLUME CMD: run listmonk on SQLite and wait for /health.
start() {
	docker rm -f "$APP" >/dev/null 2>&1 || true
	docker run -d --name "$APP" -p "127.0.0.1:$PORT:9000" -v "$2:/listmonk/data" \
		-e LISTMONK_app__address=0.0.0.0:9000 -e LISTMONK_db__driver=sqlite \
		-e LISTMONK_db__path=/listmonk/data/listmonk.db \
		-e LISTMONK_ADMIN_USER=admin -e LISTMONK_ADMIN_PASSWORD=$PASS \
		"$1" sh -c "$3" >/dev/null
	for _ in $(seq 60); do
		curl -sf "$B/health" >/dev/null && return 0
		sleep 0.5
	done
	echo "FAIL: $1 did not become healthy"
	docker logs "$APP" 2>&1 | tail -30
	exit 1
}

login() {
	rm -f "$J"
	local nonce
	nonce=$(curl -sf -c "$J" -b "$J" "$B/admin/login?password" |
		grep -o 'name="nonce" value="[^"]*"' | sed 's/.*value="//;s/"$//')
	curl -sf -o /dev/null -c "$J" -b "$J" --data-urlencode username=admin \
		--data-urlencode password=$PASS --data-urlencode "nonce=$nonce" "$B/admin/login"
	curl -sf -b "$J" "$B/api/lists?per_page=all" -o "$TMP/lists.json" ||
		{ echo "FAIL: login on $1"; exit 1; }
}

sql() { docker exec "$APP" sqlite3 /listmonk/data/listmonk.db "$1"; }

echo "== install $OLD"
start "$OLD" "$VOL" './listmonk --install --idempotent --yes && exec ./listmonk'
login "$OLD"
curl -sf -b "$J" -H 'Content-Type: application/json' -X POST "$B/api/lists" \
	-d '{"name":"upgrade-canary","type":"private","optin":"single"}' >/dev/null
docker stop "$APP" >/dev/null

echo "== boot $NEW on the same data"
start "$NEW" "$VOL" 'exec ./listmonk'
login "$NEW"
grep -q '"upgrade-canary"' "$TMP/lists.json" || { echo "FAIL: list lost"; exit 1; }
[ "$(sql 'PRAGMA integrity_check')" = ok ] || { echo "FAIL: integrity_check"; exit 1; }
[ -z "$(sql 'PRAGMA foreign_key_check')" ] || { echo "FAIL: foreign_key_check"; exit 1; }
upgraded=$(sql 'SELECT name FROM schema_migrations ORDER BY name')
docker logs "$APP" 2>&1 | grep -i migrat || true

echo "== fresh install of $NEW"
start "$NEW" "$FRESH" './listmonk --install --idempotent --yes && exec ./listmonk'
fresh=$(sql 'SELECT name FROM schema_migrations ORDER BY name')
if [ "$upgraded" != "$fresh" ]; then
	echo "FAIL: migration ledger differs from a fresh install"
	diff <(echo "$upgraded") <(echo "$fresh") || true
	exit 1
fi
echo "OK: $OLD -> $NEW (ledger: $(echo "$fresh" | tr '\n' ' '))"
