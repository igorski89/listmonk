"""Dump every listmonk table, rows in a stable order, one CSV per table.

usage: dump.py <outdir>
"""
import os
import subprocess
import sys

SKIP = {"sessions"}  # random session IDs; covered through the API instead


def psql(sql):
    return subprocess.run(
        ["docker", "exec", "-i", "lm-parity-pg", "psql", "-U", "listmonk", "-d", "listmonk",
         "-v", "ON_ERROR_STOP=1", "-qAt", "-c", sql],
        check=True, capture_output=True, text=True).stdout


def select_list(rel):
    """Columns of a table or view; json columns cast to jsonb so object
    key order (unordered in Postgres' json aggregates) doesn't matter."""
    rows = psql(f"SELECT attname, format_type(atttypid, atttypmod) FROM pg_attribute "
                f"WHERE attrelid = 'public.{rel}'::regclass AND attnum > 0 AND NOT attisdropped "
                f"ORDER BY attnum").splitlines()
    cols = []
    for r in rows:
        name, typ = r.split("|", 1)
        cols.append(f'"{name}"::jsonb AS "{name}"' if typ == "json" else f'"{name}"')
    return ", ".join(cols), len(cols)


def dump(rel, path):
    cols, n = select_list(rel)
    order = ",".join(str(i) for i in range(1, n + 1))
    data = psql(f"COPY (SELECT {cols} FROM {rel} ORDER BY {order}) TO STDOUT WITH CSV HEADER")
    with open(path, "w") as f:
        f.write(data)


def dump_sqlite(path, out):
    """SQLite: every table, rows in a stable order, as CSV."""
    import csv
    import sqlite3
    c = sqlite3.connect(path)
    tables = [r[0] for r in c.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY 1")]
    for t in tables:
        if t in SKIP:
            continue
        cur = c.execute(f'SELECT * FROM "{t}"')
        cols = [d[0] for d in cur.description]
        rows = sorted(cur.fetchall(), key=lambda r: [str(x) for x in r])
        with open(os.path.join(out, t + ".csv"), "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(cols)
            w.writerows(rows)


def main():
    out = sys.argv[1]
    os.makedirs(out, exist_ok=True)
    if os.environ.get("PARITY_SQLITE"):
        return dump_sqlite(os.environ["PARITY_SQLITE"], out)
    tables = psql("SELECT table_name FROM information_schema.tables "
                  "WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY 1").split()
    for t in tables:
        if t not in SKIP:
            dump(t, os.path.join(out, t + ".csv"))

    # Materialized views: the stats the dashboard shows.
    for v in psql("SELECT matviewname FROM pg_matviews WHERE schemaname='public' ORDER BY 1").split():
        dump(v, os.path.join(out, "matview-" + v + ".csv"))


if __name__ == "__main__":
    main()
