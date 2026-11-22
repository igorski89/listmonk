"""Scripted listmonk session for the parity harness.

Calls the API, public pages and webhooks in a fixed order and records every
response (status + body) to <outdir>/api/NNN-name.txt, then the mail that
reached mailpit to <outdir>/mail/. Steps may fail (4xx/5xx): errors are
recorded and compared like successes, since error behaviour is part of what
must not change.

usage: session.py <api-token> <outdir>
"""
import base64
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = "http://127.0.0.1:9123"
MAILPIT = "http://127.0.0.1:58025"
TOKEN = sys.argv[1]
OUT = sys.argv[2]
AUTH = "Basic " + base64.b64encode(f"apiadmin:{TOKEN}".encode()).decode()

os.makedirs(os.path.join(OUT, "api"), exist_ok=True)
os.makedirs(os.path.join(OUT, "mail"), exist_ok=True)
n = 0

# Fields whose values legitimately differ between runs or builds and are
# not database behaviour.
DROP_KEYS = {"uptime", "host", "version", "build", "go_version", "memory", "num_goroutines",
             "time_taken", "loaded_at", "update", "needs_restart", "rss", "system", "allocated",
             "heap", "sys", "num_gc", "size_mb", "cpu", "go"}


def scrub(v):
    if isinstance(v, dict):
        return {k: ("<dropped>" if k in DROP_KEYS else scrub(x)) for k, x in v.items()}
    if isinstance(v, list):
        return [scrub(x) for x in v]
    return v


def call(name, method, path, body=None, form=None, files=None, auth=True, raw=False, record=True):
    """Make a request; record and return (status, parsed body or text)."""
    global n
    url = path if path.startswith("http") else BASE + path
    headers = {}
    data = None
    if auth:
        headers["Authorization"] = AUTH
    if files is not None:
        boundary = "parityboundary0123456789"
        parts = []
        for k, v in (form or {}).items():
            parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
        for k, (fname, content, ctype) in files.items():
            parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"; filename="{fname}"\r\n'
                         f"Content-Type: {ctype}\r\n\r\n".encode() + content + b"\r\n")
        parts.append(f"--{boundary}--\r\n".encode())
        data = b"".join(parts)
        headers["Content-Type"] = f"multipart/form-data; boundary={boundary}"
    elif form is not None:
        data = urllib.parse.urlencode(form, doseq=True).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    elif body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"

    req = urllib.request.Request(url, data=data, method=method, headers=headers)

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *a, **k):
            return None

    opener = urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(req, timeout=30) as r:
            status, text, loc = r.status, r.read(), r.headers.get("Location", "")
    except urllib.error.HTTPError as e:
        status, text, loc = e.code, e.read(), e.headers.get("Location", "")

    text = text.decode("utf-8", errors="replace")
    parsed = text
    try:
        parsed = json.loads(text)
    except ValueError:
        pass

    if record:
        n += 1
        shown = json.dumps(scrub(parsed), indent=1, sort_keys=True) if not isinstance(parsed, str) else parsed
        if raw is False and isinstance(parsed, str) and len(parsed) > 20000:
            shown = parsed[:20000] + "\n<truncated>"
        with open(os.path.join(OUT, "api", f"{n:03d}-{name}.txt"), "w") as f:
            f.write(f"{method} {path}\nstatus {status}\n")
            if loc:
                f.write(f"location {loc}\n")
            f.write("\n" + shown + "\n")
    return status, parsed


def data(r):
    s, p = r
    return p.get("data") if isinstance(p, dict) else None


def wait_for(name, path, pred, timeout=30):
    end = time.time() + timeout
    while time.time() < end:
        s, p = call(name, "GET", path, record=False)
        if pred(p):
            break
        time.sleep(0.3)
    return call(name, "GET", path)


def mail_messages():
    with urllib.request.urlopen(MAILPIT + "/api/v1/messages?limit=500") as r:
        msgs = json.loads(r.read())["messages"]
    out = []
    for m in msgs:
        with urllib.request.urlopen(MAILPIT + f"/api/v1/message/{m['ID']}") as r:
            out.append(json.loads(r.read()))
    return out


