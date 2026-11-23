"""Normalise a parity run's outputs so two runs can be diffed.

Replaces values that differ between runs by nature (UUIDs, timestamps,
password and token hashes) with stable placeholders. UUIDs are numbered in
order of first appearance across all files, read in a fixed order, so the
same UUID keeps the same placeholder everywhere in one run.

usage: normalize.py <rundir>   (writes <rundir>/norm)
"""
import os
import re
import sys

UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
TS = re.compile(
    r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}(:?\d{2})?)?"
    r"|\b(Mon|Tue|Wed|Thu|Fri|Sat|Sun), \d{1,2} \w{3} \d{4} \d{2}:\d{2}:\d{2} [+-]\d{4}( \(\w+\))?"
    r"|\b\d{1,2} (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\w* \d{4}(, \d{1,2}:\d{2}( [AP]M)?)?"
    r"|\b(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\w* \d{1,2},? \d{4}")
BCRYPT = re.compile(r"\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}")
HEX64 = re.compile(r"\b[0-9a-f]{64}\b")
MSGID = re.compile(r"<[^<>@\s]+@[^<>\s]+>")
BOUNDARY = re.compile(r"(boundary=\"?|--)[0-9a-f]{20,}")
LOGTS = re.compile(r"\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)?")
# Log lines carry source locations (file.go:123) that move with any edit.
SRCLOC = re.compile(r"\b([a-z_]+\.go):\d+")
RENDER = re.compile(r'title="Page render time">[0-9.]+[µm]?s<')
HUMANTS = re.compile(r"\b(Mon|Tue|Wed|Thu|Fri|Sat|Sun), \d{1,2} \w{3} \d{4}, \d{2}:\d{2}:\d{2} \w+")
SYSINFO = re.compile(r'(<dt>(?:Uptime|App memory|Database size)</dt><dd[^>]*>)[^<]*(</dd>)')
NONCE = re.compile(r'(name="nonce" value=")[^"]*"')
ASSETV = re.compile(r"\?v=[0-9a-f]{6,}")
# Permission lists come from Go map iteration: order is random.
PERM_JSON = re.compile(r'\[(\s*"[a-z_]+:[a-z_]+",?\s*)+\]')
PERM_PG = re.compile(r'\{([a-z_]+:[a-z_]+,?)+\}')
# Signed links (upstream after v7.0.0-beta.2): the expiry is a timestamp
# and the signature depends on it and on the install's random key.
SIGNED = re.compile(r"\b(exp=)\d+|\b(sig=)[A-Za-z0-9_-]{20,}")
DURATION = re.compile(r"\"(uptime|time_taken)\":\s*\"?[^,}\"]*\"?")


def main():
    run = sys.argv[1]
    out = os.path.join(run, "norm")
    uuids = {}

    def uu(m):
        v = m.group(0)
        if v not in uuids:
            uuids[v] = f"<uuid-{len(uuids) + 1}>"
        return uuids[v]

    order = ["db-install", "api", "mail", "db-final"]
    for part in order:
        src = os.path.join(run, part)
        if not os.path.isdir(src):
            continue
        for name in sorted(os.listdir(src)):
            with open(os.path.join(src, name), errors="replace") as f:
                s = f.read()
            s = UUID.sub(uu, s)
            s = HUMANTS.sub("<ts>", s)
            s = TS.sub("<ts>", s)
            s = SYSINFO.sub(r"\1<v>\2", s)
            s = BCRYPT.sub("<bcrypt>", s)
            s = HEX64.sub("<hex64>", s)
            s = MSGID.sub("<msgid>", s)
            s = BOUNDARY.sub(r"\1<boundary>", s)
            s = LOGTS.sub("<logts>", s)
            s = ASSETV.sub("?v=<v>", s)
            s = RENDER.sub('title="Page render time"><rt><', s)
            s = NONCE.sub(r'\1<nonce>"', s)
            s = SRCLOC.sub(r"\1:<line>", s)
            s = PERM_JSON.sub(lambda m: "[" + ", ".join(sorted(re.findall(r'"[a-z_]+:[a-z_]+"', m.group(0)))) + "]", s)
            s = PERM_PG.sub(lambda m: "{" + ",".join(sorted(m.group(0)[1:-1].split(","))) + "}", s)
            s = SIGNED.sub(lambda m: (m.group(1) or m.group(2)) + "<v>", s)
            s = DURATION.sub(r'"\1": <dur>', s)
            os.makedirs(os.path.join(out, part), exist_ok=True)
            with open(os.path.join(out, part, name), "w") as f:
                f.write(s)


if __name__ == "__main__":
    main()
