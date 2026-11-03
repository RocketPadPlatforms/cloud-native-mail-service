/*
Copyright 2026 Rocket Pad Platforms GmbH.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package dkim holds the pure parts of DKIM key handling (ADR 0011):
// key generation and parsing, DNS values, selector names and the
// rotation state machine. It uses no Kubernetes API.
package dkim

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// Algorithm is a DKIM key algorithm.
type Algorithm string

const (
	// RSA is RSA-2048 (RFC 6376).
	RSA Algorithm = "rsa"
	// Ed25519 is the optional second signature (RFC 8463).
	Ed25519 Algorithm = "ed25519"
)

const (
	// RSABits is the size of generated RSA keys.
	RSABits = 2048
	// MinRSABits is the floor for keys that are provided, not generated
	// (RFC 8301 allows 1024; a lower floor would only invite weak keys).
	MinRSABits = 2048
	// MaxKeySize is the largest accepted key file, in bytes.
	MaxKeySize = 16 << 10
)

// Key is a parsed private key, reduced to what the operator needs: the
// algorithm and the public key. The private key is not retained.
type Key struct {
	Algorithm Algorithm
	// Bits is the RSA modulus size; zero for Ed25519.
	Bits int
	// public is the DER SubjectPublicKeyInfo (RSA) or the raw 32-byte
	// key (Ed25519), the bytes that go into p= base64-encoded.
	public []byte
}

// Generate returns a new private key as a PKCS#8 PEM, the format
// ADR 0011 stores in the key Secrets.
func Generate(alg Algorithm, random io.Reader) ([]byte, error) {
	var priv any
	var err error
	switch alg {
	case RSA:
		priv, err = rsa.GenerateKey(random, RSABits)
	case Ed25519:
		_, priv, err = ed25519.GenerateKey(random)
	default:
		return nil, fmt.Errorf("unsupported DKIM algorithm %q", alg)
	}
	if err != nil {
		return nil, fmt.Errorf("generating %s key: %w", alg, err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encoding %s key: %w", alg, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// Parse reads a private key in one of the formats the pinned Rspamd
// loads (ADR 0011): a PEM "PRIVATE KEY" (PKCS#8, RSA or Ed25519) or
// "RSA PRIVATE KEY" (PKCS#1), or, without PEM armour, base64 or raw
// bytes that are an Ed25519 seed (32 bytes), an Ed25519 secret key
// (64 bytes, what `rspamadm dkim_keygen -t ed25519` writes) or a DER
// private key. RSA keys must have at least MinRSABits; other key types
// are rejected.
func Parse(data []byte) (*Key, error) {
	if len(data) > MaxKeySize {
		return nil, fmt.Errorf("key is larger than %d bytes", MaxKeySize)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("key is empty")
	}
	if bytes.HasPrefix(trimmed, []byte("-----BEGIN")) {
		return parsePEM(trimmed)
	}
	// Rspamd tries base64 text first and falls back to raw bytes.
	raw := trimmed
	if dec, err := base64.StdEncoding.DecodeString(string(trimmed)); err == nil {
		raw = dec
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519Key(ed25519.NewKeyFromSeed(raw)), nil
	case ed25519.PrivateKeySize:
		priv := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
		if !bytes.Equal(priv, raw) {
			return nil, errors.New("ed25519 secret key: public half does not match the seed")
		}
		return ed25519Key(priv), nil
	}
	return parseDER(raw)
}

func parsePEM(data []byte) (*Key, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("malformed PEM")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("PEM holds more than one block; provide exactly one private key")
	}
	switch block.Type {
	case "PRIVATE KEY":
		return parseDER(block.Bytes)
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing RSA PKCS#1 key: %w", err)
		}
		return rsaKey(k)
	case "EC PRIVATE KEY":
		return nil, errors.New("unsupported key type: ECDSA (use RSA or Ed25519)")
	case "ENCRYPTED PRIVATE KEY":
		return nil, errors.New("encrypted private keys are not supported")
	}
	return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
}

// parseDER reads an unarmoured private key: PKCS#8 first (RSA or
// Ed25519), then PKCS#1, as OpenSSL does.
func parseDER(der []byte) (*Key, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		switch k := k.(type) {
		case *rsa.PrivateKey:
			return rsaKey(k)
		case ed25519.PrivateKey:
			return ed25519Key(k), nil
		}
		return nil, fmt.Errorf("unsupported key type %T (use RSA or Ed25519)", k)
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return rsaKey(k)
	}
	return nil, errors.New("not a private key in a supported format (PKCS#8, PKCS#1 or Ed25519)")
}

func rsaKey(k *rsa.PrivateKey) (*Key, error) {
	bits := k.N.BitLen()
	if bits < MinRSABits {
		return nil, fmt.Errorf("RSA key has %d bits, at least %d are required", bits, MinRSABits)
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("encoding RSA public key: %w", err)
	}
	return &Key{Algorithm: RSA, Bits: bits, public: pub}, nil
}

func ed25519Key(k ed25519.PrivateKey) *Key {
	return &Key{Algorithm: Ed25519, public: k.Public().(ed25519.PublicKey)}
}

// PublicKey returns the base64 value of the p= tag: the SPKI DER for
// RSA (RFC 6376), the raw key for Ed25519 (RFC 8463).
func (k *Key) PublicKey() string {
	return base64.StdEncoding.EncodeToString(k.public)
}

// TXTValue returns the DKIM TXT record value for the key, as one string
// (split it into 255-octet strings when publishing).
func (k *Key) TXTValue() string {
	return "v=DKIM1; k=" + string(k.Algorithm) + "; p=" + k.PublicKey()
}
