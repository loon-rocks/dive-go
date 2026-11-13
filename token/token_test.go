package token_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/loon-rocks/dive-go/token"
)

func rustPEM(label string, der []byte) string {
	encoded := base64.StdEncoding.EncodeToString(der)
	var lines []string
	for len(encoded) > 64 {
		lines = append(lines, encoded[:64])
		encoded = encoded[64:]
	}
	lines = append(lines, encoded)
	return fmt.Sprintf("-----BEGIN %s-----\n%s\n-----END %s-----\n", label, strings.Join(lines, "\n"), label)
}

func rustDevKey(t *testing.T) (ed25519.PrivateKey, string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der := []byte{0x30, 0x53, 0x02, 0x01, 0x01, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20}
	der = append(der, priv.Seed()...)
	der = append(der, 0xa1, 0x23, 0x03, 0x21, 0x00)
	der = append(der, pub...)
	spki := append([]byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}, pub...)
	return priv, rustPEM("PRIVATE KEY", der), rustPEM("PUBLIC KEY", spki)
}

func dataDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DIVE_ENV", "")
	t.Setenv("DIVE_DEV_KEY", "")
	t.Setenv("DIVE_DEV_PUBLIC_KEY", "")
	dir := filepath.Join(home, ".local", "share", "dive", "serve")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mintLikeDive(t *testing.T, key ed25519.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func devRunClaims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":      "dive-dev",
		"aud":      "dive",
		"sub":      "ada@example.com",
		"tenant":   "acme",
		"jti":      "run-123",
		"plan":     "file_issue",
		"role":     "admin",
		"dive_dev": true,
		"iat":      now.Unix(),
		"exp":      now.Add(time.Hour).Unix(),
	}
}

func TestTokenFormattingRedacts(t *testing.T) {
	secret := "eyJhbGciOiJFZERTQSJ9.secret.part"
	tok := token.Token(secret)
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("start", "token", tok)
	encoded, err := json.Marshal(map[string]any{"token": tok})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{
		fmt.Sprintf("%v", tok),
		fmt.Sprintf("%s", tok),
		fmt.Sprintf("%+v", tok),
		fmt.Sprintf("%#v", tok),
		fmt.Sprintf("%q", tok),
		fmt.Sprintf("%v", struct{ T token.Token }{tok}),
		fmt.Sprintf("%+v", &struct{ T token.Token }{tok}),
		fmt.Sprint(tok),
		tok.String(),
		logged.String(),
		string(encoded),
	}
	for _, out := range outputs {
		if strings.Contains(out, "secret") {
			t.Errorf("token leaked in %q", out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Errorf("no redaction marker in %q", out)
		}
	}
	if string(tok) != secret {
		t.Fatal("the raw value must stay reachable through a string conversion")
	}
}

func TestSignerStartAndReaderRoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rs, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := []struct {
		name string
		alg  string
		key  any
	}{
		{"ed25519", "EdDSA", priv},
		{"p256", "ES256", ec},
		{"rsa", "RS256", rs},
	}
	for _, k := range keys {
		t.Run(k.name, func(t *testing.T) {
			var signer *token.Signer
			var err error
			var public any
			switch key := k.key.(type) {
			case ed25519.PrivateKey:
				signer, err = token.NewSigner(key, token.Issuer("https://app.example.com"))
				public = key.Public()
			case *ecdsa.PrivateKey:
				signer, err = token.NewSigner(key, token.Issuer("https://app.example.com"))
				public = &key.PublicKey
			case *rsa.PrivateKey:
				signer, err = token.NewSigner(key, token.Issuer("https://app.example.com"))
				public = &key.PublicKey
			}
			if err != nil {
				t.Fatal(err)
			}
			start, runID, err := signer.Start(token.StartClaims{Plan: "triage", Tenant: "acme", User: "ada@example.com", TTL: time.Hour, Extra: map[string]any{"role": "admin"}})
			if err != nil {
				t.Fatal(err)
			}
			if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(runID) {
				t.Fatalf("run id %q isn't a v4 UUID", runID)
			}
			parsed, _, err := jwt.NewParser().ParseUnverified(string(start), jwt.MapClaims{})
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Method.Alg() != k.alg {
				t.Fatalf("alg %s, want %s", parsed.Method.Alg(), k.alg)
			}
			mapped := parsed.Claims.(jwt.MapClaims)
			for _, name := range []string{"iss", "aud", "sub", "tenant", "iat", "exp", "plan", "jti"} {
				if _, ok := mapped[name]; !ok {
					t.Errorf("start token lacks %s", name)
				}
			}
			if _, ok := mapped["scope"]; ok {
				t.Error("a start token carries no scope")
			}
			verifier := token.NewVerifier(public, token.Issuer("https://app.example.com"))
			claims, err := verifier.Verify(string(start))
			if err != nil {
				t.Fatal(err)
			}
			if claims.ID != runID || claims.Plan != "triage" || claims.Tenant != "acme" || claims.Subject != "ada@example.com" {
				t.Fatalf("claims %+v", claims)
			}
			if claims.Extra["role"] != "admin" || claims.Dev {
				t.Fatalf("extra %v dev %v", claims.Extra, claims.Dev)
			}
			if got := claims.ExpiresAt.Sub(claims.IssuedAt); got != time.Hour {
				t.Fatalf("ttl %v", got)
			}
			read, err := signer.Reader(token.ReadClaims{Tenant: "acme", User: "ada@example.com"})
			if err != nil {
				t.Fatal(err)
			}
			readClaims, err := verifier.Verify(string(read))
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range []string{"plans:read", "runs:read", "runs:cancel"} {
				if !readClaims.HasScope(scope) {
					t.Errorf("reader lacks %s", scope)
				}
			}
			if readClaims.Plan != "" || readClaims.ID != "" {
				t.Fatal("a read token has no plan or jti")
			}
		})
	}
}

