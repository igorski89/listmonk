"""OIDC part of the parity session: logs users in through a local Keycloak
(the browser side scripted with urllib) and records what listmonk answers.
Runs after session.py, against the same server. Self-contained on purpose:
python -I keeps sibling modules off the path.

usage: session.py <api-token> <outdir>
"""
import base64
import html
import http.cookiejar
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = "http://127.0.0.1:9123"
KC = "http://127.0.0.1:58080"
REALM = KC + "/realms/parity"
TOKEN = sys.argv[1]
OUT = os.path.join(sys.argv[2], "api")
AUTH = "Basic " + base64.b64encode(f"apiadmin:{TOKEN}".encode()).decode()
n = 900

# Values that change on every login: tokens, codes, PKCE, Keycloak ids.
SCRUB = [
    (re.compile(r"eyJ[\w-]+\.[\w-]+\.[\w-]+"), "<jwt>"),
    (re.compile(r"\b(code|state|session_state|nonce|code_challenge|iss|execution|tab_id|client_data|session_code)=[^&\"\s]+"), r"\1=<v>"),
]
DROP_KEYS = {"uptime", "version", "needs_restart", "password", "loggedin_at", "created_at", "updated_at"}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


class LoopbackPolicy(http.cookiejar.DefaultCookiePolicy):
    """Browsers treat http://127.0.0.1 as a secure context and send Secure
    cookies to it (Keycloak sets its login cookies Secure even over http)."""
    def return_ok_secure(self, cookie, request):
        return urllib.parse.urlsplit(request.get_full_url()).hostname == "127.0.0.1" or \
            super().return_ok_secure(cookie, request)


def browser():
    jar = http.cookiejar.CookieJar(policy=LoopbackPolicy())
    return urllib.request.build_opener(NoRedirect, urllib.request.HTTPCookieProcessor(jar))


def scrub(v):
    if isinstance(v, dict):
        return {k: ("<dropped>" if k in DROP_KEYS else scrub(x)) for k, x in v.items()}
    if isinstance(v, list):
        return [scrub(x) for x in v]
    return v


def req(method, url, body=None, form=None, auth=None, opener=None):
    headers, data = {}, None
    if auth:
        headers["Authorization"] = auth
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    elif body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    r = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with (opener or browser()).open(r, timeout=30) as resp:
            status, text, loc = resp.status, resp.read(), resp.headers.get("Location", "")
    except urllib.error.HTTPError as e:
        status, text, loc = e.code, e.read(), e.headers.get("Location", "")
    text = text.decode("utf-8", errors="replace")
    try:
        return status, json.loads(text), loc
    except ValueError:
        return status, text, loc


def record(name, method, path, status, body, loc=""):
    global n
    n += 1
    shown = body if isinstance(body, str) else json.dumps(scrub(body), indent=1, sort_keys=True)
    text = f"{method} {path}\nstatus {status}\n" + (f"location {loc}\n" if loc else "") + "\n" + shown + "\n"
    for rx, rep in SCRUB:
        text = rx.sub(rep, text)
    with open(os.path.join(OUT, f"{n:03d}-oidc-{name}.txt"), "w") as f:
        f.write(text)


def api(name, method, path, body=None, opener=None, rec=True):
    s, p, loc = req(method, BASE + path, body=body, auth=None if opener else AUTH, opener=opener)
    if rec:
        record(name, method, path, s, p, loc)
    return s, p


def data(r):
    return r[1].get("data") if isinstance(r[1], dict) else None


def page_error(text):
    """The error a rendered login page shows, or its <title> if none."""
    m = re.search(r'role="alert"[^>]*>\s*(.*?)\s*<', text, re.S)
    if m and m.group(1).strip():
        return "error: " + html.unescape(m.group(1).strip())
    m = re.search(r"<title>(.*?)</title>", text, re.S)
    return "page: " + (html.unescape(m.group(1).strip()) if m else text[:200])


