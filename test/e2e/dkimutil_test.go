//go:build e2e

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

package e2e

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	msgdkim "github.com/emersion/go-msgauth/dkim"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// TestZoneFile checks that a long TXT value (an RSA-2048 DKIM record) is
// split into character-strings of at most 255 octets that join to the
// original value. E2E_ZONE_OUT writes the zone to a file, so it can be
// loaded into a real CoreDNS by hand.
func TestZoneFile(t *testing.T) {
	value := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 600)
	zone := zoneFile(1, []cnmsv1alpha1.DNSRecord{{
		Name: "s1._domainkey." + dnsZone, Type: "TXT", Value: value,
	}})
	if out := os.Getenv("E2E_ZONE_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(zone), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var line string
	for l := range strings.Lines(zone) {
		if strings.HasPrefix(l, "s1._domainkey.") {
			line = l
		}
	}
	rest, ok := strings.CutPrefix(line, "s1._domainkey."+dnsZone+". IN TXT ")
	if !ok {
		t.Fatalf("no TXT line for the record in:\n%s", zone)
	}
	var joined strings.Builder
	for part := range strings.SplitSeq(strings.TrimSpace(rest), `" "`) {
		part = strings.Trim(part, `"`)
		if len(part) > 255 {
			t.Errorf("character-string of %d octets, want at most 255", len(part))
		}
		joined.WriteString(part)
	}
	if joined.String() != value {
		t.Errorf("joined value %q, want %q", joined.String(), value)
	}
}

// TestDKIMSignatures checks the tag parsing of a folded, repeated header.
func TestDKIMSignatures(t *testing.T) {
	raw := "DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d=example.org;\r\n" +
		"\ts=cnms-20261104; h=from:to; bh=abc=;\r\n b=def\r\n" +
		"DKIM-Signature: v=1; a=ed25519-sha256; d=example.org; s=cnms-ed25519-20261104; b=x\r\n" +
		"From: alice@example.org\r\n\r\nbody\r\n"
	got, err := dkimSignatures(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []dkimSignature{
		{algRSA, "example.org", "cnms-20261104"},
		{algEd25519, "example.org", "cnms-ed25519-20261104"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestVerifyDKIM signs a message and checks that verifyDKIM accepts it
// against the matching record, and rejects it against another key and
// without a record, for Ed25519 and RSA keys.
func TestVerifyDKIM(t *testing.T) {
	const domain, selector = "example.org", "s1"
	name := selector + "._domainkey." + domain
	message := "From: alice@example.org\r\nTo: bob@example.net\r\nSubject: test\r\n\r\nhello\r\n"

	edPub, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	record := func(value string) []cnmsv1alpha1.DNSRecord {
		return []cnmsv1alpha1.DNSRecord{{Name: name, Type: "TXT", Value: value}}
	}
	edRecord := func(pub ed25519.PublicKey) []cnmsv1alpha1.DNSRecord {
		return record("v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub))
	}
	rsaRecord := record("v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(rsaDER))

	tests := []struct {
		name    string
		signer  crypto.Signer
		records []cnmsv1alpha1.DNSRecord
		wantErr bool
	}{
		{"ed25519 with its record", edKey, edRecord(edPub), false},
		{"ed25519 with another key", edKey, edRecord(otherPub), true},
		{"ed25519 without a record", edKey, nil, true},
		{"ed25519 with the record in upper case and a trailing dot", edKey,
			[]cnmsv1alpha1.DNSRecord{{Name: strings.ToUpper(name) + ".", Type: "TXT",
				Value: "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(edPub)}}, false},
		{"rsa with its record", rsaKey, rsaRecord, false},
		{"rsa with an ed25519 record", rsaKey, edRecord(edPub), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var signed bytes.Buffer
			opts := &msgdkim.SignOptions{Domain: domain, Selector: selector, Signer: tc.signer}
			if err := msgdkim.Sign(&signed, strings.NewReader(message), opts); err != nil {
				t.Fatal(err)
			}
			got, err := verifyDKIM(signed.String(), tc.records)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d verifications, want 1", len(got))
			}
			if (got[0].Err != nil) != tc.wantErr {
				t.Errorf("verification error = %v, want an error: %v", got[0].Err, tc.wantErr)
			}
		})
	}
}
