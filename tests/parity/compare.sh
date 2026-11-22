#!/usr/bin/env bash
# Build the baseline (a git ref, default v7.0.0-beta.2) and the working tree,
# run the parity session on both and diff the normalised outputs.
#   tests/parity/compare.sh [baseline-ref] [workdir]
# BACKEND=sqlite runs the working tree on SQLite (the baseline always runs
# on Postgres) and compares API responses and mail only: table dumps differ
# in representation between databases.
# VERSION=v7.0.0-beta.2 builds both binaries with that version string (the
# non-final startup path: migrations re-run on boot, version not recorded).
set -euo pipefail
REF=${1:-v7.0.0-beta.2}
WORK=${2:-${TMPDIR:-/tmp}/listmonk-parity}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
HERE=$ROOT/tests/parity
mkdir -p "$WORK"
if [ ! -d "$WORK/baseline" ]; then
	git -C "$ROOT" worktree add -q "$WORK/baseline" "$REF"
fi
LDFLAGS="-X main.versionString=${VERSION:-}"
(cd "$WORK/baseline" && go build -ldflags "$LDFLAGS" -o "$WORK/listmonk-baseline" ./cmd)
(cd "$ROOT" && go build -ldflags "$LDFLAGS" -o "$WORK/listmonk-new" ./cmd)
[ -f "$WORK/base/norm/.done" ] || { "$HERE/run.sh" "$WORK/listmonk-baseline" "$WORK/baseline" "$WORK/base" >/dev/null 2>&1; touch "$WORK/base/norm/.done"; }
"$HERE/run.sh" "$WORK/listmonk-new" "$ROOT" "$WORK/new" >/dev/null 2>&1
EXCL="-x .done"
if [ "${BACKEND:-postgres}" = sqlite ]; then
	EXCL="$EXCL -x db-install -x db-final"
fi
if diff -r $EXCL "$WORK/base/norm" "$WORK/new/norm" >"$WORK/parity.diff"; then
	echo "PARITY OK"
else
	echo "PARITY DIFF: $(grep -c '^[<>]' "$WORK/parity.diff") lines, see $WORK/parity.diff"
	exit 1
fi