def oidc_login(name, user, password):
    """One browser: listmonk login page -> Keycloak -> callback. Returns the
    browser, logged in or not. Records the callback's outcome."""
    br = browser()
    s, page, _ = req("GET", BASE + "/admin/login", opener=br)
    nonce = re.search(r'name="nonce" value="([^"]+)"', page).group(1)
    s, _, kc_url = req("POST", BASE + "/auth/oidc", form={"nonce": nonce, "next": "/admin/lists"}, opener=br)
    assert s == 302 and kc_url.startswith(REALM), (s, kc_url)
    s, kc_page, _ = req("GET", kc_url, opener=br)
    action = html.unescape(re.search(r'<form[^>]+id="kc-form-login"[^>]+action="([^"]+)"', kc_page).group(1))
    s, kc_resp, cb = req("POST", action, form={"username": user, "password": password, "credentialId": ""}, opener=br)
    assert s == 302 and cb.startswith(BASE + "/auth/oidc"), (s, page_error(kc_resp) if isinstance(kc_resp, str) else kc_resp)
    s, body, loc = req("GET", cb, opener=br)
    record(f"login-{name}", "GET", "/auth/oidc", s, page_error(body) if isinstance(body, str) and s != 302 else "", loc)
    return br


def kc_admin():
    s, tok, _ = req("POST", KC + "/realms/master/protocol/openid-connect/token",
                    form={"grant_type": "password", "client_id": "admin-cli", "username": "admin", "password": "admin"})
    return "Bearer " + tok["access_token"]


def kc_set_groups(username, groups):
    """Set a Keycloak user's groups (the run mutates them; reset every run)."""
    a = kc_admin()
    adm = KC + "/admin/realms/parity"
    uid = req("GET", f"{adm}/users?exact=true&username={username}", auth=a)[1][0]["id"]
    all_groups = {g["name"]: g["id"] for g in req("GET", f"{adm}/groups", auth=a)[1]}
    for g in req("GET", f"{adm}/users/{uid}/groups", auth=a)[1]:
        req("DELETE", f"{adm}/users/{uid}/groups/{g['id']}", auth=a)
    for g in groups:
        req("PUT", f"{adm}/users/{uid}/groups/{all_groups[g]}", auth=a)


def unmask(v):
    """Secrets come back masked; like the admin UI, send "" to keep them."""
    if isinstance(v, dict):
        return {k: unmask(x) for k, x in v.items()}
    if isinstance(v, list):
        return [unmask(x) for x in v]
    if isinstance(v, str) and v and set(v) == {"•"}:
        return ""
    return v


def set_oidc(name, **over):
    """Update security.oidc through the settings API; listmonk reloads itself."""
    st = unmask(data(api(f"{name}-get", "GET", "/api/settings", rec=False)))
    cfg = st["security.oidc"]
    cfg.update(over)
    st["security.oidc"] = cfg
    api(name, "PUT", "/api/settings", st)
    end = time.time() + 30
    time.sleep(2)
    while time.time() < end:
        try:
            s, p = api("", "GET", "/api/settings", rec=False)
            if s == 200 and all(p["data"]["security.oidc"].get(k) == v for k, v in over.items() if k != "client_secret"):
                s2, page, _ = req("GET", BASE + "/admin/login")
                if over.get("enabled") is False or "Keycloak" in page:
                    return
        except Exception:
            pass
        time.sleep(0.3)
    raise SystemExit(f"listmonk did not reload with {name}")


# ---------------------------------------------------------------- setup
kc_set_groups("bob", ["listmgr"])
lst = data(api("lists", "GET", "/api/lists?per_page=all&order_by=id", rec=False))["results"][0]["id"]
r_default = data(api("role-default", "POST", "/api/roles/users", {"name": "OIDC default", "permissions": ["lists:get_all"]}))["id"]
r_editor = data(api("role-editor", "POST", "/api/roles/users",
                    {"name": "OIDC editors", "permissions": ["campaigns:get_all", "subscribers:get_all", "lists:get_all"]}))["id"]
r_lists = data(api("role-lists", "POST", "/api/roles/lists",
                   {"name": "OIDC lists", "lists": [{"id": lst, "permissions": ["list:get", "list:manage"]}]}))["id"]
