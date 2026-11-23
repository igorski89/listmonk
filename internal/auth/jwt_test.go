package auth

import (
	"reflect"

	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/labstack/echo/v5"
	null "gopkg.in/volatiletech/null.v6"
)

// testIdP is an OIDC provider: discovery and a JWKS whose keys can be
// swapped (rotation).
type testIdP struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys []jose.JSONWebKey
}

func newTestIdP(t *testing.T) *testIdP {
	p := &testIdP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.srv.URL,
			"jwks_uri":                              p.srv.URL + "/jwks",
			"authorization_endpoint":                p.srv.URL + "/auth",
			"token_endpoint":                        p.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256", "ES256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: p.keys})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

type testKey struct {
	priv any
	alg  jose.SignatureAlgorithm
	kid  string
}

// publish makes k the provider's only key.
func (p *testIdP) publish(k testKey) {
	pub := k.priv.(crypto.Signer).Public()
	p.mu.Lock()
	p.keys = []jose.JSONWebKey{{Key: pub, KeyID: k.kid, Algorithm: string(k.alg), Use: "sig"}}
	p.mu.Unlock()
}

func rsaKey(t *testing.T, kid string) testKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{k, jose.RS256, kid}
}

// sign returns a JWT with the given header typ and claims.
func sign(t *testing.T, k testKey, typ string, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithHeader("kid", k.kid)
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: k.alg, Key: k.priv}, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := s.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// keycloakClaims are the claims of a token Keycloak gives sgnl-mcp by token
// exchange (aud listmonk, azp sgnl-mcp, typ "Bearer").
func keycloakClaims(iss string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": iss, "sub": "u-1", "aud": "listmonk", "azp": "sgnl-mcp", "typ": "Bearer",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"email": "Alice@Example.com", "email_verified": true, "preferred_username": "alice",
	}
}

var testUsers = map[string]User{
	"alice@example.com": {Base: Base{ID: 1}, Username: "alice", Email: null.StringFrom("alice@example.com"),
		Type: UserTypeUser, Status: UserStatusEnabled, PermissionsMap: map[string]struct{}{"lists:get_all": {}}},
	"dora@example.com": {Base: Base{ID: 2}, Username: "dora", Type: UserTypeUser, Status: UserStatusDisabled},
	"bot@example.com":  {Base: Base{ID: 3}, Username: "bot", Type: UserTypeAPI, Status: UserStatusEnabled},
}

func newTestAuth(p *testIdP, enabled bool) *Auth {
	a := &Auth{
		cfg: Config{},
		log: log.New(io.Discard, "", 0),
		cb: &Callbacks{GetUserByEmail: func(email string) (User, error) {
			if u, ok := testUsers[email]; ok {
				return u, nil
			}
			return User{}, echo.NewHTTPError(http.StatusNotFound, "not found")
		}},
		apiUsers: map[string]User{},
	}
	if enabled {
		a.jwt = &jwtAuth{cfg: APIJWTConfig{Enabled: true, Issuer: p.srv.URL, Audiences: []string{"listmonk"}, Clients: []string{"sgnl-mcp"}}}
	}
	return a
}

// call runs a request through APIMiddleware and a handler that needs perm,
// as the /api group does, and returns the status and the user (if any).
func call(a *Auth, hdr, cookie, perm string) (int, string, string) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/lists", nil)
	if hdr != "" {
		req.Header.Set("Authorization", hdr)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var who string
	h := a.APIMiddleware(func(c *echo.Context) error {
		if err, ok := c.Get(UserHTTPCtxKey).(*echo.HTTPError); ok {
			return err
		}
		return a.Perm(func(c *echo.Context) error {
			who = GetUser(c).Username
			return c.String(http.StatusOK, "ok")
		}, perm)(c)
	})
	if err := h(c); err != nil {
		he := err.(*echo.HTTPError)
		return he.Code, "", rec.Header().Get("WWW-Authenticate")
	}
	return rec.Code, who, rec.Header().Get("WWW-Authenticate")
}