func TestSignerRefusesBadInput(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := token.NewSigner(priv); err == nil {
		t.Fatal("a signer without an issuer must fail")
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := token.NewSigner(p384, token.Issuer("x")); err == nil {
		t.Fatal("P-384 must be refused")
	}
	signer, err := token.NewSigner(priv, token.Issuer("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := signer.Start(token.StartClaims{Tenant: "acme", User: "ada"}); err == nil {
		t.Fatal("a start without a plan must fail")
	}
	if _, err := signer.Reader(token.ReadClaims{User: "ada"}); err == nil {
		t.Fatal("a token without a tenant must fail")
	}
	if _, err := signer.Reader(token.ReadClaims{Tenant: "acme", User: "ada", Extra: map[string]any{"jti": "x"}}); err == nil {
		t.Fatal("a reserved extra claim must fail")
	}
}

func TestVerifierRules(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	base := func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss": "https://app.example.com", "aud": "dive", "sub": "ada", "tenant": "acme",
			"jti": "run-1", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
		}
	}
	verifier := token.NewVerifier(pub, token.Issuer("https://app.example.com"))
	sign := func(c jwt.MapClaims) string { return mintLikeDive(t, priv, c) }
	with := func(edit func(jwt.MapClaims)) string {
		c := base()
		edit(c)
		return sign(c)
	}
	hmac, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, base()).SignedString([]byte("shared"))
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, base()).SignedString(jwt.UnsafeAllowNoneSignatureType)
	ok := map[string]string{
		"valid":                  sign(base()),
		"expired within leeway":  with(func(c jwt.MapClaims) { c["exp"] = now.Add(-20 * time.Second).Unix() }),
		"nbf within leeway":      with(func(c jwt.MapClaims) { c["nbf"] = now.Add(20 * time.Second).Unix() }),
		"audience in a list":     with(func(c jwt.MapClaims) { c["aud"] = []string{"other", "dive"} }),
		"no jti on a read token": with(func(c jwt.MapClaims) { delete(c, "jti") }),
	}
	for name, raw := range ok {
		if _, err := verifier.Verify(raw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]string{
		"expired":        with(func(c jwt.MapClaims) { c["exp"] = now.Add(-time.Minute).Unix() }),
		"not yet valid":  with(func(c jwt.MapClaims) { c["nbf"] = now.Add(time.Minute).Unix() }),
		"wrong issuer":   with(func(c jwt.MapClaims) { c["iss"] = "https://evil.example.com" }),
		"wrong audience": with(func(c jwt.MapClaims) { c["aud"] = "other" }),
		"no exp":         with(func(c jwt.MapClaims) { delete(c, "exp") }),
		"no sub":         with(func(c jwt.MapClaims) { delete(c, "sub") }),
		"no tenant":      with(func(c jwt.MapClaims) { delete(c, "tenant") }),
		"other key":      mintLikeDive(t, other, base()),
		"hmac":           hmac,
		"none":           none,
		"garbage":        "not.a.jwt",
		"empty":          "",
	}
	for name, raw := range bad {
		_, err := verifier.Verify(raw)
		if !errors.Is(err, token.ErrInvalidToken) {
			t.Errorf("%s: got %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestWithDevAcceptsATokenMintedByDive(t *testing.T) {
	dir := dataDir(t)
	priv, privatePEM, _ := rustDevKey(t)
	if err := os.WriteFile(filepath.Join(dir, "dev-key.pem"), []byte(privatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	appPub, _, _ := ed25519.GenerateKey(rand.Reader)
	app := token.NewVerifier(appPub, token.Issuer("https://app.example.com"))
	raw := mintLikeDive(t, priv, devRunClaims(time.Now()))
	if _, err := app.Verify(raw); err == nil {
		t.Fatal("a verifier without WithDev must refuse a dev token")
	}
	key := token.DevKey()
	if key.Err() != nil {
		t.Fatal(key.Err())
	}
	claims, err := app.WithDev(key).Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !claims.Dev || claims.Tenant != "acme" || claims.Subject != "ada@example.com" || claims.ID != "run-123" || claims.Plan != "file_issue" {
		t.Fatalf("claims %+v", claims)
	}
	if claims.Extra["role"] != "admin" {
		t.Fatalf("extra %v", claims.Extra)
	}
	if _, ok := claims.Extra["dive_dev"]; ok {
		t.Fatal("dive_dev is reserved, not extra")
	}
	forged := mintLikeDive(t, priv, func() jwt.MapClaims {
		c := devRunClaims(time.Now())
		c["iss"] = "https://app.example.com"
		return c
	}())
	if _, err := app.WithDev(key).Verify(forged); err == nil {
		t.Fatal("the dev key must not sign for the app's issuer")
	}
}

func TestDevKeyPrefersThePublicFile(t *testing.T) {
	dir := dataDir(t)
	priv, _, publicPEM := rustDevKey(t)
	if err := os.WriteFile(filepath.Join(dir, "dev-key.pub.pem"), []byte(publicPEM), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := token.DevKey().PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(priv.Public()) {
		t.Fatal("wrong key from dev-key.pub.pem")
	}
}

func TestDevKeyFromEnvironment(t *testing.T) {
	dir := dataDir(t)
	priv, _, publicPEM := rustDevKey(t)
	t.Setenv("DIVE_DEV_PUBLIC_KEY", publicPEM)
	got, err := token.DevKey().PublicKey()
	if err != nil || !got.Equal(priv.Public()) {
		t.Fatalf("PEM text: %v", err)
	}
	path := filepath.Join(dir, "elsewhere.pem")
	if err := os.WriteFile(path, []byte(publicPEM), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIVE_DEV_PUBLIC_KEY", path)
	got, err = token.DevKey().PublicKey()
	if err != nil || !got.Equal(priv.Public()) {
		t.Fatalf("path: %v", err)
	}
}

func TestDevKeyMissing(t *testing.T) {
	dataDir(t)
	err := token.DevKey().Err()
	if err == nil || !strings.Contains(err.Error(), "dive serve --dev-key") {
		t.Fatalf("got %v", err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	verifier := token.NewVerifier(pub, token.Issuer("app")).WithDev(token.DevKey())
	_, err = verifier.Verify(mintLikeDive(t, priv, devRunClaims(time.Now())))
	if !errors.Is(err, token.ErrInvalidToken) || !strings.Contains(err.Error(), "dev key isn't loaded") {
		t.Fatalf("got %v", err)
	}
}

func TestDevKeyAndSignerFailInProduction(t *testing.T) {
	dir := dataDir(t)
	_, privatePEM, publicPEM := rustDevKey(t)
	if err := os.WriteFile(filepath.Join(dir, "dev-key.pem"), []byte(privatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIVE_DEV_PUBLIC_KEY", publicPEM)
	for _, env := range []string{"production", "Production"} {
		t.Setenv("DIVE_ENV", env)
		if err := token.DevKey().Err(); !errors.Is(err, token.ErrProduction) {
			t.Fatalf("DevKey under %s: %v", env, err)
		}
		if _, err := token.DevSigner(); !errors.Is(err, token.ErrProduction) {
			t.Fatalf("DevSigner under %s: %v", env, err)
		}
	}
	t.Setenv("DIVE_ENV", "staging")
	if err := token.DevKey().Err(); err != nil {
		t.Fatal(err)
	}
}

func TestDevSignerMintsTokensTheDevKeyVerifies(t *testing.T) {
	dir := dataDir(t)
	_, privatePEM, _ := rustDevKey(t)
	if err := os.WriteFile(filepath.Join(dir, "dev-key.pem"), []byte(privatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := token.DevSigner()
	if err != nil {
		t.Fatal(err)
	}
	start, runID, err := signer.Start(token.StartClaims{Plan: "file_issue", Tenant: "globex", User: "hank@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	claims, err := token.NewVerifier(pub, token.Issuer("app")).WithDev(token.DevKey()).Verify(string(start))
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "dive-dev" || claims.ID != runID || claims.Tenant != "globex" || !claims.Dev {
		t.Fatalf("claims %+v", claims)
	}
	t.Setenv("DIVE_DEV_KEY", privatePEM)
	if err := os.Remove(filepath.Join(dir, "dev-key.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := token.DevSigner(); err != nil {
		t.Fatalf("DIVE_DEV_KEY as PEM text: %v", err)
	}
}

func TestParseKeysInOtherPEMForms(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalECPrivateKey(ec)
	if _, err := token.ParsePrivateKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})); err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	key, err := token.ParsePublicKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := token.NewSigner(ec, token.Issuer("app"), token.Audience("custom"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := signer.Reader(token.ReadClaims{Tenant: "acme", User: "ada", Scopes: []string{"runs:read"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := token.NewVerifier(key, token.Issuer("app")).Verify(string(read)); err == nil {
		t.Fatal("the default audience must refuse a custom-audience token")
	}
	claims, err := token.NewVerifier(key, token.Issuer("app"), token.Audience("custom")).Verify(string(read))
	if err != nil {
		t.Fatal(err)
	}
	if claims.HasScope("runs:cancel") || !claims.HasScope("runs:read") {
		t.Fatalf("scope %q", claims.Scope)
	}
}

func TestAVerifierThatTrustsAKeyNeedsAnIssuer(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := token.NewVerifier(pub).Err(); err == nil || !strings.Contains(err.Error(), "Issuer") {
		t.Fatalf("NewVerifier: %v", err)
	}
	if err := token.NewVerifier(nil).WithKey(pub).Err(); err == nil {
		t.Fatal("WithKey without an Issuer should fail")
	}
	if err := token.NewVerifier(nil).Err(); err != nil {
		t.Fatalf("a dev-only Verifier: %v", err)
	}
}

func TestDevSignerKeepsTheDevIssuer(t *testing.T) {
	dir := dataDir(t)
	_, privatePEM, publicPEM := rustDevKey(t)
	if err := os.WriteFile(filepath.Join(dir, "dev-key.pem"), []byte(privatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIVE_DEV_PUBLIC_KEY", publicPEM)
	signer, err := token.DevSigner(token.Issuer("https://app.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := signer.Start(token.StartClaims{Plan: "p", Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := token.NewVerifier(nil).WithDev(token.DevKey()).Verify(string(start))
	if err != nil || claims.Issuer != token.DevIssuer || !claims.Dev {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}