def wait_mail(count, timeout=20):
    end = time.time() + timeout
    while time.time() < end:
        with urllib.request.urlopen(MAILPIT + "/api/v1/messages?limit=1") as r:
            if json.loads(r.read())["messages_count"] >= count:
                return
        time.sleep(0.3)


# ---------------------------------------------------------------- after install
for name, path in [("lists", "/api/lists?per_page=all"), ("subscribers", "/api/subscribers"),
                   ("campaigns", "/api/campaigns"), ("templates", "/api/templates"),
                   ("users", "/api/users"), ("roles-users", "/api/roles/users"),
                   ("roles-lists", "/api/roles/lists"), ("dashboard-counts", "/api/dashboard/counts"),
                   ("dashboard-charts", "/api/dashboard/charts"), ("media-empty", "/api/media"),
                   ("about", "/api/about"), ("settings", "/api/settings")]:
    call("init-" + name, "GET", path)

# ---------------------------------------------------------------- lists
l3 = data(call("list-create-public-double", "POST", "/api/lists",
               {"name": "Weekly", "type": "public", "optin": "double", "tags": ["news", "weekly"],
                "description": "the weekly one"}))["id"]
l4 = data(call("list-create-private-single", "POST", "/api/lists",
               {"name": "Customers", "type": "private", "optin": "single", "tags": ["crm"]}))["id"]
l5 = data(call("list-create-to-delete", "POST", "/api/lists",
               {"name": "Temp", "type": "private", "optin": "single", "tags": []}))["id"]
call("list-get", "GET", f"/api/lists/{l3}")
call("list-update", "PUT", f"/api/lists/{l3}",
     {"name": "Weekly news", "type": "public", "optin": "double", "tags": ["news"], "description": ""})
call("list-get-missing", "GET", "/api/lists/999")
call("lists-query-search", "GET", "/api/lists?query=week")
call("lists-query-tag", "GET", "/api/lists?tag=crm")
call("lists-query-type", "GET", "/api/lists?type=private&order_by=name&order=asc")
call("lists-minimal", "GET", "/api/lists?minimal=true&per_page=all")
call("list-delete", "DELETE", f"/api/lists/{l5}")

# ---------------------------------------------------------------- subscribers
s1 = data(call("sub-create-1", "POST", "/api/subscribers",
               {"email": "anna@example.com", "name": "Anna", "status": "enabled",
                "lists": [l3, l4], "attribs": {"city": "Berlin", "plan": "pro", "n": 3}}))["id"]
s2 = data(call("sub-create-2-preconfirm", "POST", "/api/subscribers",
               {"email": "ben@example.com", "name": "Ben", "lists": [l3, l4], "attribs": {"city": "Paris"},
                "preconfirm_subscriptions": True}))["id"]
s3 = data(call("sub-create-3", "POST", "/api/subscribers",
               {"email": "cara@example.com", "name": "Cara", "lists": [l4], "attribs": {}}))["id"]
s4 = data(call("sub-create-4-nolists", "POST", "/api/subscribers",
               {"email": "dan@example.com", "name": "Dan", "lists": [], "attribs": {"city": "Berlin"}}))["id"]
call("sub-create-duplicate", "POST", "/api/subscribers", {"email": "anna@example.com", "name": "Again", "lists": []})
call("sub-create-bad-email", "POST", "/api/subscribers", {"email": "not-an-email", "name": "X", "lists": []})
call("sub-get", "GET", f"/api/subscribers/{s1}")
call("sub-get-missing", "GET", "/api/subscribers/9999")
call("sub-update", "PUT", f"/api/subscribers/{s3}",
     {"email": "cara@example.com", "name": "Cara C", "status": "enabled", "lists": [l4, l3],
      "attribs": {"vip": True}, "preconfirm_subscriptions": True})
