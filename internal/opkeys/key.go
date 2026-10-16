// Package opkeys manages the Ed25519 key pair this backend uses as an Open
// Payments client: the key it signs its requests with.
package opkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const blockType = "PRIVATE KEY"

// Key is the client key pair.
type Key struct {
	private ed25519.PrivateKey
}

// Generate creates a new key pair.
func Generate() (Key, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, fmt.Errorf("opkeys: generating the key: %w", err)
	}

	return Key{private: private}, nil
}

// LoadFile reads a PKCS#8 PEM private key.
func LoadFile(path string) (Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Key{}, fmt.Errorf("opkeys: reading %s: %w", path, err)
	}

	block, _ := pem.Decode(raw)
	if block == nil || block.Type != blockType {
		return Key{}, fmt.Errorf("opkeys: %s is not a PEM private key", path)
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Key{}, fmt.Errorf("opkeys: parsing %s: %w", path, err)
	}

	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return Key{}, fmt.Errorf("opkeys: %s does not hold an Ed25519 key", path)
	}

	return Key{private: private}, nil
}

// LoadOrGenerate reads the key from path and creates one when the file is not
// there yet, so provisioning can run on every restart without rotating the key.
// The directory is created when it is missing.
func LoadOrGenerate(path string) (Key, error) {
	if path == "" {
		return Key{}, errors.New("opkeys: no private key path was given")
	}

	key, err := LoadFile(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Key{}, err
	}

	generated, err := Generate()
	if err != nil {
		return Key{}, err
	}
	if err := generated.WriteFile(path); err != nil {
		return Key{}, err
	}

	return generated, nil
}

// WriteFile stores the key as a PKCS#8 PEM file only its owner can read.
func (k Key) WriteFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("opkeys: creating %s: %w", dir, err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(k.private)
	if err != nil {
		return fmt.Errorf("opkeys: encoding the key: %w", err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})

	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("opkeys: writing %s: %w", path, err)
	}

	return nil
}

// PublicKeyBase64 is the public key in the unpadded base64url form Open Payments
// carries.
func (k Key) PublicKeyBase64() string {
	public, ok := k.private.Public().(ed25519.PublicKey)
	if !ok {
		return ""
	}

	return base64.RawURLEncoding.EncodeToString(public)
}

// Kid names this key. It is the RFC 7638 JWK thumbprint of the public key, so
// both sides compute the same name without coordinating anything.
func (k Key) Kid() string {
	thumbprint := fmt.Sprintf(`{"crv":"Ed25519","kty":"OKP","x":"%s"}`, k.PublicKeyBase64())
	sum := sha256.Sum256([]byte(thumbprint))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}
