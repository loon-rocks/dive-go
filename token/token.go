package token

import (
	"fmt"
	"log/slog"
	"time"
)

const (
	// DefaultAudience is the aud claim of the tokens a [Signer] mints, and
	// the audience a [Verifier] requires, when [Audience] is omitted.
	DefaultAudience = "dive"
	// DefaultTTL is how long a token is valid when [StartClaims.TTL] or
	// [ReadClaims.TTL] is zero.
	DefaultTTL = time.Hour
	// DevIssuer is the iss claim of tokens signed by the development key.
	// [DevSigner] mints them, and a [Verifier] checks them against the key
	// from [Verifier.WithDev].
	DevIssuer = "dive-dev"
	// Leeway is the clock skew a [Verifier] allows when it checks the exp
	// and nbf claims.
	Leeway = 30 * time.Second
)

const redacted = "[redacted]"

// Token is a signed run token, a compact JWT. It redacts itself when
// printed with the fmt package, logged with log/slog, or encoded as text
// or JSON, so it can sit in structs that get logged. Use string(tok)
// where the raw value must go, as in an Authorization header.
type Token string

// String returns "[redacted]".
func (t Token) String() string { return redacted }

// GoString returns "[redacted]", for the %#v verb.
func (t Token) GoString() string { return redacted }

// Format writes "[redacted]" for every verb, so even %q and %x don't
// print the token.
func (t Token) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue returns "[redacted]" as a [slog.Value].
func (t Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText returns "[redacted]", which [encoding/json] also uses.
func (t Token) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Claims are the claims of a run token that [Verifier.Verify] accepted.
type Claims struct {
	// Issuer is the iss claim.
	Issuer string

	// Audience is the aud claim.
	Audience []string

	// Subject is the sub claim: the user the run acts for. It is never
	// empty.
	Subject string

	// Tenant is the tenant claim. It is never empty.
	Tenant string

	// Scope is the scope claim, a space-separated list of scopes. It is
	// empty in a start token. See [Claims.HasScope].
	Scope string

	// Plan is the plan claim, the identifier of the plan a start token
	// may start. It is empty in a read token.
	Plan string

	// ID is the jti claim. In a start token it is the run's id.
	ID string

	// IssuedAt is the iat claim, or the zero time when it is absent.
	IssuedAt time.Time

	// ExpiresAt is the exp claim.
	ExpiresAt time.Time

	// NotBefore is the nbf claim, or the zero time when it is absent.
	NotBefore time.Time

	// Extra holds every other claim, such as those from
	// [StartClaims.Extra]. It is never nil.
	Extra map[string]any

	// Dev reports whether the token was verified with the development key
	// from [Verifier.WithDev].
	Dev bool
}

type config struct {
	issuer   string
	audience string
}

// Option configures a [Signer] in [NewSigner] and [DevSigner], or a
// [Verifier] in [NewVerifier].
type Option func(*config)

// Issuer sets the iss claim. A Signer needs it; [NewSigner] fails
// without it. A Verifier that trusts a key needs it too, and requires
// tokens to carry it.
func Issuer(issuer string) Option {
	return func(c *config) { c.issuer = issuer }
}

// Audience sets the aud claim that a Signer writes and a Verifier
// requires. It must match the audience dive serve expects. When it is
// omitted, the audience is [DefaultAudience].
func Audience(audience string) Option {
	return func(c *config) { c.audience = audience }
}

func newConfig(opts []Option) config {
	c := config{audience: DefaultAudience}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

var reserved = map[string]bool{
	"iss": true, "aud": true, "sub": true, "tenant": true, "scope": true, "plan": true,
	"jti": true, "iat": true, "exp": true, "nbf": true, "dive_dev": true,
}