call("sub-patch", "PATCH", f"/api/subscribers/{s1}", {"name": "Anna A"})
call("subs-query-all", "GET", "/api/subscribers?per_page=all")
call("subs-query-search", "GET", "/api/subscribers?search=anna")
call("subs-query-list", "GET", f"/api/subscribers?list_id={l4}&order_by=email&order=asc")
call("subs-query-substatus", "GET", f"/api/subscribers?list_id={l3}&subscription_status=confirmed")
call("subs-query-orphans", "GET", "/api/subscribers?subscription_status=none")
call("subs-query-status", "GET", "/api/subscribers?subscriber_status=enabled")
q = urllib.parse.quote
call("subs-query-sql", "GET", "/api/subscribers?query=" + q("subscribers.attribs->>'city' = 'Berlin'"))
call("subs-query-sql-forbidden-table", "GET", "/api/subscribers?query=" + q("(SELECT COUNT(*) FROM users) > 0"))
call("subs-query-sql-write", "GET", "/api/subscribers?query=" + q("1=1; DELETE FROM subscribers"))
call("subs-query-sql-syntax", "GET", "/api/subscribers?query=" + q("subscribers.name = "))
call("subs-export", "GET", "/api/subscribers/export?list_id=" + str(l4))
call("subs-export-sql", "GET", "/api/subscribers/export?query=" + q("subscribers.name LIKE 'A%'"))
call("sub-export-data", "GET", f"/api/subscribers/{s1}/export")
call("sub-activity-empty", "GET", f"/api/subscribers/{s1}/activity")
call("subs-lists-add", "PUT", "/api/subscribers/lists",
     {"ids": [s3, s4], "action": "add", "target_list_ids": [l3], "status": "confirmed"})
call("subs-lists-unsubscribe", "PUT", "/api/subscribers/lists",
     {"ids": [s3], "action": "unsubscribe", "target_list_ids": [l3]})
call("subs-lists-remove", "PUT", "/api/subscribers/lists",
     {"ids": [s4], "action": "remove", "target_list_ids": [l3]})
call("subs-query-lists-add", "PUT", "/api/subscribers/query/lists",
     {"query": "subscribers.attribs->>'city' = 'Berlin'", "list_ids": [], "action": "add",
      "target_list_ids": [l4], "status": "unconfirmed"})
call("subs-query-lists-forbidden", "PUT", "/api/subscribers/query/lists",
     {"query": "subscribers.id IN (SELECT id FROM users)", "action": "add", "target_list_ids": [l4]})
call("subs-after-bulk", "GET", "/api/subscribers?per_page=all")
call("sub-optin-send", "POST", f"/api/subscribers/{s1}/optin")

# ---------------------------------------------------------------- import
csv = ("email,name,attributes\n"
       "eve@example.com,Eve,\"{\"\"city\"\": \"\"Rome\"\"}\"\n"
       "anna@example.com,Anna Imported,\"{\"\"city\"\": \"\"Berlin\"\"}\"\n"
       "finn@example.com,Finn,\"{}\"\n").encode()
call("import-start", "POST", "/api/import/subscribers",
     form={"params": json.dumps({"mode": "subscribe", "subscription_status": "confirmed", "delim": ",",
                                 "lists": [l4], "overwrite": True})},
     files={"file": ("subs.csv", csv, "text/csv")})
wait_for("import-status", "/api/import/subscribers",
         lambda p: isinstance(p, dict) and (p.get("data") or {}).get("status") in ("finished", "failed"))
call("import-logs", "GET", "/api/import/subscribers/logs")
call("import-reset", "DELETE", "/api/import/subscribers")
csv2 = b"email,name,attributes\ngreg@example.com,Greg,\"{}\"\n"
call("import-blocklist", "POST", "/api/import/subscribers",
     form={"params": json.dumps({"mode": "blocklist", "delim": ",", "lists": []})},
     files={"file": ("block.csv", csv2, "text/csv")})
wait_for("import-blocklist-status", "/api/import/subscribers",
         lambda p: isinstance(p, dict) and (p.get("data") or {}).get("status") in ("finished", "failed"))
