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

package dkim

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

// rfc8463Seed and rfc8463Public are the Ed25519 example of RFC 8463
// Appendix A (the RFC 8032 section 7.1 test 1 key).
const (
	rfc8463Seed   = "nWGxne/9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A="
	rfc8463Public = "v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="
)

func mustPEM(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func TestGenerateRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		alg      Algorithm
		wantBits int
		wantK    string
	}{
		{RSA, 2048, "v=DKIM1; k=rsa; p=MIIBIjAN"},
		{Ed25519, 0, "v=DKIM1; k=ed25519; p="},
	} {
		t.Run(string(tc.alg), func(t *testing.T) {
			data, err := Generate(tc.alg, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), "-----BEGIN PRIVATE KEY-----\n") {
				t.Fatalf("not a PKCS#8 PEM: %.40q", data)
			}
			k, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if k.Algorithm != tc.alg || k.Bits != tc.wantBits {
				t.Fatalf("got %s/%d, want %s/%d", k.Algorithm, k.Bits, tc.alg, tc.wantBits)
			}
			if !strings.HasPrefix(k.TXTValue(), tc.wantK) {
				t.Fatalf("TXT value %.40q does not start with %q", k.TXTValue(), tc.wantK)
			}
		})
	}
	t.Run("unsupported algorithm", func(t *testing.T) {
		if _, err := Generate("ecdsa", rand.Reader); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestParseFormats(t *testing.T) {
	rsaKey2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(rsaKey2048)
	pkcs1 := x509.MarshalPKCS1PrivateKey(rsaKey2048)
	wantRSA := "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(mustMarshalPKIX(t, &rsaKey2048.PublicKey))

	seed, _ := base64.StdEncoding.DecodeString(rfc8463Seed)
	edKey := ed25519.NewKeyFromSeed(seed)
	edPKCS8, _ := x509.MarshalPKCS8PrivateKey(edKey)

	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"rsa pkcs8 pem", mustPEM(t, "PRIVATE KEY", pkcs8), wantRSA},
		{"rsa pkcs1 pem", mustPEM(t, "RSA PRIVATE KEY", pkcs1), wantRSA},
		{"rsa pkcs8 pem with surrounding whitespace", append([]byte("\n\n"), append(mustPEM(t, "PRIVATE KEY", pkcs8), '\n', '\n')...), wantRSA},
		{"rsa pkcs8 base64 der", []byte(base64.StdEncoding.EncodeToString(pkcs8)), wantRSA},
		{"rsa pkcs1 raw der", pkcs1, wantRSA},
		{"ed25519 pkcs8 pem", mustPEM(t, "PRIVATE KEY", edPKCS8), rfc8463Public},
		{"ed25519 base64 seed", []byte(rfc8463Seed + "\n"), rfc8463Public},
		{"ed25519 raw seed", seed, rfc8463Public},
		{"ed25519 base64 secret key (dkim_keygen)", []byte(base64.StdEncoding.EncodeToString(edKey)), rfc8463Public},
		{"ed25519 raw secret key", edKey, rfc8463Public},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := Parse(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if got := k.TXTValue(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A raw key may end in a byte that is whitespace: it is key material,
// not padding (a random PKCS#1 key did this in about 2 of 100 runs).
func TestParseRawKeyEndingInWhitespace(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	for _, last := range []byte{'\n', ' ', '\t'} {
		seed[len(seed)-1] = last
		k, err := Parse(seed)
		if err != nil {
			t.Fatalf("seed ending in %q: %v", last, err)
		}
		want := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		if got := k.PublicKey(); got != base64.StdEncoding.EncodeToString(want) {
			t.Errorf("seed ending in %q: wrong public key", last)
		}
	}
}

func mustMarshalPKIX(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestParseRejects(t *testing.T) {
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	smallDER, _ := x509.MarshalPKCS8PrivateKey(small)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecPKCS8, _ := x509.MarshalPKCS8PrivateKey(ec)
	ecSEC1, _ := x509.MarshalECPrivateKey(ec)
	seed, _ := base64.StdEncoding.DecodeString(rfc8463Seed)
	badFull := append(ed25519.NewKeyFromSeed(seed)[:32:32], make([]byte, 32)...)
	good := mustPEM(t, "PRIVATE KEY", smallDER)

	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty", []byte(" \n"), "empty"},
		{"too large", []byte(strings.Repeat("a", MaxKeySize+1)), "larger than"},
		{"rsa 1024", good, "1024 bits, at least 2048"},
		{"ecdsa pkcs8", mustPEM(t, "PRIVATE KEY", ecPKCS8), "unsupported key type"},
		{"ecdsa sec1", mustPEM(t, "EC PRIVATE KEY", ecSEC1), "ECDSA"},
		{"encrypted", mustPEM(t, "ENCRYPTED PRIVATE KEY", []byte("x")), "encrypted"},
		{"public key block", mustPEM(t, "PUBLIC KEY", []byte("x")), "unsupported PEM block"},
		{"two blocks", append(append([]byte{}, good...), good...), "more than one"},
		{"malformed pem", []byte("-----BEGIN PRIVATE KEY-----\n!!!\n"), "malformed PEM"},
		{"corrupt pem body", mustPEM(t, "PRIVATE KEY", []byte("garbage")), "supported format"},
		{"garbage text", []byte("not a key at all"), "supported format"},
		{"ed25519 secret key with wrong public half", badFull, "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want one containing %q", err, tc.want)
			}
		})
	}
}
