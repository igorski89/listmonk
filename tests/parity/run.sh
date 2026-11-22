#!/usr/bin/env bash
# Parity harness: installs listmonk into a fresh local Postgres, runs a
# scripted API session against it and dumps the database and the mail it
# sent. Run it for two builds and diff the normalised outputs:
#
#   tests/parity/run.sh <binary> <source-root> <outdir>
#   diff -r <outdir-a>/norm <outdir-b>/norm
#
# Needs two local containers (127.0.0.1 only), started by setup.sh:
#   lm-parity-pg   (postgres, port 55432)
#   lm-parity-mail (mailpit, SMTP 51025, API 58025)
set -euo pipefail

BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
SRC=$(cd "$2" && pwd)
OUT=$3
HERE=$(cd "$(dirname "$0")" && pwd)
PG=lm-parity-pg

rm -rf "$OUT"
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

psql() { docker exec -i "$PG" psql -U listmonk -d listmonk -v ON_ERROR_STOP=1 -qAt "$@"; }

# Fresh database and mailbox.
psql -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null
curl -sf -X DELETE http://127.0.0.1:58025/api/v1/messages >/dev/null

# The binary runs from a source checkout without a frontend build.
mkdir -p "$SRC/static/admin/dist"
rm -rf "$SRC/uploads"
mkdir -p "$SRC/uploads"
cd "$SRC"

LISTMONK_ADMIN_USER=admin LISTMONK_ADMIN_PASSWORD=adminpass123 LISTMONK_ADMIN_API_USER=apiadmin \
	"$BIN" --config "$HERE/config.toml" --install --yes >"$OUT/install.log" 2>"$OUT/install.err"
TOKEN=$(sed -n 's/.*LISTMONK_ADMIN_API_TOKEN="\(.*\)"/\1/p' "$OUT/install.err")

python3 -I "$HERE/dump.py" "$OUT/db-install"
psql <"$HERE/settings.sql" >/dev/null

"$BIN" --config "$HERE/config.toml" >"$OUT/server.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT
for _ in $(seq 50); do
	curl -sf http://127.0.0.1:9123/health >/dev/null && break
	sleep 0.2
done

python3 -I "$HERE/session.py" "$TOKEN" "$OUT"

kill "$PID"
wait "$PID" 2>/dev/null || true
trap - EXIT

python3 -I "$HERE/dump.py" "$OUT/db-final"
python3 -I "$HERE/normalize.py" "$OUT"
echo "parity run done: $OUT/norm"
