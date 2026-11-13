package token

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// ParsePrivateKeyPEM parses the first PEM block in data as a private key
// for [NewSigner]. It reads PKCS #8 ("PRIVATE KEY"), SEC 1 ("EC PRIVATE
// KEY") and PKCS #1 ("RSA PRIVATE KEY") blocks. It doesn't check that the
// key is a kind NewSigner accepts.
func ParsePrivateKeyPEM(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("token: no PEM block in the private key")
	}
	switch block.Type {
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("token: can't parse the PKCS#8 private key: %w", err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, errors.New("token: the private key can't sign")
		}
		return signer, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("token: can't parse the EC private key: %w", err)
		}
		return key, nil
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("token: can't parse the RSA private key: %w", err)
		}
		return key, nil
	}
	return nil, fmt.Errorf("token: unsupported PEM block %q for a private key", block.Type)
}

// ParsePublicKeyPEM parses the first PEM block in data as a public key
// for [NewVerifier]. It reads PKIX ("PUBLIC KEY") and PKCS #1 ("RSA
// PUBLIC KEY") blocks. It doesn't check that the key is a kind
// NewVerifier accepts.
func ParsePublicKeyPEM(data []byte) (crypto.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("token: no PEM block in the public key")
	}
	switch block.Type {
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("token: can't parse the public key: %w", err)
		}
		return key, nil
	case "RSA PUBLIC KEY":
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("token: can't parse the RSA public key: %w", err)
		}
		return key, nil
	}
	return nil, fmt.Errorf("token: unsupported PEM block %q for a public key", block.Type)
}

func signingMethod(key crypto.Signer) (jwt.SigningMethod, error) {
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return jwt.SigningMethodEdDSA, nil
	case *ecdsa.PrivateKey:
		if k.Curve != elliptic.P256() {
			return nil, errors.New("token: ECDSA keys must use P-256 for ES256")
		}
		return jwt.SigningMethodES256, nil
	case *rsa.PrivateKey:
		return jwt.SigningMethodRS256, nil
	}
	return nil, fmt.Errorf("token: unsupported private key %T; use Ed25519, P-256 or RSA", key)
}

func checkPublic(key crypto.PublicKey) (crypto.PublicKey, error) {
	switch k := key.(type) {
	case ed25519.PublicKey:
		return k, nil
	case *ed25519.PublicKey:
		return *k, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return nil, errors.New("token: ECDSA keys must use P-256 for ES256")
		}
		return k, nil
	case *rsa.PublicKey:
		return k, nil
	case ed25519.PrivateKey:
		return k.Public(), nil
	case crypto.Signer:
		return checkPublic(k.Public())
	}
	return nil, fmt.Errorf("token: unsupported public key %T; use Ed25519, P-256 or RSA", key)
}

func family(key crypto.PublicKey) string {
	switch key.(type) {
	case ed25519.PublicKey:
		return "EdDSA"
	case *ecdsa.PublicKey:
		return "ES256"
	case *rsa.PublicKey:
		return "RS256"
	}
	return ""
}
