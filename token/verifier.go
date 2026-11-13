package token

import (
	"crypto"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is wrapped by every error [Verifier.Verify] returns for
// a token that doesn't verify. The rest of the message says why, such as
// "the token has expired".
var ErrInvalidToken = errors.New("invalid run token")

var validMethods = []string{"EdDSA", "ES256", "RS256"}

// Verifier checks the run tokens that Dive sends with tool calls. Create
// one with [NewVerifier], add keys with [Verifier.WithKey] and the
// development key with [Verifier.WithDev], and pass it to the tools
// package as the handler's token verifier.
//
// A Verifier accepts a token when it is signed with EdDSA, ES256 or RS256
// by a trusted key, has the Verifier's audience and, when one is set, its
// issuer, has an exp claim, is within its validity period give or take
// [Leeway], and has non-empty sub and tenant claims.
//
// A Verifier is safe for concurrent use by multiple goroutines. The With
// methods return a new Verifier and leave the receiver unchanged.
type Verifier struct {
	keys     []crypto.PublicKey
	issuer   string
	audience string
	dev      []crypto.PublicKey
	devErr   error
	err      error
	now      func() time.Time
}

// NewVerifier returns a Verifier that trusts key: an [ed25519.PublicKey],
// an [*ecdsa.PublicKey] on P-256 or an [*rsa.PublicKey]. A private key of
// those kinds is accepted for its public half. [ParsePublicKeyPEM] reads
// a key from a PEM file. A nil key adds no key, for a Verifier that only
// trusts the development key.
//
// A Verifier that trusts a key needs the [Issuer] option, and accepts
// only tokens with that issuer.
//
// NewVerifier doesn't return an error. A missing Issuer, a key of another
// kind or an empty [Audience] is reported by [Verifier.Err] and returned by every call to
// [Verifier.Verify].
func NewVerifier(key crypto.PublicKey, opts ...Option) *Verifier {
	cfg := newConfig(opts)
	v := &Verifier{issuer: cfg.issuer, audience: cfg.audience, now: time.Now}
	if cfg.audience == "" {
		v.err = errors.New("token: the Audience can't be empty")
	}
	return v.add(key)
}

func (v *Verifier) clone() *Verifier {
	c := *v
	c.keys = append([]crypto.PublicKey(nil), v.keys...)
	c.dev = append([]crypto.PublicKey(nil), v.dev...)
	return &c
}

func (v *Verifier) add(key crypto.PublicKey) *Verifier {
	if key == nil {
		return v
	}
	if v.issuer == "" {
		if v.err == nil {
			v.err = errors.New("token: a Verifier that trusts a key needs an Issuer")
		}
		return v
	}
	checked, err := checkPublic(key)
	if err != nil {
		if v.err == nil {
			v.err = err
		}
		return v
	}
	v.keys = append(v.keys, checked)
	return v
}

// WithKey returns a copy of v that also trusts key, as for rotating keys.
// It accepts the same keys as [NewVerifier]; a nil key adds nothing.
func (v *Verifier) WithKey(key crypto.PublicKey) *Verifier {
	return v.clone().add(key)
}

// WithDev returns a copy of v that also accepts development tokens:
// tokens with the issuer [DevIssuer], signed by key. It verifies them
// with the same audience as other tokens and marks their claims with
// [Claims.Dev]. Get key from [DevKey].
//
// When key failed to load, as when DIVE_ENV is production, WithDev
// doesn't fail: the copy refuses development tokens with key's error and
// verifies other tokens as v does.
func (v *Verifier) WithDev(key DevPublicKey) *Verifier {
	c := v.clone()
	if key.err != nil {
		c.devErr = key.err
		return c
	}
	c.dev = append(c.dev, key.key)
	return c
}

// Err returns the error from building v, such as an unsupported key, or
// nil. Check it once at startup.
func (v *Verifier) Err() error { return v.err }

// Verify checks raw, a compact JWT, and returns its claims. Surrounding
// spaces are ignored. A token that doesn't verify returns an error that
// wraps [ErrInvalidToken]. When [Verifier.Err] isn't nil, Verify returns
// that error for every token.
func (v *Verifier) Verify(raw string) (*Claims, error) {
	if v.err != nil {
		return nil, v.err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: no token", ErrInvalidToken)
	}
	unverified, _, err := jwt.NewParser().ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("%w: the token isn't a JWT", ErrInvalidToken)
	}
	iss, _ := unverified.Claims.GetIssuer()
	if iss == DevIssuer && (len(v.dev) > 0 || v.devErr != nil) {
		if len(v.dev) == 0 {
			return nil, fmt.Errorf("%w: a development token arrived but the dev key isn't loaded: %v", ErrInvalidToken, v.devErr)
		}
		claims, err := v.check(raw, v.dev, DevIssuer)
		if err != nil {
			return nil, err
		}
		claims.Dev = true
		return claims, nil
	}
	return v.check(raw, v.keys, v.issuer)
}

