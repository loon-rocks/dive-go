package token

import (
	"crypto"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultReadScopes are the scopes of a read token when
// [ReadClaims.Scopes] is empty: every scope a [dive.Client] method other
// than Start needs.
//
// [dive.Client]: https://pkg.go.dev/github.com/loon-rocks/dive-go#Client
var DefaultReadScopes = []string{"plans:read", "runs:read", "runs:cancel"}

// Signer mints run tokens for dive serve with an app's private key.
// dive serve must trust the matching public key and the Signer's issuer.
// Create one with [NewSigner], or [DevSigner] for local development.
//
// A Signer is safe for concurrent use by multiple goroutines.
type Signer struct {
	key    crypto.Signer
	method jwt.SigningMethod
	cfg    config
	now    func() time.Time
}

// StartClaims are the claims of a start token, for [Signer.Start].
type StartClaims struct {
	// Plan is the identifier of the plan the token may start. It is
	// required.
	Plan string

	// Tenant is the tenant the run belongs to. It is required.
	Tenant string

	// User is the user the run acts for, the sub claim. It is required.
	User string

	// TTL is how long the token is valid. Make it longer than the plan
	// takes to run, since the run's tool calls carry the same token. Zero
	// means [DefaultTTL].
	TTL time.Duration

	// Extra adds claims to the token, such as a role that the app's tools
	// check. A name that the token sets itself, such as iss, sub, tenant,
	// plan or jti, is an error.
	Extra map[string]any
}

// ReadClaims are the claims of a read token, for [Signer.Reader].
type ReadClaims struct {
	// Tenant is the tenant whose runs the token can read. It is required.
	Tenant string

	// User is the sub claim. It is required.
	User string

	// Scopes are the scopes the token grants, such as "runs:read". Empty
	// means [DefaultReadScopes].
	Scopes []string

	// TTL is how long the token is valid. Zero means [DefaultTTL].
	TTL time.Duration

	// Extra adds claims to the token. A name that the token sets itself,
	// such as iss, sub, tenant or scope, is an error.
	Extra map[string]any
}

// NewSigner returns a Signer that signs with key: an [ed25519.PrivateKey]
// (EdDSA), an [*ecdsa.PrivateKey] on P-256 (ES256) or an
// [*rsa.PrivateKey] (RS256). [ParsePrivateKeyPEM] reads one from a PEM
// file. NewSigner fails when key is nil or of another kind, when the
// [Issuer] option is missing, or when the [Audience] is empty.
func NewSigner(key crypto.Signer, opts ...Option) (*Signer, error) {
	if key == nil {
		return nil, errors.New("token: NewSigner needs a private key")
	}
	method, err := signingMethod(key)
	if err != nil {
		return nil, err
	}
	cfg := newConfig(opts)
	if cfg.issuer == "" {
		return nil, errors.New("token: NewSigner needs an Issuer")
	}
	if cfg.audience == "" {
		return nil, errors.New("token: the Audience can't be empty")
	}
	return &Signer{key: key, method: method, cfg: cfg, now: time.Now}, nil
}

// Start mints a start token for one run of c.Plan and returns it with the
// run's id. The id is a new random UUID, which Start also writes as the
// token's jti, and dive serve uses it as the run's id. Pass the token to
// [dive.Client.Start]. Start fails when c.Plan, c.Tenant or c.User is
// empty or c.Extra names a reserved claim.
//
// [dive.Client.Start]: https://pkg.go.dev/github.com/loon-rocks/dive-go#Client.Start
func (s *Signer) Start(c StartClaims) (Token, string, error) {
	if strings.TrimSpace(c.Plan) == "" {
		return "", "", errors.New("token: a start token needs a Plan")
	}
	runID, err := newUUID()
	if err != nil {
		return "", "", err
	}
	claims, err := s.base(c.Tenant, c.User, c.TTL, c.Extra)
	if err != nil {
		return "", "", err
	}
	claims["plan"] = c.Plan
	claims["jti"] = runID
	tok, err := s.sign(claims)
	if err != nil {
		return "", "", err
	}
	return tok, runID, nil
}

// Reader mints a read token, which lists plans and reads, follows and
// cancels the runs of c.Tenant as its scopes allow. Reader fails when
// c.Tenant or c.User is empty or c.Extra names a reserved claim.
func (s *Signer) Reader(c ReadClaims) (Token, error) {
	scopes := c.Scopes
	if len(scopes) == 0 {
		scopes = DefaultReadScopes
	}
	claims, err := s.base(c.Tenant, c.User, c.TTL, c.Extra)
	if err != nil {
		return "", err
	}
	claims["scope"] = strings.Join(scopes, " ")
	return s.sign(claims)
}

func (s *Signer) base(tenant, user string, ttl time.Duration, extra map[string]any) (jwt.MapClaims, error) {
	if strings.TrimSpace(tenant) == "" {
		return nil, errors.New("token: a token needs a Tenant")
	}
	if strings.TrimSpace(user) == "" {
		return nil, errors.New("token: a token needs a User")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := s.now()
	claims := jwt.MapClaims{}
	for name, value := range extra {
		if reserved[name] {
			return nil, fmt.Errorf("token: %q is a reserved claim", name)
		}
		claims[name] = value
	}
	claims["iss"] = s.cfg.issuer
	claims["aud"] = s.cfg.audience
	claims["sub"] = user
	claims["tenant"] = tenant
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(ttl).Unix()
	return claims, nil
}

func (s *Signer) sign(claims jwt.MapClaims) (Token, error) {
	signed, err := jwt.NewWithClaims(s.method, claims).SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("token: couldn't sign: %w", err)
	}
	return Token(signed), nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("token: couldn't make a run id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
