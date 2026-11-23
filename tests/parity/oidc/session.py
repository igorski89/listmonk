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
                # ?password: the form even with auto-login on (fork only;
                # older builds ignore the parameter).
                s2, page, _ = req("GET", BASE + "/admin/login?password")
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

# ---------------------------------------------------------------- auto-login (fork only)
# Self-checking (asserts): beta.2 has no auto-login, so it can't be the
# oracle. Runs only with OIDC_AUTOLOGIN=1, never in compare.sh's baseline run.
if os.environ.get("OIDC_AUTOLOGIN") == "1":
    def cookies(br):
        jar = next(h for h in br.handlers if isinstance(h, urllib.request.HTTPCookieProcessor)).cookiejar
        return {c.name: c.value for c in jar if c.domain.startswith("127.0.0.1") and ":58080" not in c.domain and c.path == "/"}

    def to_callback(br, kc_url, user, password):
        """Keycloak: log in (or ride its SSO session); return the callback URL."""
        s, kc_page, loc = req("GET", kc_url, opener=br)
        if s == 302 and loc.startswith(BASE + "/auth/oidc"):
            return loc, True
        action = html.unescape(re.search(r'<form[^>]+id="kc-form-login"[^>]+action="([^"]+)"', kc_page).group(1))
        s, _, cb = req("POST", action, form={"username": user, "password": password, "credentialId": ""}, opener=br)
        assert s == 302 and cb.startswith(BASE + "/auth/oidc"), (s, cb)
        return cb, False

    def is_form(r):
        s, body, loc = r
        return s == 200 and isinstance(body, str) and 'name="password"' in body

    set_oidc("settings-autologin-on", auto_login=True)
    s, p = api("autologin-settings", "GET", "/api/settings", rec=False)
    assert p["data"]["security.oidc"]["auto_login"] is True

    # 1. The login page goes straight to Keycloak, keeping next=.
    br = browser()
    s, _, kc_url = req("GET", BASE + "/admin/login?next=/admin/campaigns", opener=br)
    assert s == 302 and kc_url.startswith(REALM + "/protocol/openid-connect/auth"), (s, kc_url)
    assert "code_challenge=" in kc_url, "PKCE missing"
    assert cookies(br).get("nonce"), "no nonce cookie"
    cb, sso = to_callback(br, kc_url, "alice", "alicepass")
    assert not sso
    s, _, loc = req("GET", cb, opener=br)
    assert (s, loc) == (302, "/admin/campaigns"), (s, loc)
    s, p, _ = req("GET", BASE + "/api/profile", opener=br)
    assert s == 200 and p["data"]["username"] == "alice", (s, p)
    # Right after a login, auto-login pauses: the login page is the form.
    assert cookies(br).get("oidc_autologin_pause") == "1", cookies(br)
    assert is_form(req("GET", BASE + "/admin/login", opener=br))
    record("auto-1-straight-to-idp", "GET", "/admin/login?next=/admin/campaigns", 302, "ok")

    # 1b. The plain link (no next=) lands on /admin.
    brp = browser()
    s, _, kc_url = req("GET", BASE + "/admin/login", opener=brp)
    assert s == 302 and kc_url.startswith(REALM), (s, kc_url)
    cb, _ = to_callback(brp, kc_url, "frank", "frankpass")
    s, _, loc = req("GET", cb, opener=brp)
    assert (s, loc) == (302, "/admin"), (s, loc)

    # 1c. next= pointing at the login page itself returns to /admin, not
    # into another login.
    for bad in ["/admin/login", "/admin/login?password", "/admin/login/", "/auth/oidc?code=x"]:
        brl = browser()
        s, _, kc_url = req("GET", BASE + "/admin/login?next=" + urllib.parse.quote(bad), opener=brl)
        assert s == 302, (bad, s)
        st = urllib.parse.parse_qs(urllib.parse.urlsplit(kc_url).query)["state"][0]
        nxt = json.loads(base64.urlsafe_b64decode(st + "=" * (-len(st) % 4)))["next"]
        assert nxt == "/admin", (bad, nxt)
    record("auto-1b-plain-link-and-next-login", "GET", "/admin/login", 302, "ok")

    # 2. A deep link without a session: the app redirects to the login page,
    # which goes to Keycloak; Keycloak's SSO session brings the user back.
    br2 = browser()
    s, _, loc = req("GET", BASE + "/admin/subscribers", opener=br2)
    assert s in (302, 307) and "/admin/login" in loc, (s, loc)
    s, _, kc_url = req("GET", loc if loc.startswith("http") else BASE + loc, opener=br2)
    assert s == 302 and kc_url.startswith(REALM), (s, kc_url)
    cb, _ = to_callback(br2, kc_url, "carol", "carolpass")
    s, _, loc = req("GET", cb, opener=br2)
    assert (s, loc) == (302, "/admin/subscribers"), (s, loc)
    record("auto-2-deep-link", "GET", "/admin/subscribers", 302, "ok")

    # 3. After logout the login page shows the form, though Keycloak's
    # session would log the user straight back in.
    s, p, _ = req("POST", BASE + "/api/logout", opener=br)
    assert s == 200 and cookies(br).get("oidc_autologin_pause") == "1", (s, cookies(br))
    assert p["redirect_url"].startswith(REALM + "/protocol/openid-connect/logout"), p
    r = req("GET", BASE + "/admin/login", opener=br)
    assert is_form(r), r[:1] + r[2:]
    # The SSO button still works (rides Keycloak's session) and clears the pause.
    nonce = re.search(r'name="nonce" value="([^"]+)"', r[1]).group(1)
    s, _, kc_url = req("POST", BASE + "/auth/oidc", form={"nonce": nonce, "next": "/admin"}, opener=br)
    assert s == 302 and "oidc_autologin_pause" not in cookies(br), (s, cookies(br))
    cb, sso = to_callback(br, kc_url, "alice", "alicepass")
    assert sso, "expected Keycloak's SSO session"
    s, _, loc = req("GET", cb, opener=br)
    assert (s, loc) == (302, "/admin"), (s, loc)
    req("POST", BASE + "/api/logout", opener=br)
    record("auto-3-no-redirect-after-logout", "GET", "/admin/login", 200, "ok")

    # 4. ?password: the password form, and password login works.
    br3 = browser()
    r = req("GET", BASE + "/admin/login?password", opener=br3)
    assert is_form(r), r[:1] + r[2:]
    r = req("GET", BASE + "/admin/login?password=1&next=/admin/lists", opener=br3)
    assert is_form(r) and 'name="next" value="/admin/lists"' in r[1]
    s, _, loc = req("POST", BASE + "/admin/login",
                    form={"username": "alice", "password": "alicepass123", "next": "/admin/lists"}, opener=br3)
    assert (s, loc) == (302, "/admin/lists"), (s, loc)
    # A failed password login renders the form with the error, no redirect.
    r = req("POST", BASE + "/admin/login", form={"username": "alice", "password": "wrongpass123", "next": "/admin"})
    assert is_form(r) and "role=\"alert\"" in r[1], r[:1]
    record("auto-4-password-escape", "GET", "/admin/login?password", 200, "ok")

    # 5. Errors don't loop. An error on the login page shows the form; a
    # rejected OIDC login renders the error on the callback, not a redirect.
    assert is_form(req("GET", BASE + "/admin/login?error=access_denied"))
    for user, pw, want in [("dave", "davepass", "Invalid login"), ("eve", "evepass", "not verified")]:
        brx = browser()
        s, _, kc_url = req("GET", BASE + "/admin/login", opener=brx)
        assert s == 302, s
        cb, _ = to_callback(brx, kc_url, user, pw)
        s, body, loc = req("GET", cb, opener=brx)
        assert s == 200 and not loc and want in page_error(body), (user, s, loc, page_error(body))
    # Keycloak reporting an error (user cancelled) to the callback.
    brx = browser()
    s, _, kc_url = req("GET", BASE + "/admin/login", opener=brx)
    q = urllib.parse.parse_qs(urllib.parse.urlsplit(kc_url).query)
    s, body, loc = req("GET", BASE + "/auth/oidc?error=access_denied&state=" + urllib.parse.quote(q["state"][0]), opener=brx)
    assert s in (200, 401) and not loc, (s, loc)
    record("auto-5-no-loop-on-error", "GET", "/auth/oidc?error", s, "ok")

    # 5b. Provider unreachable: the form with an error, not a 500.
    set_oidc("settings-autologin-deadidp", provider_url="http://127.0.0.1:58099/realms/parity")
    r = req("GET", BASE + "/admin/login")
    assert is_form(r) and 'role="alert"' in r[1], (r[0], r[2])
    set_oidc("settings-autologin-idp-back", provider_url=REALM)
    s, _, kc_url = req("GET", BASE + "/admin/login")
    assert s == 302 and kc_url.startswith(REALM), (s, kc_url)
    record("auto-5b-idp-down", "GET", "/admin/login", 200, "ok")

    # 6. Off again: the login page is the form.
    set_oidc("settings-autologin-off", auto_login=False)
    assert is_form(req("GET", BASE + "/admin/login"))
    record("auto-6-off", "GET", "/admin/login", 200, "ok")
    print("oidc auto-login: all checks passed")