alice = data(api("user-alice", "POST", "/api/users",
                 {"username": "alice", "email": "alice@example.com", "name": "Alice", "type": "user",
                  "password_login": True, "password": "alicepass123", "user_role_id": r_default,
                  "list_role_id": r_lists, "status": "enabled"}))["id"]
api("user-dave", "POST", "/api/users",
    {"username": "dave", "email": "dave@example.com", "name": "Dave", "type": "user", "password_login": False,
     "user_role_id": r_editor, "status": "disabled"})

roles = [{"claim": "groups", "match": "editors", "user_role_id": r_editor},
         {"claim": "groups", "match": "listmgr", "user_role_id": r_editor, "list_role_id": r_lists},
         {"claim": "preferred_username", "match": "carol", "user_role_id": r_editor}]
set_oidc("settings-oidc-on", enabled=True, provider_name="Keycloak", provider_url=REALM,
         redirect_url=BASE + "/auth/oidc", client_id="listmonk", client_secret="parity-secret",
         auto_create_users=False, auto_sync_roles=False, logout=True, verify_email=True,
         default_user_role_id=r_default, default_list_role_id=None, roles=roles)

# ---------------------------------------------------------------- auto-create and sync off
br = oidc_login("alice", "alice", "alicepass")
api("alice-profile", "GET", "/api/profile", opener=br)
api("alice-lists", "GET", "/api/lists?minimal=true&per_page=all", opener=br)
api("alice-logout", "POST", "/api/logout", opener=br)
api("alice-after-logout", "GET", "/api/profile", opener=br)
oidc_login("frank-no-autocreate", "frank", "frankpass")
oidc_login("dave-disabled", "dave", "davepass")
oidc_login("eve-unverified", "eve", "evepass")
api("users-1", "GET", "/api/users")

# ---------------------------------------------------------------- auto-create and sync on
set_oidc("settings-autocreate", auto_create_users=True, auto_sync_roles=True)
br_frank = oidc_login("frank-autocreate", "frank", "frankpass")
api("frank-profile", "GET", "/api/profile", opener=br_frank)
br_bob = oidc_login("bob-autocreate", "bob", "bobpass")
api("bob-profile", "GET", "/api/profile", opener=br_bob)
api("bob-lists", "GET", "/api/lists?minimal=true&per_page=all", opener=br_bob)
br_carol = oidc_login("carol-string-claim", "carol", "carolpass")
api("carol-profile", "GET", "/api/profile", opener=br_carol)
br_alice = oidc_login("alice-sync", "alice", "alicepass")
api("alice-profile-sync", "GET", "/api/profile", opener=br_alice)
# The role sync must not touch alice's password: password login still works.
br_pw = browser()
s, body, loc = req("POST", BASE + "/admin/login", form={"username": "alice", "password": "alicepass123", "next": "/admin"}, opener=br_pw)
record("alice-password-login", "POST", "/admin/login", s, page_error(body) if s != 302 else "", loc)
api("alice-password-profile", "GET", "/api/profile", opener=br_pw)
oidc_login("eve-unverified-autocreate", "eve", "evepass")
oidc_login("dave-disabled-sync", "dave", "davepass")

# Bob leaves listmgr: the next login falls back to the default role, no list role.
kc_set_groups("bob", [])
br_bob2 = oidc_login("bob-resync", "bob", "bobpass")
api("bob-profile-resync", "GET", "/api/profile", opener=br_bob2)
api("bob-old-session", "GET", "/api/profile", opener=br_bob)
kc_set_groups("bob", ["listmgr"])

api("users-2", "GET", "/api/users")
api("role-delete-referenced", "DELETE", f"/api/roles/{r_editor}")
api("role-delete-referenced-list", "DELETE", f"/api/roles/{r_lists}")
api("frank-logout", "POST", "/api/logout", opener=br_frank)
api("frank-after-logout", "GET", "/api/profile", opener=br_frank)
api("carol-still-in", "GET", "/api/profile", opener=br_carol)

# Leave OIDC off so the final database dump is the same shape as without it.
set_oidc("settings-oidc-off", enabled=False)
