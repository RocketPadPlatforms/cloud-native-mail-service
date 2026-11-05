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

package dnsrecords

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	edP        = "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="
	otherP     = "D9Sur5rktcxNIJvr1l8OjT1ID5c+BzyuzlFzZhzwBHM="
	dmarcWant  = "v=DMARC1; p=reject; rua=mailto:agg@example.org"
	spfWant    = "v=spf1 ip4:203.0.113.9 include:_spf.example.net ~all"
	stsWant    = "v=STSv1; id=abc123"
	tlsrptWant = "v=TLSRPTv1; rua=mailto:t@example.org"

	reasonNoDKIM  = "no DKIM record"
	reasonDiffers = "differs"
	reasonOnlyOne = "exactly one"
)

func record(p Purpose, value string) Record {
	return txt("name.example.org", p, value)
}

func TestEvaluate(t *testing.T) {
	siteToken := "google-site-verification=xyz"
	for _, tc := range []struct {
		name     string
		purpose  Purpose
		expected string
		observed []string
		verified bool
		reason   string // substring of the reason when not verified
	}{
		// DKIM
		{"dkim exact", PurposeDKIM, edValue, []string{edValue}, true, ""},
		{"dkim tag order, extra tags and folding", PurposeDKIM, edValue,
			[]string{"t=s;  p=11qYAYKxCrfVS/7Ty WQHOg7hcvPapiMlrwIa\t aPcHURo= ; s=email; k=ed25519; v=DKIM1;"}, true, ""},
		{"dkim missing padding", PurposeDKIM, edValue, []string{"v=DKIM1; k=ed25519; p=" + strings.TrimRight(edP, "=")}, true, ""},
		{"dkim rsa is the default key type", PurposeDKIM, "v=DKIM1; k=rsa; p=QUJD", []string{"p=QUJD"}, true, ""},
		{"dkim ed25519 needs k", PurposeDKIM, edValue, []string{"v=DKIM1; p=" + edP}, false, "key type differs"},
		{"dkim wrong key type", PurposeDKIM, edValue, []string{"v=DKIM1; k=rsa; p=" + edP}, false, "key type differs"},
		{"dkim wrong key", PurposeDKIM, edValue, []string{"v=DKIM1; k=ed25519; p=" + otherP}, false, "public key differs"},
		{"dkim revoked key", PurposeDKIM, edValue, []string{"v=DKIM1; k=ed25519; p="}, false, "public key differs"},
		{"dkim other version is ignored", PurposeDKIM, edValue, []string{"v=DKIM2; k=ed25519; p=" + edP}, false, reasonNoDKIM},
		{"dkim unparsable record is ignored", PurposeDKIM, edValue, []string{"garbage", "v=DKIM1; v=DKIM1; k=ed25519; p=" + edP}, false, reasonNoDKIM},
		{"dkim among other records", PurposeDKIM, edValue, []string{siteToken, "v=DKIM1; k=ed25519; p=" + otherP, edValue}, false, "2 DKIM records"},
		{"dkim one record among other records", PurposeDKIM, edValue, []string{siteToken, edValue}, true, ""},
		{"dkim nothing there", PurposeDKIM, edValue, []string{siteToken}, false, reasonNoDKIM},
		// DMARC
		{"dmarc exact", PurposeDMARC, dmarcWant, []string{dmarcWant}, true, ""},
		{"dmarc whitespace, case of names and trailing semicolon", PurposeDMARC, dmarcWant,
			[]string{" v = DMARC1 ;P=reject;  RUA=mailto:agg@example.org ; "}, true, ""},
		{"dmarc tag order does not matter after v", PurposeDMARC, dmarcWant, []string{"v=DMARC1; rua=mailto:agg@example.org; p=reject"}, true, ""},
		{"dmarc address list spacing", PurposeDMARC, "v=DMARC1; p=none; rua=mailto:a@example.org,mailto:b@example.org",
			[]string{"v=DMARC1; p=none; rua=mailto:a@example.org , mailto:b@example.org"}, true, ""},
		{"dmarc extra tag", PurposeDMARC, dmarcWant, []string{dmarcWant + "; pct=100"}, false, reasonDiffers},
		{"dmarc other policy", PurposeDMARC, dmarcWant, []string{"v=DMARC1; p=none; rua=mailto:agg@example.org"}, false, reasonDiffers},
		{"dmarc missing tag", PurposeDMARC, dmarcWant, []string{"v=DMARC1; p=reject"}, false, reasonDiffers},
		{"dmarc duplicate tag", PurposeDMARC, dmarcWant, []string{dmarcWant + "; p=none"}, false, "not a valid"},
		{"dmarc two records", PurposeDMARC, dmarcWant, []string{dmarcWant, dmarcWant}, false, reasonOnlyOne},
		{"dmarc version not first", PurposeDMARC, dmarcWant, []string{"p=reject; v=DMARC1; rua=mailto:agg@example.org"}, false, "no TXT record starts"},
		{"dmarc ignores other records", PurposeDMARC, dmarcWant, []string{siteToken, dmarcWant}, true, ""},
		{"dmarc nothing there", PurposeDMARC, dmarcWant, nil, false, "no TXT record starts"},
		// MTA-STS and TLS-RPT
		{"mta-sts exact", PurposeMTASTS, stsWant, []string{stsWant}, true, ""},
		{"mta-sts old id", PurposeMTASTS, stsWant, []string{"v=STSv1; id=old"}, false, reasonDiffers},
		{"mta-sts two records", PurposeMTASTS, stsWant, []string{stsWant, "v=STSv1; id=old"}, false, reasonOnlyOne},
		{"tls-rpt exact", PurposeTLSRPT, tlsrptWant, []string{tlsrptWant + ";"}, true, ""},
		{"tls-rpt other address", PurposeTLSRPT, tlsrptWant, []string{"v=TLSRPTv1; rua=mailto:x@example.org"}, false, reasonDiffers},
		{"tls-rpt two records", PurposeTLSRPT, tlsrptWant, []string{tlsrptWant, tlsrptWant}, false, reasonOnlyOne},
		// SPF
		{"spf exact", PurposeSPF, spfWant, []string{spfWant}, true, ""},
		{"spf case and whitespace", PurposeSPF, spfWant, []string{"V=SPF1  IP4:203.0.113.9\tinclude:_SPF.example.net  ~ALL "}, true, ""},
		{"spf different", PurposeSPF, spfWant, []string{spfNone}, false, spfNone},
		{"spf order matters", PurposeSPF, spfWant, []string{"v=spf1 include:_spf.example.net ip4:203.0.113.9 ~all"}, false, reasonDiffers},
		{"spf two records", PurposeSPF, spfWant, []string{spfWant, spfWant}, false, "2 SPF records"},
		{"spf two records, one other", PurposeSPF, spfWant, []string{spfWant, spfNone}, false, "2 SPF records"},
		{"spf version prefix is not spf", PurposeSPF, spfWant, []string{"v=spf10 something", spfWant}, true, ""},
		{"spf ignores other records", PurposeSPF, spfWant, []string{siteToken, spfWant}, true, ""},
		{"spf nothing there", PurposeSPF, spfWant, []string{siteToken}, false, "no SPF"},
		{"unsupported purpose", "other", "x", []string{"x"}, false, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Evaluate(record(tc.purpose, tc.expected), tc.observed)
			if res.Verified != tc.verified || res.Unknown {
				t.Fatalf("verified = %v (unknown %v), reason %q; want verified = %v", res.Verified, res.Unknown, res.Reason, tc.verified)
			}
			if tc.verified && res.Reason != "" {
				t.Fatalf("a verified record has the reason %q", res.Reason)
			}
			if !tc.verified && !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", res.Reason, tc.reason)
			}
			if !reflect.DeepEqual(res.Observed, tc.observed) {
				t.Fatalf("observed %q, want %q", res.Observed, tc.observed)
			}
		})
	}
}

