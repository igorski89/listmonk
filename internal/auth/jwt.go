package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v5"
)

// APIJWTConfig lets the API accept OAuth 2.0 access tokens (JWTs) from an
// identity provider, as `Authorization: Bearer <token>`. A token acts as the
// existing listmonk user whose e-mail it carries, with that user's own
// permissions. It's read from the config file or environment only (never
// from the settings in the database), so an admin session can't point
// listmonk at another issuer.
type APIJWTConfig struct {
	Enabled bool
	// Issuer is the provider's issuer URL; its discovery document gives the
	// JWKS. Tokens must carry exactly this `iss`.
	Issuer string
	// Audiences: a token's `aud` must contain one of these.
	Audiences []string
	// Clients: the client the token was issued to (`client_id`, RFC 9068, or
	// `azp`) must be one of these.
	Clients []string
}

// ConfigList reads a config list value: a TOML array, or a comma or space
// separated string (environment variables). Empty items are dropped, so an
// empty array stays empty.
func ConfigList(v any) []string {
	var vals []string
	switch v := v.(type) {
	case string:
		vals = []string{v}
	case []string:
		vals = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				vals = append(vals, s)
			}
		}
	}
	var out []string
	for _, v := range vals {
		out = append(out, strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })...)
	}
	return out
}

// Validate reports a config that can't be used safely.
func (c APIJWTConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch {
	case c.Issuer == "":
		return errors.New("api_jwt: issuer is required")
	case len(c.Audiences) == 0:
		return errors.New("api_jwt: at least one audience is required")
	case len(c.Clients) == 0:
		return errors.New("api_jwt: at least one client is required")
	}
	return nil
}

// jwtAuth verifies bearer access tokens. The provider (discovery and JWKS)
// is fetched on first use and kept; a failed fetch is retried on a later
// request, at most every jwtRetry.
type jwtAuth struct {
	cfg APIJWTConfig

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	lastErr  time.Time
}

const jwtRetry = 10 * time.Second

// jwtAlgs are the accepted signing algorithms.
var jwtAlgs = []string{oidc.RS256, oidc.ES256}

func (j *jwtAuth) getVerifier(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.verifier != nil {
		return j.verifier, nil
	}
	if time.Since(j.lastErr) < jwtRetry {
		return nil, errors.New("identity provider unavailable")
	}
	// The provider's JWKS fetches use this context's HTTP client after
	// NewProvider returns, so it mustn't be the request's context.
	p, err := oidc.NewProvider(oidc.ClientContext(context.Background(), &http.Client{Timeout: 10 * time.Second}), j.cfg.Issuer)
	if err != nil {
		j.lastErr = time.Now()
		return nil, err
	}
	// Audience and client are checked by verify (a token's audience is
	// listmonk, not the client it was issued to).
	j.verifier = p.Verifier(&oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: jwtAlgs})
	return j.verifier, nil
}

// jwtClaims are the access token claims listmonk reads.
type jwtClaims struct {
	Typ           string `json:"typ"`
	ClientID      string `json:"client_id"`
	AZP           string `json:"azp"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
}

// errInvalidToken is a token that can't be used: the client should get a
// new one (401).
type errInvalidToken struct{ reason string }

func (e errInvalidToken) Error() string { return e.reason }

// verify checks raw and returns the e-mail it identifies, lowercased.
func (j *jwtAuth) verify(ctx context.Context, raw string) (string, error) {
	if !isAccessToken(raw) {
		return "", errInvalidToken{"not an access token"}
	}

	v, err := j.getVerifier(ctx)
	if err != nil {
		return "", fmt.Errorf("verifier: %w", err)
	}
	// Signature (RS256/ES256 only, from the issuer's JWKS), iss, exp, nbf.
	tok, err := v.Verify(ctx, raw)
	if err != nil {
		return "", errInvalidToken{err.Error()}
	}

	if !slices.ContainsFunc(tok.Audience, func(a string) bool { return slices.Contains(j.cfg.Audiences, a) }) {
		return "", errInvalidToken{fmt.Sprintf("audience %v not accepted", tok.Audience)}
	}

	var c jwtClaims
	if err := tok.Claims(&c); err != nil {
		return "", errInvalidToken{"invalid claims"}
	}
	client := c.ClientID
	if client == "" {
		client = c.AZP
	}
	if client == "" || !slices.Contains(j.cfg.Clients, client) {
		return "", errInvalidToken{fmt.Sprintf("client %q not accepted", client)}
	}

	if c.EmailVerified == nil || !*c.EmailVerified {
		return "", errInvalidToken{"e-mail not verified"}
	}
	em, err := mail.ParseAddress(strings.TrimSpace(c.Email))
	if err != nil {
		return "", errInvalidToken{"no valid e-mail"}
	}
	return strings.ToLower(em.Address), nil
}

// isAccessToken tells access tokens from ID and refresh tokens: an RFC 9068
// access token has the header `typ: at+jwt` (Authelia); Keycloak marks its
// tokens with a `typ` claim, "Bearer" for access tokens ("ID", "Refresh"
// otherwise). Anything else is refused. The signature is checked after.
func isAccessToken(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	var h, p struct {
		Typ string `json:"typ"`
	}
	if b, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil || json.Unmarshal(b, &h) != nil {
		return false
	}
	switch strings.ToLower(h.Typ) {
	case "at+jwt", "application/at+jwt":
		return true
	}
	if b, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil || json.Unmarshal(b, &p) != nil {
		return false
	}
	return p.Typ == "Bearer"
}

// bearerToken returns the token of an `Authorization: Bearer` header.
func bearerToken(hdr string) (string, bool) {
	scheme, tok, ok := strings.Cut(hdr, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(tok), true
}

// authBearer authenticates a bearer token; on failure it returns the error
// to respond with.
func (o *Auth) authBearer(c *echo.Context, raw string) (User, error) {
	email, err := o.jwt.verify(c.Request().Context(), raw)
	if err != nil {
		var inv errInvalidToken
		if errors.As(err, &inv) {
			o.log.Printf("api_jwt: rejected token: %s", inv.reason)
			c.Response().Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			return User{}, echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
		}
		o.log.Printf("api_jwt: %v", err)
		return User{}, echo.NewHTTPError(http.StatusServiceUnavailable, "token verification unavailable")
	}

	u, err := o.cb.GetUserByEmail(email)
	if err != nil {
		var he *echo.HTTPError
		if errors.As(err, &he) && he.Code == http.StatusNotFound {
			o.log.Printf("api_jwt: no user with the token's e-mail %s", email)
			return User{}, echo.NewHTTPError(http.StatusForbidden, "unknown user")
		}
		o.log.Printf("api_jwt: fetching user %s: %v", email, err)
		return User{}, echo.NewHTTPError(http.StatusInternalServerError, "error fetching user")
	}
	if u.Type != UserTypeUser || u.Status != UserStatusEnabled {
		o.log.Printf("api_jwt: user %s is %s/%s", email, u.Type, u.Status)
		return User{}, echo.NewHTTPError(http.StatusForbidden, "user not allowed")
	}
	return u, nil
}
