// Package token mints and verifies Dive run tokens.
//
// A run token is a JWT that an app signs with its own private key. dive
// serve trusts the matching public key, so every request to dive serve
// carries a token that says which tenant and user it acts for. dive serve
// forwards the start token with each call it makes to the app's tools, so
// the tools learn the same tenant and user.
//
// # Minting tokens
//
// A [Signer] mints two kinds of token. A start token, from
// [Signer.Start], starts one run of one plan; its jti is the run's id. A
// read token, from [Signer.Reader], carries scopes such as runs:read for
// listing plans and reading, following and cancelling runs:
//
//	key, err := token.ParsePrivateKeyPEM(pemBytes)
//	if err != nil {
//		return err
//	}
//	signer, err := token.NewSigner(key, token.Issuer("https://app.example.com"))
//	if err != nil {
//		return err
//	}
//	start, runID, err := signer.Start(token.StartClaims{Plan: "triage", Tenant: "acme", User: "ada@example.com"})
//
// Keys are Ed25519 (EdDSA), ECDSA on P-256 (ES256) or RSA (RS256). Tokens
// are valid for [DefaultTTL] unless the claims set a TTL. A [Token]
// redacts itself when printed or logged.
//
// # Verifying tokens
//
// A tool handler verifies the token of each call with a [Verifier]:
//
//	verifier := token.NewVerifier(appPublicKey, token.Issuer("https://app.example.com"))
//	claims, err := verifier.Verify(raw)
//
// The handler from the tools package does this itself when given the
// Verifier, and passes the claims to each tool.
//
// # Development keys
//
// dive serve --dev-key, Dive desktop and the dive CLI sign tokens with a
// local Ed25519 key in ~/.local/share/dive/serve, with the issuer
// [DevIssuer], "dive-dev". [DevKey] loads its public half, or reads it
// from the DIVE_DEV_PUBLIC_KEY environment variable, and
// [Verifier.WithDev] makes a Verifier accept tokens it signed, marked
// with [Claims.Dev]. [DevSigner] mints tokens with the private key, read
// from DIVE_DEV_KEY or dev-key.pem, for an app that starts runs on a
// dive serve --dev-key.
//
// Both refuse to load the key when the DIVE_ENV environment variable is
// production: DevSigner returns [ErrProduction], and the key from DevKey
// carries it, so a Verifier given that key refuses development tokens.
package token
