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
	"crypto/rand"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/mtasts"
)

const (
	// The Ed25519 key of RFC 8463 Appendix A.
	edSeed  = "nWGxne/9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A="
	edValue = "v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="

	testDomain   = "example.org"
	ip4Host      = "203.0.113.9"
	ip4Other     = "203.0.113.10"
	ip6Host      = "2001:db8::1"
	spfNet       = "_spf.example.net"
	nameServfail = "servfail.example.org"
	nameSilent   = "silent.example.org"
	mx1          = "mx1.example.org"
	dmarcName    = "_dmarc.example.org"
	policyReject = "reject"
	policyTest   = "version: STSv1\r\nmode: testing\r\nmx: mx1.example.org\r\nmx: mx2.example.org\r\nmax_age: 604800\r\n"
)

func edKey(t *testing.T) *dkim.Key {
	t.Helper()
	k, err := dkim.Parse([]byte(edSeed))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func rsaKey(t *testing.T) *dkim.Key {
	t.Helper()
	pem, err := dkim.Generate(dkim.RSA, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := dkim.Parse(pem)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// summary reduces records to name, value and purpose for comparison.
func summary(rs []Record) [][3]string {
	out := make([][3]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, [3]string{r.Name, r.Value(), string(r.Purpose)})
	}
	return out
}

func TestComposeFull(t *testing.T) {
	rsa := rsaKey(t)
	in := Input{
		Domain: "Example.ORG",
		DKIM: []DKIMKey{
			{"cnms-ed25519-20261103", edKey(t)},
			{"cnms-20261103", rsa},
		},
		SPF: &SPF{
			IP4:       []string{ip4Host, "198.51.100.0/24", ip4Host},
			IP6:       []string{ip6Host},
			Include:   []string{spfNet, "_spf.example.com"},
			Qualifier: "fail",
		},
		DMARC:  &DMARC{Policy: policyReject, SubdomainPolicy: "quarantine", NonExistentSubdomainPolicy: policyReject, Testing: true, Rua: "agg@example.org", Ruf: "for@example.org"},
		MTASTS: &MTASTS{MX: []string{"mx2.example.org", "MX1.example.org", mx1}},
		TLSRPT: &TLSRPT{},
	}
	res, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	id := mtasts.PolicyID(policyTest)
	want := [][3]string{
		{"cnms-20261103._domainkey.example.org", rsa.TXTValue(), string(PurposeDKIM)},
		{"cnms-ed25519-20261103._domainkey.example.org", edValue, string(PurposeDKIM)},
		{testDomain, "v=spf1 ip4:198.51.100.0/24 ip4:203.0.113.9 ip6:2001:db8::1 include:_spf.example.com include:_spf.example.net -all", string(PurposeSPF)},
		{dmarcName, "v=DMARC1; p=reject; sp=quarantine; np=reject; t=y; rua=mailto:agg@example.org; ruf=mailto:for@example.org", string(PurposeDMARC)},
		{"_mta-sts.example.org", "v=STSv1; id=" + id, string(PurposeMTASTS)},
		{"_smtp._tls.example.org", "v=TLSRPTv1; rua=mailto:agg@example.org", string(PurposeTLSRPT)},
	}
	if got := summary(res.Records); !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
	if res.MTASTS == nil || res.MTASTS.Policy != policyTest || res.MTASTS.ID != id {
		t.Fatalf("policy: %+v", res.MTASTS)
	}
	if len(res.Warnings) != 0 || res.NoSPFSources || res.NoMTASTSHosts {
		t.Fatalf("unexpected flags: %+v", res)
	}

	// The RSA-2048 value does not fit one string.
	if r := res.Records[0]; len(r.Values) != 2 || len(r.Values[0]) != 255 || r.Type != TypeTXT {
		t.Fatalf("DKIM record not split: %d strings", len(r.Values))
	}

	t.Run("deterministic for any input order", func(t *testing.T) {
		in.DKIM = []DKIMKey{in.DKIM[1], in.DKIM[0]}
		in.SPF.IP4 = []string{"198.51.100.0/24", ip4Host}
		again, err := Compose(in)
		if err != nil || !reflect.DeepEqual(again, res) {
			t.Fatalf("output changed with the order of the input (err %v)", err)
		}
	})
}

func TestComposeSections(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Input
		want [][3]string
		flag func(Composed) bool
	}{
		{name: "nothing configured", in: Input{Domain: testDomain}},
		{
			name: "dmarc defaults to p=none",
			in:   Input{Domain: testDomain, DMARC: &DMARC{}},
			want: [][3]string{{dmarcName, "v=DMARC1; p=none", string(PurposeDMARC)}},
		},
		{
			name: "dmarc testing flag only",
			in:   Input{Domain: testDomain, DMARC: &DMARC{Policy: "quarantine", Testing: true}},
			want: [][3]string{{dmarcName, "v=DMARC1; p=quarantine; t=y", string(PurposeDMARC)}},
		},
		{
			name: "spf without sources is not published",
			in:   Input{Domain: testDomain, SPF: &SPF{}},
			flag: func(r Composed) bool { return r.NoSPFSources },
		},
		{
			name: "spf softfail by default",
			in:   Input{Domain: testDomain, SPF: &SPF{IP4: []string{"203.0.113.1"}}},
			want: [][3]string{{testDomain, "v=spf1 ip4:203.0.113.1 ~all", string(PurposeSPF)}},
		},
		{
			name: "spf neutral",
			in:   Input{Domain: testDomain, SPF: &SPF{Include: []string{spfNet}, Qualifier: "neutral"}},
			want: [][3]string{{testDomain, "v=spf1 include:_spf.example.net ?all", string(PurposeSPF)}},
		},
		{
			name: "mta-sts without hosts has no record",
			in:   Input{Domain: testDomain, MTASTS: &MTASTS{}},
			flag: func(r Composed) bool { return r.NoMTASTSHosts && r.MTASTS == nil },
		},
		{
			name: "tls-rpt without any address has no record",
			in:   Input{Domain: testDomain, TLSRPT: &TLSRPT{}},
		},
		{
			name: "tls-rpt own address wins over the dmarc one",
			in:   Input{Domain: testDomain, DMARC: &DMARC{Rua: "d@example.org"}, TLSRPT: &TLSRPT{Rua: "t@example.org"}},
			want: [][3]string{
				{dmarcName, "v=DMARC1; p=none; rua=mailto:d@example.org", string(PurposeDMARC)},
				{"_smtp._tls.example.org", "v=TLSRPTv1; rua=mailto:t@example.org", string(PurposeTLSRPT)},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compose(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := summary(res.Records); len(got)+len(tc.want) > 0 && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got\n%v\nwant\n%v", got, tc.want)
			}
			if tc.flag != nil && !tc.flag(res) {
				t.Fatalf("flag not set: %+v", res)
			}
		})
	}
}

func TestDMARCNeverEmitsHistoricTags(t *testing.T) {
	res, err := Compose(Input{Domain: testDomain, DMARC: &DMARC{Policy: policyReject, SubdomainPolicy: "none", NonExistentSubdomainPolicy: "none", Testing: true, Rua: "a@example.org", Ruf: "b@example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	v := res.Records[0].Value()
	for _, tag := range []string{"pct=", "rf=", "ri=", "psd=", "adkim=", "aspf=", "fo="} {
		if strings.Contains(v, tag) {
			t.Fatalf("%q must not be published: %s", tag, v)
		}
	}
	if !strings.HasPrefix(v, "v=DMARC1; ") || strings.HasSuffix(v, ";") {
		t.Fatalf("bad framing: %q", v)
	}
}

func TestMTASTSPolicy(t *testing.T) {
	text, err := MTASTSPolicyText("", []string{"mx2.example.org", "mx1.example.org"})
	if err != nil || text != policyTest {
		t.Fatalf("got %q, %v", text, err)
	}
	enforce, _ := MTASTSPolicyText("enforce", []string{"mx1.example.org"})
	if !strings.Contains(enforce, "mode: enforce\r\n") || enforce == text {
		t.Fatalf("mode not rendered: %q", enforce)
	}
	if mtasts.PolicyID(text) == mtasts.PolicyID(enforce) {
		t.Fatal("the id must change with the policy")
	}
	if got, err := MTASTSPolicyText("", nil); got != "" || err != nil {
		t.Fatalf("no hosts: got %q, %v", got, err)
	}
}

func TestReportAuthorizationWarnings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		domain string
		rua    string
		want   []Warning
	}{
		{"same domain", testDomain, "r@example.org", nil},
		{"subdomain of the organizational domain", testDomain, "r@reports.example.org", nil},
		{"policy domain is a subdomain", testHostname, "r@example.org", nil},
		{"another organization", testDomain, "r@dmarc.example.net",
			[]Warning{{"rua", "r@dmarc.example.net", "example.org._report._dmarc.dmarc.example.net"}}},
		{"multi-label public suffix, same organization", "example.co.uk", "r@mail.example.co.uk", nil},
		{"multi-label public suffix, other organization", "example.co.uk", "r@other.co.uk",
			[]Warning{{"rua", "r@other.co.uk", "example.co.uk._report._dmarc.other.co.uk"}}},
		{"host is case-insensitive", testDomain, "r@EXAMPLE.org", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compose(Input{Domain: tc.domain, DMARC: &DMARC{Rua: tc.rua}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(res.Warnings, tc.want) {
				t.Fatalf("got %+v, want %+v", res.Warnings, tc.want)
			}
		})
	}

	t.Run("ruf is checked too", func(t *testing.T) {
		res, _ := Compose(Input{Domain: testDomain, DMARC: &DMARC{Ruf: "f@example.net"}})
		if len(res.Warnings) != 1 || res.Warnings[0].Tag != "ruf" {
			t.Fatalf("got %+v", res.Warnings)
		}
	})
}

func TestComposeErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Input
		want string
	}{
		{"empty domain", Input{}, "domain is empty"},
		{"key missing", Input{Domain: testDomain, DKIM: []DKIMKey{{Selector: "s"}}}, "no key"},
		{"spf qualifier", Input{Domain: testDomain, SPF: &SPF{IP4: []string{"203.0.113.1"}, Qualifier: "pass"}}, "SPF qualifier"},
		{"dmarc policy", Input{Domain: testDomain, DMARC: &DMARC{Policy: "block"}}, "DMARC policy"},
		{"dmarc sp", Input{Domain: testDomain, DMARC: &DMARC{SubdomainPolicy: "block"}}, "for sp"},
		{"mta-sts mode", Input{Domain: testDomain, MTASTS: &MTASTS{Mode: "strict", MX: []string{"mx.example.org"}}}, "MTA-STS mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compose(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestDKIMValueRoundTripsThroughTXTSplitting(t *testing.T) {
	k := rsaKey(t)
	res, err := Compose(Input{Domain: testDomain, DKIM: []DKIMKey{{"s", k}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Records[0].Value() != k.TXTValue() {
		t.Fatal("joined strings differ from the key value")
	}
	if _, err := base64.StdEncoding.DecodeString(k.PublicKey()); err != nil {
		t.Fatal(err)
	}
}
