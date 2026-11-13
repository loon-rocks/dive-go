package token

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	devPrivateFile = "dev-key.pem"
	devPublicFile  = "dev-key.pub.pem"
)

// ErrProduction is returned by [DevSigner], and carried by the
// [DevPublicKey] from [DevKey], when the DIVE_ENV environment variable is
// production, compared without case.
var ErrProduction = errors.New("token: the development key is refused when DIVE_ENV is production")

// DevPublicKey is the public half of the development key, or the error
// that kept it from loading, as [DevKey] returns it. Pass it to
// [Verifier.WithDev].
type DevPublicKey struct {
	key ed25519.PublicKey
	err error
}

// Err returns the error that kept the key from loading, or nil.
func (k DevPublicKey) Err() error { return k.err }

// PublicKey returns the key, or the error that kept it from loading.
func (k DevPublicKey) PublicKey() (ed25519.PublicKey, error) { return k.key, k.err }

// DevKey loads the public half of the Ed25519 development key that
// dive serve --dev-key, Dive desktop and the dive CLI sign tokens with.
// It reads, in order:
//
//   - the DIVE_DEV_PUBLIC_KEY environment variable, as PEM text or the
//     path of a PEM file;
//   - ~/.local/share/dive/serve/dev-key.pub.pem;
//   - the public half of ~/.local/share/dive/serve/dev-key.pem.
//
// DevKey doesn't return an error; the DevPublicKey carries it, so a
// missing key can be passed to [Verifier.WithDev] and only refuses
// development tokens. When DIVE_ENV is production, the error is
// [ErrProduction] and no file is read.
func DevKey() DevPublicKey {
	if err := refuseInProduction(); err != nil {
		return DevPublicKey{err: err}
	}
	if value := os.Getenv("DIVE_DEV_PUBLIC_KEY"); strings.TrimSpace(value) != "" {
		data, source, err := pemOrPath(value, "DIVE_DEV_PUBLIC_KEY")
		if err != nil {
			return DevPublicKey{err: err}
		}
		return devPublicFromPEM(data, source)
	}
	dir, err := devKeyDir()
	if err != nil {
		return DevPublicKey{err: err}
	}
	public := filepath.Join(dir, devPublicFile)
	if data, err := os.ReadFile(public); err == nil {
		return devPublicFromPEM(data, public)
	}
	private := filepath.Join(dir, devPrivateFile)
	data, err := os.ReadFile(private)
	if err != nil {
		return DevPublicKey{err: fmt.Errorf("token: no development key at %s; run `dive serve --dev-key` once to create it, or set DIVE_DEV_PUBLIC_KEY", public)}
	}
	key, err := devPrivateFromPEM(data, private)
	if err != nil {
		return DevPublicKey{err: err}
	}
	return DevPublicKey{key: key.Public().(ed25519.PublicKey)}
}

// DevSigner returns a [Signer] that mints development tokens, with the
// issuer [DevIssuer], which dive serve --dev-key accepts. It reads the
// Ed25519 private key from the DIVE_DEV_KEY environment variable, as PEM
// text or the path of a PEM file, or else from
// ~/.local/share/dive/serve/dev-key.pem. Run dive serve --dev-key once
// to create that file.
//
// The tokens always have the issuer [DevIssuer]; an [Issuer] option is
// ignored. DevSigner returns [ErrProduction] without reading a key when
// DIVE_ENV is production.
func DevSigner(opts ...Option) (*Signer, error) {
	if err := refuseInProduction(); err != nil {
		return nil, err
	}
	var data []byte
	var source string
	if value := os.Getenv("DIVE_DEV_KEY"); strings.TrimSpace(value) != "" {
		var err error
		data, source, err = pemOrPath(value, "DIVE_DEV_KEY")
		if err != nil {
			return nil, err
		}
	} else {
		dir, err := devKeyDir()
		if err != nil {
			return nil, err
		}
		source = filepath.Join(dir, devPrivateFile)
		data, err = os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("token: no development key at %s; run `dive serve --dev-key` once to create it, or set DIVE_DEV_KEY", source)
		}
	}
	key, err := devPrivateFromPEM(data, source)
	if err != nil {
		return nil, err
	}
	return NewSigner(key, append(opts, Issuer(DevIssuer))...)
}

func refuseInProduction() error {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DIVE_ENV")), "production") {
		return ErrProduction
	}
	return nil
}

func devKeyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("token: can't find the home directory for the development key: %w", err)
	}
	return filepath.Join(home, ".local", "share", "dive", "serve"), nil
}

func pemOrPath(value, name string) ([]byte, string, error) {
	if strings.Contains(value, "-----BEGIN") {
		return []byte(value), name, nil
	}
	data, err := os.ReadFile(value)
	if err != nil {
		return nil, "", fmt.Errorf("token: can't read %s from %s: %w", name, value, err)
	}
	return data, value, nil
}

func devPublicFromPEM(data []byte, source string) DevPublicKey {
	key, err := ParsePublicKeyPEM(data)
	if err != nil {
		return DevPublicKey{err: fmt.Errorf("token: %s: %w", source, err)}
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return DevPublicKey{err: fmt.Errorf("token: %s isn't an Ed25519 public key", source)}
	}
	return DevPublicKey{key: ed}
}

func devPrivateFromPEM(data []byte, source string) (ed25519.PrivateKey, error) {
	key, err := ParsePrivateKeyPEM(data)
	if err != nil {
		return nil, fmt.Errorf("token: %s: %w", source, err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("token: %s isn't an Ed25519 private key", source)
	}
	return ed, nil
}