call("import-reset-2", "DELETE", "/api/import/subscribers")
call("subs-after-import", "GET", "/api/subscribers?per_page=all")

# ---------------------------------------------------------------- templates
t_camp = data(call("tpl-create-campaign", "POST", "/api/templates",
                   {"name": "Parity campaign tpl", "type": "campaign",
                    "body": "<html><body>{{ template \"content\" . }}<p>footer {{ UnsubscribeURL }}</p></body></html>"}))["id"]
t_tx = data(call("tpl-create-tx", "POST", "/api/templates",
                 {"name": "Parity tx", "type": "tx", "subject": "Hi {{ .Subscriber.Name }}",
                  "body": "<p>tx for {{ .Subscriber.Email }}</p>"}))["id"]
call("tpl-get", "GET", f"/api/templates/{t_camp}")
call("tpl-update", "PUT", f"/api/templates/{t_camp}",
     {"name": "Parity campaign tpl 2", "type": "campaign",
      "body": "<html><body>{{ template \"content\" . }}<p>f2 {{ UnsubscribeURL }}</p></body></html>"})
call("tpl-preview", "GET", f"/api/templates/{t_camp}/preview")
call("tpl-list", "GET", "/api/templates")
call("tpl-list-nobody", "GET", "/api/templates?no_body=true")
call("tpl-set-default", "PUT", f"/api/templates/{t_camp}/default")
call("tpl-delete-default", "DELETE", f"/api/templates/{t_camp}")
call("tpl-set-default-back", "PUT", "/api/templates/1/default")
call("tx-send", "POST", "/api/tx", {"subscriber_email": "ben@example.com", "template_id": t_tx, "data": {"x": 1}})

# ---------------------------------------------------------------- media
png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
m1 = data(call("media-upload", "POST", "/api/media", form={"tags": "logo"}, files={"file": ("dot.png", png, "image/png")}))
m1 = m1["id"] if isinstance(m1, dict) else 0
call("media-get", "GET", f"/api/media/{m1}")
call("media-update-tags", "PUT", f"/api/media/{m1}", {"tags": ["logo", "brand"]})
call("media-list", "GET", "/api/media")
call("media-list-tag", "GET", "/api/media?tag=brand")
call("media-tags", "GET", "/api/media/tags")

# ---------------------------------------------------------------- campaigns
body = ('<p>Hello {{ .Subscriber.Name }}</p>'
        '<p><a href="{{ TrackLink "https://example.com/offer" }}">offer</a></p>{{ TrackView }}')
c1 = data(call("camp-create", "POST", "/api/campaigns",
               {"name": "Parity one", "subject": "Hello {{ .Subscriber.FirstName }}", "lists": [l4],
                "from_email": "Parity <parity@example.com>", "content_type": "richtext", "messenger": "email",
                "type": "regular", "tags": ["promo", "q4"], "template_id": t_camp, "body": body,
                "media": [m1], "headers": [{"X-Parity": "1"}]}))["id"]
c2 = data(call("camp-create-2", "POST", "/api/campaigns",
               {"name": "Parity two", "subject": "Two", "lists": [l3], "from_email": "parity@example.com",
                "content_type": "html", "messenger": "email", "type": "regular", "body": "<p>two</p>"}))["id"]
c_opt = data(call("camp-create-optin", "POST", "/api/campaigns",
                  {"name": "Optin camp", "subject": "Confirm", "lists": [l3], "from_email": "parity@example.com",
                   "content_type": "richtext", "messenger": "email", "type": "optin", "body": "<p>confirm</p>"}))
call("camp-get", "GET", f"/api/campaigns/{c1}")
call("camp-update", "PUT", f"/api/campaigns/{c2}",
     {"name": "Parity two b", "subject": "Two b", "lists": [l3, l4], "from_email": "parity@example.com",
      "content_type": "html", "messenger": "email", "type": "regular", "body": "<p>two b</p>", "tags": ["x"]})