func (v *Verifier) check(raw string, keys []crypto.PublicKey, issuer string) (*Claims, error) {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(validMethods),
		jwt.WithLeeway(Leeway),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.now),
	}
	if issuer != "" {
		opts = append(opts, jwt.WithIssuer(issuer))
	}
	mapped := jwt.MapClaims{}
	_, err := jwt.NewParser(opts...).ParseWithClaims(raw, mapped, func(t *jwt.Token) (any, error) {
		set := jwt.VerificationKeySet{}
		for _, key := range keys {
			if family(key) == t.Method.Alg() {
				set.Keys = append(set.Keys, key)
			}
		}
		if len(set.Keys) == 0 {
			return nil, errors.New("no key for the algorithm")
		}
		return set, nil
	})
	if err != nil {
		return nil, describe(err)
	}
	return fromMap(mapped)
}

func describe(err error) error {
	reason := "the token is malformed"
	switch {
	case errors.Is(err, jwt.ErrTokenSignatureInvalid), errors.Is(err, jwt.ErrTokenUnverifiable):
		reason = "no trusted key can verify this token"
	case errors.Is(err, jwt.ErrTokenExpired):
		reason = "the token has expired"
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		reason = "the token's issuer isn't trusted"
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		reason = "the token isn't meant for this audience"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		reason = "the token isn't valid yet"
	case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
		reason = "the token lacks a required claim"
	}
	return fmt.Errorf("%w: %s", ErrInvalidToken, reason)
}

func fromMap(m jwt.MapClaims) (*Claims, error) {
	c := &Claims{Extra: map[string]any{}}
	c.Issuer, _ = m.GetIssuer()
	aud, _ := m.GetAudience()
	c.Audience = []string(aud)
	c.Subject, _ = m.GetSubject()
	if strings.TrimSpace(c.Subject) == "" {
		return nil, fmt.Errorf("%w: the token has no sub", ErrInvalidToken)
	}
	c.Tenant, _ = m["tenant"].(string)
	if strings.TrimSpace(c.Tenant) == "" {
		return nil, fmt.Errorf("%w: the token has no tenant", ErrInvalidToken)
	}
	c.Scope, _ = m["scope"].(string)
	c.Plan, _ = m["plan"].(string)
	c.ID, _ = m["jti"].(string)
	if t, _ := m.GetIssuedAt(); t != nil {
		c.IssuedAt = t.Time
	}
	if t, _ := m.GetExpirationTime(); t != nil {
		c.ExpiresAt = t.Time
	}
	if t, _ := m.GetNotBefore(); t != nil {
		c.NotBefore = t.Time
	}
	for name, value := range m {
		if !reserved[name] {
			c.Extra[name] = value
		}
	}
	return c, nil
}

// HasScope reports whether scope is one of the space-separated scopes in
// [Claims.Scope].
func (c *Claims) HasScope(scope string) bool {
	return slices.Contains(strings.Fields(c.Scope), scope)
}