func TestAPIJWT(t *testing.T) {
	p := newTestIdP(t)
	k := rsaKey(t, "k1")
	p.publish(k)
	a := newTestAuth(p, true)
	iss := p.srv.URL

	with := func(f func(m map[string]any)) map[string]any {
		m := keycloakClaims(iss)
		f(m)
		return m
	}
	bearer := func(tok string) string { return "Bearer " + tok }

	// The user's own permissions apply.
	if code, who, _ := call(a, bearer(sign(t, k, "JWT", keycloakClaims(iss))), "", "lists:get_all"); code != 200 || who != "alice" {
		t.Fatalf("valid Keycloak token: %d %q", code, who)
	}
	if code, _, _ := call(a, bearer(sign(t, k, "JWT", keycloakClaims(iss))), "", "settings:manage"); code != http.StatusForbidden {
		t.Errorf("permission alice lacks: %d, want 403", code)
	}

	// RFC 9068 (Authelia): typ at+jwt in the header, client_id, no azp.
	rfc := with(func(m map[string]any) {
		delete(m, "typ")
		delete(m, "azp")
		m["client_id"] = "sgnl-mcp"
		m["aud"] = []string{"listmonk", "other"}
	})
	if code, who, _ := call(a, bearer(sign(t, k, "at+jwt", rfc)), "", "lists:get_all"); code != 200 || who != "alice" {
		t.Errorf("RFC 9068 token: %d %q", code, who)
	}

	// ES256 is accepted too.
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	es := testKey{ek, jose.ES256, "e1"}
	p.publish(es)
	if code, _, _ := call(a, bearer(sign(t, es, "JWT", keycloakClaims(iss))), "", "lists:get_all"); code != 200 {
		t.Errorf("ES256: %d", code)
	}
	p.publish(k)

	// alg none and HS256 keyed with the public key.
	h64 := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	none := h64(map[string]string{"alg": "none", "typ": "JWT"}) + "." + h64(keycloakClaims(iss)) + "."
	pubDER, _ := x509.MarshalPKIXPublicKey(k.priv.(*rsa.PrivateKey).Public())
	hs := sign(t, testKey{pubDER, jose.HS256, "k1"}, "JWT", keycloakClaims(iss))

	// Unrelated key with the same kid (forged).
	forged := sign(t, rsaKey(t, "k1"), "JWT", keycloakClaims(iss))

	rejected := map[string]string{
		"wrong audience":    sign(t, k, "JWT", with(func(m map[string]any) { m["aud"] = "account" })),
		"no audience":       sign(t, k, "JWT", with(func(m map[string]any) { delete(m, "aud") })),
		"wrong client":      sign(t, k, "JWT", with(func(m map[string]any) { m["azp"] = "other-client" })),
		"no client":         sign(t, k, "JWT", with(func(m map[string]any) { delete(m, "azp") })),
		"listmonk client":   sign(t, k, "JWT", with(func(m map[string]any) { m["azp"] = "listmonk" })),
		"wrong issuer":      sign(t, k, "JWT", with(func(m map[string]any) { m["iss"] = "https://evil.example" })),
		"expired":           sign(t, k, "JWT", with(func(m map[string]any) { m["exp"] = time.Now().Add(-time.Minute).Unix() })),
		"not yet valid":     sign(t, k, "JWT", with(func(m map[string]any) { m["nbf"] = time.Now().Add(time.Hour).Unix() })),
		"ID token":          sign(t, k, "JWT", with(func(m map[string]any) { m["typ"] = "ID" })),
		"refresh token":     sign(t, k, "JWT", with(func(m map[string]any) { m["typ"] = "Refresh" })),
		"no type":           sign(t, k, "JWT", with(func(m map[string]any) { delete(m, "typ") })),
		"unverified mail":   sign(t, k, "JWT", with(func(m map[string]any) { m["email_verified"] = false })),
		"no email_verified": sign(t, k, "JWT", with(func(m map[string]any) { delete(m, "email_verified") })),
		"no e-mail":         sign(t, k, "JWT", with(func(m map[string]any) { delete(m, "email") })),
		"alg none":          none,
		"HS256":             hs,
		"forged":            forged,
		"garbage":           "abc.def.ghi",
	}
	for name, tok := range rejected {
		code, _, www := call(a, bearer(tok), "", "lists:get_all")
		if code != http.StatusUnauthorized || !strings.Contains(www, "invalid_token") {
			t.Errorf("%s: %d %q, want 401 invalid_token", name, code, www)
		}
	}

	// Valid tokens of users that can't use the API this way.
	for email, want := range map[string]int{"nobody@example.com": 403, "dora@example.com": 403, "bot@example.com": 403} {
		tok := sign(t, k, "JWT", with(func(m map[string]any) { m["email"] = email }))
		if code, _, _ := call(a, bearer(tok), "", "lists:get_all"); code != want {
			t.Errorf("%s: %d, want %d", email, code, want)
		}
	}

	// A failed bearer token doesn't fall back to a cookie session.
	if code, _, _ := call(a, bearer("abc.def.ghi"), "session=abc", "lists:get_all"); code != http.StatusUnauthorized {
		t.Errorf("bad token with a session cookie: %d, want 401", code)
	}
	// A good one wins over the cookie.
	if code, who, _ := call(a, bearer(sign(t, k, "JWT", keycloakClaims(iss))), "session=abc", "lists:get_all"); code != 200 || who != "alice" {
		t.Errorf("token with a session cookie: %d %q", code, who)
	}

	// Key rotation: a token signed with a new key is verified after the
	// provider publishes it.
	k2 := rsaKey(t, "k2")
	p.publish(k2)
	if code, _, _ := call(a, bearer(sign(t, k2, "JWT", keycloakClaims(iss))), "", "lists:get_all"); code != 200 {
		t.Errorf("rotated key: %d", code)
	}
}