call("camp-preview", "GET", f"/api/campaigns/{c1}/preview")
call("camp-list", "GET", "/api/campaigns")
call("camp-list-search", "GET", "/api/campaigns?query=two")
call("camp-list-status", "GET", "/api/campaigns?status=draft&order_by=name&order=asc")
call("camp-list-tag", "GET", "/api/campaigns?tag=promo")
call("camp-status-invalid", "PUT", f"/api/campaigns/{c2}/status", {"status": "paused"})
call("camp-schedule-no-date", "PUT", f"/api/campaigns/{c2}/status", {"status": "scheduled"})
call("camp-archive", "PUT", f"/api/campaigns/{c1}/archive",
     {"archive": True, "archive_slug": "parity-one", "archive_template_id": 1, "archive_meta": {"name": "Reader"}})
call("camp-test", "POST", f"/api/campaigns/{c1}/test",
     {"subscribers": ["anna@example.com"], "name": "Parity one", "subject": "Test", "lists": [l4],
      "from_email": "parity@example.com", "content_type": "richtext", "messenger": "email", "body": body,
      "template_id": t_camp})
call("camp-start", "PUT", f"/api/campaigns/{c1}/status", {"status": "running"})
wait_for("camp-finished", f"/api/campaigns/{c1}",
         lambda p: isinstance(p, dict) and (p.get("data") or {}).get("status") == "finished", timeout=60)
call("camp-running-stats", "GET", "/api/campaigns/running/stats")
call("dashboard-campaigns", "GET", "/api/dashboard/campaigns")

# ---------------------------------------------------------------- tracking (from the sent mail)
time.sleep(1)
msgs = mail_messages()
links, pixels = set(), set()
for m in msgs:
    html = m.get("HTML") or ""
    links.update(re.findall(r'href="(http://127\.0\.0\.1:9123/link/[^"]+)"', html))
    pixels.update(re.findall(r'src="(http://127\.0\.0\.1:9123/campaign/[^"]+/px\.png)"', html))
# Visit in a stable order: by the recipient's e-mail, not by random UUIDs.
emails = {sub["uuid"]: sub["email"]
          for sub in data(call("subs-uuids", "GET", "/api/subscribers?per_page=all", record=False))["results"]}
for i, u in enumerate(sorted(pixels, key=lambda u: emails.get(u.split("/")[-2], u))):
    call(f"track-view-{i}", "GET", u, auth=False)
for i, u in enumerate(sorted(links, key=lambda u: emails.get(u.split("/")[-1], u))):
    call(f"track-click-{i}", "GET", u, auth=False)
call("track-click-bad-link", "GET", "/link/00000000-0000-0000-0000-000000000000/00000000-0000-0000-0000-000000000000/00000000-0000-0000-0000-000000000000", auth=False)
rng = "from=2000-01-01&to=2100-01-01"
for typ in ["views", "clicks", "bounces", "links"]:
    call(f"camp-analytics-{typ}", "GET", f"/api/campaigns/analytics/{typ}?id={c1}&{rng}")
call("camp-analytics-views-unique", "GET", f"/api/campaigns/analytics/views?id={c1}&{rng}&unique=true")
for typ in ["views", "clicks", "subscriptions", "unsubscriptions", "links"]:
    call(f"list-analytics-{typ}", "GET", f"/api/lists/analytics/{typ}?id={l4}&{rng}")
call("sub-activity", "GET", f"/api/subscribers/{s1}/activity")

# ---------------------------------------------------------------- public pages
call("public-lists", "GET", "/api/public/lists", auth=False)
call("public-subscribe", "POST", "/api/public/subscription",
     {"email": "hana@example.com", "name": "Hana", "list_uuids": [data(call("l3-uuid", "GET", f"/api/lists/{l3}", record=False))["uuid"]]},
     auth=False)
