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

const testHostname = "mail.example.org"

// typed reduces records to name, type and value for comparison.
func typed(rs []Record) [][3]string {
	if len(rs) == 0 {
		return nil
	}
	out := make([][3]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, [3]string{r.Name, r.Type, r.Value()})
	}
	return out
}

func TestComposeServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ServerInput
		want [][3]string
	}{
		{name: "nothing known", in: ServerInput{Hostname: testHostname}},
		{
			name: "addresses by family, sorted and without duplicates",
			in: ServerInput{
				Hostname:  "Mail.Example.ORG",
				Addresses: []string{ip4Other, ip6Host, ip4Host, ip4Other, "::ffff:198.51.100.7"},
			},
			want: [][3]string{
				{testHostname, "A", "198.51.100.7"},
				{testHostname, "A", ip4Other},
				{testHostname, "A", ip4Host},
				{testHostname, "AAAA", ip6Host},
			},
		},
		{
			name: "SPF of the hostname ends in -all and lists the server sources only",
			in: ServerInput{
				Hostname: testHostname,
				SPF:      &SPF{IP4: []string{ip4Host}, IP6: []string{ip6Host}, Include: []string{spfNet}, Qualifier: "neutral"},
			},
			want: [][3]string{{testHostname, TypeTXT, "v=spf1 ip4:203.0.113.9 ip6:2001:db8::1 -all"}},
		},
		{
			name: "no sources, no SPF record",
			in:   ServerInput{Hostname: testHostname, SPF: &SPF{}},
		},
		{
			name: "SPF skipped when a domain of the same name carries it",
			in: ServerInput{
				Hostname: testHostname, Addresses: []string{ip4Host}, SkipSPF: true,
				SPF: &SPF{IP4: []string{ip4Host}},
			},
			want: [][3]string{{testHostname, "A", ip4Host}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComposeServer(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(typed(got), tc.want) {
				t.Fatalf("got\n%v\nwant\n%v", typed(got), tc.want)
			}
		})
	}

	t.Run("rejects an empty hostname and a bad address", func(t *testing.T) {
		if _, err := ComposeServer(ServerInput{}); err == nil {
			t.Error("empty hostname accepted")
		}
		if _, err := ComposeServer(ServerInput{Hostname: testHostname, Addresses: []string{"lb.example.net"}}); err == nil {
			t.Error("a name accepted as an address")
		}
	})
}

func TestComposeMX(t *testing.T) {
	res, err := Compose(Input{Domain: testDomain, MXHost: "Mail.Example.ORG", SPF: &SPF{IP4: []string{ip4Host}}})
	if err != nil {
		t.Fatal(err)
	}
	want := [][3]string{
		{testDomain, TypeTXT, "v=spf1 ip4:203.0.113.9 ~all"},
		{testDomain, TypeMX, "10 mail.example.org"},
	}
	if got := typed(res.Records); !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
	if res.Records[1].Purpose != PurposeMX {
		t.Fatalf("purpose %q", res.Records[1].Purpose)
	}
	none, err := Compose(Input{Domain: testDomain})
	if err != nil || len(none.Records) != 0 {
		t.Fatalf("no host, no MX record: %v, %v", none.Records, err)
	}
}

func TestVerifyAddressAndMX(t *testing.T) {
	ctx := context.Background()
	a := Record{Name: testHostname, Type: TypeA, Purpose: PurposeAddress, Values: []string{ip4Host}}
	aaaa := Record{Name: testHostname, Type: TypeAAAA, Purpose: PurposeAddress, Values: []string{ip6Host}}
	mx := Record{Name: testDomain, Type: TypeMX, Purpose: PurposeMX, Values: []string{"10 mail.example.org"}}

	for _, tc := range []struct {
		name     string
		rec      Record
		set      func(*FakeResolver)
		verified bool
		unknown  bool
		reason   string
	}{
		{"A found among others", a, func(f *FakeResolver) { f.SetHost(testHostname, ip4Other, ip4Host, ip6Host) }, true, false, ""},
		{"AAAA found, written differently", aaaa, func(f *FakeResolver) { f.SetHost(testHostname, "2001:0db8:0:0:0:0:0:1") }, true, false, ""},
		{"A found as an IPv4-mapped address", a, func(f *FakeResolver) { f.SetHost(testHostname, "::ffff:203.0.113.9") }, true, false, ""},
		{"A points elsewhere", a, func(f *FakeResolver) { f.SetHost(testHostname, ip4Other) }, false, false, "resolves to 203.0.113.10, not to 203.0.113.9"},
		{"A with only an AAAA", a, func(f *FakeResolver) { f.SetHost(testHostname, ip6Host) }, false, false, "no A record found"},
		{"AAAA with only an A", aaaa, func(f *FakeResolver) { f.SetHost(testHostname, ip4Host) }, false, false, "no AAAA record found"},
		{"A not found", a, func(*FakeResolver) {}, false, false, "no A record at " + testHostname},
		{"A temporary failure", a, func(f *FakeResolver) { f.SetError(testHostname, errors.New("SERVFAIL")) }, false, true, "SERVFAIL"},
		{"MX found next to a backup", mx, func(f *FakeResolver) { f.SetMX(testDomain, MX{"backup.example.net", 20}, MX{"Mail.Example.ORG.", 30}) }, true, false, ""},
		{"MX points elsewhere", mx, func(f *FakeResolver) { f.SetMX(testDomain, MX{"mx.example.net", 10}) }, false, false, "point at mx.example.net, not at mail.example.org"},
		{"MX not found", mx, func(*FakeResolver) {}, false, false, "no MX record at " + testDomain},
		{"MX temporary failure", mx, func(f *FakeResolver) { f.SetError(testDomain, errors.New("timeout")) }, false, true, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFakeResolver()
			tc.set(f)
			res := Verify(ctx, f, tc.rec)
			if res.Verified != tc.verified || res.Unknown != tc.unknown {
				t.Fatalf("got %+v, want verified %v unknown %v", res, tc.verified, tc.unknown)
			}
			if tc.verified && res.Reason != "" {
				t.Fatalf("a verified record has the reason %q", res.Reason)
			}
			if !tc.verified && !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", res.Reason, tc.reason)
			}
		})
	}

	t.Run("an address record that is no address", func(t *testing.T) {
		bad := Record{Name: testHostname, Type: TypeA, Values: []string{"lb"}}
		if res := EvaluateAddress(bad, []string{ip4Host}); res.Verified || !strings.Contains(res.Reason, "not an IP address") {
			t.Fatalf("got %+v", res)
		}
	})
}

func TestComposedServerRecordsVerifyAgainstThemselves(t *testing.T) {
	recs, err := ComposeServer(ServerInput{
		Hostname: testHostname, Addresses: []string{ip4Host, ip6Host}, SPF: &SPF{IP4: []string{ip4Host}},
	})
	if err != nil || len(recs) != 3 {
		t.Fatalf("got %d records, %v", len(recs), err)
	}
	f := NewFakeResolver()
	f.SetHost(testHostname, ip4Host, ip6Host)
	f.Set(testHostname, recs[2].Value())
	for _, r := range recs {
		if res := Verify(context.Background(), f, r); !res.Verified {
			t.Errorf("%s %s does not verify: %s", r.Type, r.Name, res.Reason)
		}
	}
}