func TestVerifyUsesTheResolver(t *testing.T) {
	ctx := context.Background()
	rec := record(PurposeDMARC, dmarcWant)

	t.Run("found", func(t *testing.T) {
		f := NewFakeResolver()
		f.Set(rec.Name, "other", dmarcWant)
		if res := Verify(ctx, f, rec); !res.Verified || res.Unknown {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("a long DKIM value split into strings is verified", func(t *testing.T) {
		k := rsaKey(t)
		dkimRec := txt("s._domainkey.example.org", PurposeDKIM, k.TXTValue())
		if len(dkimRec.Values) < 2 {
			t.Fatal("test needs a value over 255 octets")
		}
		f := NewFakeResolver()
		f.Set(dkimRec.Name, dkimRec.Value()) // the resolver joins the strings
		if res := Verify(ctx, f, dkimRec); !res.Verified {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("not found is a definite no", func(t *testing.T) {
		res := Verify(ctx, NewFakeResolver(), rec)
		if res.Verified || res.Unknown || !strings.Contains(res.Reason, "no TXT record at "+rec.Name) {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("a temporary failure is unknown, not failed", func(t *testing.T) {
		f := NewFakeResolver()
		f.SetError(rec.Name, errors.New("SERVFAIL"))
		res := Verify(ctx, f, rec)
		if res.Verified || !res.Unknown || !strings.Contains(res.Reason, "SERVFAIL") {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("not found and a different record", func(t *testing.T) {
		f := NewFakeResolver()
		f.Set(rec.Name, "v=DMARC1; p=none")
		res := Verify(ctx, f, rec)
		if res.Verified || res.Unknown || !reflect.DeepEqual(res.Observed, []string{"v=DMARC1; p=none"}) {
			t.Fatalf("got %+v", res)
		}
	})
}

func TestEvaluateComposedRecordsVerifyAgainstThemselves(t *testing.T) {
	res, err := Compose(Input{
		Domain: testDomain,
		DKIM:   []DKIMKey{{"a", edKey(t)}, {"b", rsaKey(t)}},
		SPF:    &SPF{IP4: []string{ip4Host}, IP6: []string{ip6Host}},
		DMARC:  &DMARC{Policy: policyReject, Testing: true, Rua: "a@example.org", Ruf: "b@example.org"},
		MTASTS: &MTASTS{MX: []string{mx1}},
		TLSRPT: &TLSRPT{Rua: "t@example.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 6 {
		t.Fatalf("got %d records", len(res.Records))
	}
	for _, r := range res.Records {
		if got := Evaluate(r, []string{r.Value()}); !got.Verified {
			t.Errorf("%s %s does not verify against itself: %s", r.Purpose, r.Name, got.Reason)
		}
	}
}