hana = data(call("subs-hana", "GET", "/api/subscribers?search=hana", record=False))["results"][0]
call("public-optin-page", "GET", f"/subscription/optin/{hana['uuid']}", auth=False)
call("public-optin-confirm", "POST", f"/subscription/optin/{hana['uuid']}", form={"confirm": "true"}, auth=False)
c1d = data(call("c1", "GET", f"/api/campaigns/{c1}", record=False))
anna = data(call("anna", "GET", f"/api/subscribers/{s1}", record=False))
call("public-sub-page", "GET", f"/subscription/{c1d['uuid']}/{anna['uuid']}", auth=False)
cara = data(call("cara", "GET", f"/api/subscribers/{s3}", record=False))
call("public-unsubscribe", "POST", f"/subscription/{c1d['uuid']}/{cara['uuid']}", form={"unsubscribe": "true"}, auth=False)
call("public-campaign-view", "GET", f"/campaign/{c1d['uuid']}/{anna['uuid']}", auth=False)
call("public-archive", "GET", "/api/public/archive", auth=False)
call("archive-page", "GET", "/archive", auth=False)
call("archive-feed", "GET", "/archive.xml", auth=False)
call("archive-slug", "GET", "/archive/parity-one", auth=False)
call("public-self-export", "POST", f"/subscription/export/{anna['uuid']}", auth=False)

# ---------------------------------------------------------------- bounces
call("bounce-hard", "POST", "/webhooks/bounce",
     {"email": "ben@example.com", "campaign_uuid": c1d["uuid"], "source": "api", "type": "hard",
      "meta": {"reason": "mailbox gone"}})
call("bounce-soft", "POST", "/webhooks/bounce",
     {"email": "eve@example.com", "source": "api", "type": "soft", "meta": {}})
call("bounce-soft-2", "POST", "/webhooks/bounce",
     {"email": "eve@example.com", "source": "api", "type": "soft", "meta": {}})
call("bounce-unknown", "POST", "/webhooks/bounce",
     {"email": "nobody@example.com", "source": "api", "type": "hard", "meta": {}})
call("bounces-list", "GET", "/api/bounces")
call("bounces-list-sorted", "GET", "/api/bounces?order_by=email&order=asc&source=api")
call("bounce-get", "GET", "/api/bounces/1")
call("sub-bounces", "GET", f"/api/subscribers/{s2}/bounces")
call("bounces-blocklist", "PUT", "/api/bounces/blocklist", {"ids": [2]})
call("bounce-delete", "DELETE", "/api/bounces/1")
call("bounces-delete-none", "DELETE", "/api/bounces")
call("bounces-after", "GET", "/api/bounces")
call("sub-bounces-delete", "DELETE", f"/api/subscribers/{s2}/bounces")

# ---------------------------------------------------------------- users, roles
r_user = data(call("role-user-create", "POST", "/api/roles/users",
                   {"name": "Editors", "permissions": ["campaigns:get_all", "subscribers:get_all", "lists:get_all"]}))
r_user = r_user["id"] if isinstance(r_user, dict) else 0
r_list = data(call("role-list-create", "POST", "/api/roles/lists",
                   {"name": "Newsroom", "lists": [{"id": l3, "permissions": ["list:get", "list:manage"]},
                                                  {"id": l4, "permissions": ["list:get"]}]}))
r_list = r_list["id"] if isinstance(r_list, dict) else 0
call("role-user-update", "PUT", f"/api/roles/users/{r_user}",
     {"name": "Editors 2", "permissions": ["campaigns:get_all"]})
call("role-list-update", "PUT", f"/api/roles/lists/{r_list}",
     {"name": "Newsroom 2", "lists": [{"id": l4, "permissions": ["list:get", "list:manage"]}]})
call("role-create-duplicate", "POST", "/api/roles/users", {"name": "Editors 2", "permissions": []})
call("roles-users", "GET", "/api/roles/users")
call("roles-lists", "GET", "/api/roles/lists")
u1 = data(call("user-create", "POST", "/api/users",
               {"username": "editor", "email": "editor@example.com", "name": "Ed", "type": "user",
                "password_login": True, "password": "editorpass123", "user_role_id": r_user,
                "list_role_id": r_list, "status": "enabled"}))