# ---------------------------------------------------------------- API bearer tokens (fork only)
# Self-checking, like auto-login: runs only with OIDC_API_JWT=1 (run.sh then
# starts listmonk with [api_jwt] for this realm, audience listmonk, client
# sgnl-mcp). The gateway's flow: a user's token, exchanged (RFC 8693) for
# one with aud listmonk, sent as Authorization: Bearer.
if os.environ.get("OIDC_API_JWT") == "1":
    TOK = REALM + "/protocol/openid-connect/token"

    def exchanged(client, secret, user, password):
        s, sub, _ = req("POST", TOK, form={"grant_type": "password", "client_id": client, "client_secret": secret,
                                            "username": user, "password": password, "scope": "openid email profile"})
        assert s == 200, (s, sub)
        s, x, _ = req("POST", TOK, form={"grant_type": "urn:ietf:params:oauth:grant-type:token-exchange",
                                          "client_id": client, "client_secret": secret,
                                          "subject_token": sub["access_token"],
                                          "subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
                                          "audience": "listmonk"})
        assert s == 200, (s, x)
        return x["access_token"], sub["id_token"]

    def bearer(method, path, tok):
        s, body, _ = req(method, BASE + path, auth="Bearer " + tok)
        return s, body

    mcp = ("sgnl-mcp", "mcp-secret")
    tok, id_tok = exchanged(*mcp, "alice", "alicepass")
    s, p = bearer("GET", "/api/profile", tok)
    assert s == 200 and p["data"]["username"] == "alice", (s, p)
    s, p = bearer("GET", "/api/lists?minimal=true&per_page=all", tok)
    assert s == 200, (s, p)
    # Alice's role (OIDC editors) can't read settings or users.
    for path in ("/api/settings", "/api/users"):
        s, p = bearer("GET", path, tok)
        assert s == 403, (path, s, p)
    # No session to end.
    s, p = bearer("POST", "/api/logout", tok)
    assert s == 400, (s, p)
    s, p = bearer("GET", "/api/profile", tok)
    assert s == 200, (s, p)

    # Bob's Keycloak e-mail is mixed case.
    s, p = bearer("GET", "/api/profile", exchanged(*mcp, "bob", "bobpass")[0])
    assert s == 200 and p["data"]["email"] == "bob@example.com", (s, p)

    refused = {
        "id-token": (id_tok, 401),
        "other-client": (exchanged("other-client", "other-secret", "alice", "alicepass")[0], 401),
        "unverified-email": (exchanged(*mcp, "eve", "evepass")[0], 401),
        "disabled-user": (exchanged(*mcp, "dave", "davepass")[0], 403),
        "unknown-user": (exchanged(*mcp, "gina", "ginapass")[0], 403),
        "tampered": (tok[:-4] + ("AAAA" if not tok.endswith("AAAA") else "BBBB"), 401),
    }
    for name, (t, want) in refused.items():
        s, p = bearer("GET", "/api/profile", t)
        assert s == want, (name, s, p)
    # API tokens and sessions still work.
    s, p = api("", "GET", "/api/profile", rec=False)
    assert s == 200, (s, p)
    print("api bearer tokens: all checks passed")

# Leave OIDC off so the final database dump is the same shape as without it.
set_oidc("settings-oidc-off", enabled=False)