// Without api_jwt, a bearer header is refused as before (unknown scheme),
// and the non-API middleware ignores tokens even when it's on.
func TestAPIJWTOff(t *testing.T) {
	p := newTestIdP(t)
	k := rsaKey(t, "k1")
	p.publish(k)
	tok := "Bearer " + sign(t, k, "JWT", keycloakClaims(p.srv.URL))

	if code, _, www := call(newTestAuth(p, false), tok, "", "lists:get_all"); code != http.StatusForbidden || www != "" {
		t.Errorf("disabled: %d %q, want 403 as before", code, www)
	}

	a := newTestAuth(p, true)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", tok)
	c := e.NewContext(req, httptest.NewRecorder())
	_ = a.Middleware(func(c *echo.Context) error { return nil })(c)
	if _, ok := c.Get(UserHTTPCtxKey).(User); ok {
		t.Error("admin (non-API) middleware accepted a bearer token")
	}
}

func TestAPIJWTConfig(t *testing.T) {
	for _, c := range []APIJWTConfig{
		{Enabled: true, Audiences: []string{"listmonk"}, Clients: []string{"x"}},
		{Enabled: true, Issuer: "https://idp", Clients: []string{"x"}},
		{Enabled: true, Issuer: "https://idp", Audiences: []string{"listmonk"}},
	} {
		if c.Validate() == nil {
			t.Errorf("%+v: accepted", c)
		}
	}
	if (APIJWTConfig{}).Validate() != nil {
		t.Error("disabled config refused")
	}
}

// The provider is fetched lazily; while it's down, requests get 503 (not
// 401: the token may be fine).
func TestAPIJWTProviderDown(t *testing.T) {
	p := newTestIdP(t)
	k := rsaKey(t, "k1")
	p.publish(k)
	tok := "Bearer " + sign(t, k, "JWT", keycloakClaims(p.srv.URL))
	a := newTestAuth(p, true)
	p.srv.Close()
	if code, _, _ := call(a, tok, "", "lists:get_all"); code != http.StatusServiceUnavailable {
		t.Errorf("provider down: %d, want 503", code)
	}
}

// [api_jwt] lists come from TOML arrays or environment strings; an empty
// array must stay empty, so the startup check refuses it.
func TestConfigList(t *testing.T) {
	ko := koanf.New(".")
	if err := ko.Load(rawbytes.Provider([]byte(`[api_jwt]
clients = []
audiences = ["listmonk", "other"]
`)), toml.Parser()); err != nil {
		t.Fatal(err)
	}
	if err := ko.Load(confmap.Provider(map[string]any{"env.one": "sgnl-mcp", "env.many": "a, b c", "env.empty": ""}, "."), nil); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][]string{
		"api_jwt.clients":   nil,
		"api_jwt.audiences": {"listmonk", "other"},
		"api_jwt.missing":   nil,
		"env.one":           {"sgnl-mcp"},
		"env.many":          {"a", "b", "c"},
		"env.empty":         nil,
	} {
		if got := ConfigList(ko.Get(key)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}