u1 = u1["id"] if isinstance(u1, dict) else 0
call("user-create-duplicate", "POST", "/api/users",
     {"username": "editor", "email": "e2@example.com", "name": "Ed2", "type": "user", "password_login": True,
      "password": "editorpass123", "user_role_id": r_user, "status": "enabled"})
call("user-get", "GET", f"/api/users/{u1}")
call("user-update", "PUT", f"/api/users/{u1}",
     {"username": "editor", "email": "editor2@example.com", "name": "Ed B", "type": "user",
      "password_login": True, "password": "", "user_role_id": r_user, "list_role_id": None, "status": "enabled"})
call("user-demote-last-admin", "PUT", "/api/users/1",
     {"username": "admin", "email": "admin@listmonk", "name": "admin", "type": "user", "password_login": True,
      "password": "", "user_role_id": r_user, "status": "enabled"})
call("users-list", "GET", "/api/users")
call("user-delete-last-admin", "DELETE", "/api/users/1")
call("role-delete", "DELETE", f"/api/roles/{r_list}")
call("user-delete", "DELETE", f"/api/users/{u1}")
call("users-after", "GET", "/api/users")

# ---------------------------------------------------------------- bulk deletes, maintenance
call("subs-blocklist", "PUT", f"/api/subscribers/{s4}/blocklist")
call("subs-query-blocklist", "PUT", "/api/subscribers/query/blocklist",
     {"query": "subscribers.email LIKE 'finn%'", "list_ids": []})
call("subs-query-delete", "POST", "/api/subscribers/query/delete",
     {"query": "subscribers.email = 'greg@example.com'", "list_ids": []})
call("sub-delete", "DELETE", f"/api/subscribers/{s4}")
call("sub-activity-delete", "DELETE", f"/api/subscribers/{s1}/activity/link_clicks")
call("gc-orphans", "DELETE", "/api/maintenance/subscribers/orphan")
call("gc-blocklisted", "DELETE", "/api/maintenance/subscribers/blocklisted")
call("gc-unconfirmed", "DELETE", "/api/maintenance/subscriptions/unconfirmed?before_date=2100-01-01T00:00:00Z")
call("analytics-export-views", "GET", "/api/maintenance/analytics/views/export?since=2000-01-01T00:00:00Z")
call("analytics-export-clicks", "GET", "/api/maintenance/analytics/clicks/export?since=2000-01-01T00:00:00Z")
call("gc-views", "DELETE", "/api/maintenance/analytics/views?before_date=2000-01-01T00:00:00Z")
call("camp-delete", "DELETE", f"/api/campaigns/{c2}")
call("camps-delete-query", "DELETE", "/api/campaigns?query=Optin")
call("lists-delete-query", "DELETE", "/api/lists?query=Temp")
call("tpl-delete", "DELETE", f"/api/templates/{t_tx}")
call("media-delete", "DELETE", f"/api/media/{m1}")

# ---------------------------------------------------------------- final reads
for name, path in [("lists", "/api/lists?per_page=all"), ("subscribers", "/api/subscribers?per_page=all"),
                   ("campaigns", "/api/campaigns"), ("templates", "/api/templates?no_body=true"),
                   ("dashboard-counts", "/api/dashboard/counts"), ("dashboard-charts", "/api/dashboard/charts"),
                   ("list-charts", f"/api/lists/{l4}")]:
    call("final-" + name, "GET", path)

# ---------------------------------------------------------------- mail
wait_mail(1)
time.sleep(1)
msgs = mail_messages()
keyed = []
for m in msgs:
    to = ",".join(sorted(a["Address"] for a in m.get("To") or []))
    keyed.append((m.get("Subject", ""), to, m))
for i, (subj, to, m) in enumerate(sorted(keyed, key=lambda k: (k[0], k[1], k[2].get("Text", "")))):
    with open(os.path.join(OUT, "mail", f"{i:03d}.txt"), "w") as f:
        f.write(f"subject: {subj}\nto: {to}\nfrom: {(m.get('From') or {}).get('Address')}\n\n")
        f.write((m.get("Text") or "") + "\n---\n" + (m.get("HTML") or ""))
