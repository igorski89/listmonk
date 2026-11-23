#!/usr/bin/env bash
# Parity harness: installs listmonk into a fresh local Postgres, runs a
# scripted API session against it and dumps the database and the mail it
# sent. Run it for two builds and diff the normalised outputs:
#
#   tests/parity/run.sh <binary> <source-root> <outdir>
#   diff -r <outdir-a>/norm <outdir-b>/norm
#
# BACKEND=sqlite runs the build on SQLite (a fresh file in <outdir>)
# instead of the Postgres container.
#
# Needs two local containers (127.0.0.1 only), started by setup.sh:
#   lm-parity-pg   (postgres, port 55432)
#   lm-parity-mail (mailpit, SMTP 51025, API 58025)
# OIDC=1 adds the OIDC session (oidc/session.py), which needs a third
# container, lm-parity-kc (Keycloak, port 58080), started by OIDC=1 setup.sh.
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

BACKEND=${BACKEND:-postgres}
if [ "$BACKEND" = sqlite ]; then
	export LISTMONK_db__driver=sqlite
	export LISTMONK_db__path="$OUT/listmonk.db"
	export PARITY_SQLITE="$OUT/listmonk.db"
fi

# Fresh database and mailbox.
if [ "$BACKEND" = postgres ]; then
	psql -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null
fi
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
if [ "$BACKEND" = sqlite ]; then
	python3 -I -c "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.executescript(open(sys.argv[2]).read()); c.commit()" "$PARITY_SQLITE" "$HERE/settings.sql"
else
	psql <"$HERE/settings.sql" >/dev/null
fi

"$BIN" --config "$HERE/config.toml" >"$OUT/server.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT
for _ in $(seq 50); do
	curl -sf http://127.0.0.1:9123/health >/dev/null && break
	sleep 0.2
done

python3 -I "$HERE/session.py" "$TOKEN" "$OUT"
if [ "${OIDC:-}" = 1 ]; then
	python3 -I "$HERE/oidc/session.py" "$TOKEN" "$OUT"
fi

kill "$PID"
wait "$PID" 2>/dev/null || true
trap - EXIT

python3 -I "$HERE/dump.py" "$OUT/db-final"
python3 -I "$HERE/normalize.py" "$OUT"
echo "parity run done: $OUT/norm"
