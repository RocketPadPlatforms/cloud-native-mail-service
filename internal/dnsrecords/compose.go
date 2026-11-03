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
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/mtasts"
)

// MTASTSMaxAge is the max_age of the reported policy: one week.
const MTASTSMaxAge = 604800

var purposeRank = map[Purpose]int{PurposeDKIM: 0, PurposeSPF: 1, PurposeDMARC: 2, PurposeMTASTS: 3, PurposeTLSRPT: 4}

// Input is what the records are composed from. A nil section means the
// feature is off. The types are plain on purpose: the controller maps
// the API types and this package stays independent of them.
type Input struct {
	// Domain is the domain name of the MailDomain.
	Domain string
	// DKIM lists the published keys: active, pending and retiring.
	DKIM   []DKIMKey
	SPF    *SPF
	DMARC  *DMARC
	MTASTS *MTASTS
	TLSRPT *TLSRPT
}

// DKIMKey is a published DKIM key.
type DKIMKey struct {
	Selector string
	Key      *dkim.Key
}

// SPF lists the sending sources (ADR 0012, section 3). Further sources
// (the addresses of an IP pool claim, a relay include) are appended by
// the caller to these lists once they exist.
type SPF struct {
	IP4, IP6, Include []string
	// Qualifier is "softfail" (default), "fail" or "neutral".
	Qualifier string
}

// DMARC is the policy to publish. Policies are none, quarantine or
// reject; an empty Policy means none.
type DMARC struct {
	Policy, SubdomainPolicy, NonExistentSubdomainPolicy string
	// Rua and Ruf are plain addresses; "mailto:" is added.
	Rua, Ruf string
	// Testing publishes t=y (RFC 9989).
	Testing bool
}

// MTASTS is the reported policy. MX lists the hostnames of the mail
// servers of the domain; without any there is no policy.
type MTASTS struct {
	// Mode is "testing" (default), "enforce" or "none".
	Mode string
	MX   []string
}

// TLSRPT enables TLS-RPT. Rua defaults to the DMARC rua.
type TLSRPT struct {
	Rua string
}

// Warning is a ReportAuthorization warning: a report address lies in
// another organizational domain, so its owner has to publish Record
// before receivers send reports there (RFC 9990 section 4).
type Warning struct {
	// Tag is "rua" or "ruf".
	Tag     string
	Address string
	// Record is the name of the TXT record the receiver publishes.
	Record string
}

// MTASTSPolicy is the report-only policy content and its id.
type MTASTSPolicy struct {
	Policy string
	ID     string
}

// Composed is the composed set of records.
type Composed struct {
	// Records are sorted: DKIM (by name), SPF, DMARC, MTA-STS, TLS-RPT.
	Records []Record
	// MTASTS is the policy behind the _mta-sts record; nil when none is
	// required.
	MTASTS *MTASTSPolicy
	// NoSPFSources is true when no SPF record can be generated.
	NoSPFSources bool
	// NoMTASTSHosts is true when MTA-STS is enabled but no mail server
	// serves the domain, so there is no policy to publish.
	NoMTASTSHosts bool
	Warnings      []Warning
}

// Compose returns the records of ADR 0012 for the input. The output is
// deterministic. It fails on a value outside the enums above, which the
// API validation already rejects.
func Compose(in Input) (Composed, error) {
	domain := strings.ToLower(in.Domain)
	if domain == "" {
		return Composed{}, errors.New("domain is empty")
	}
	var res Composed
	for _, k := range in.DKIM {
		if k.Key == nil {
			return Composed{}, fmt.Errorf("DKIM selector %q has no key", k.Selector)
		}
		res.Records = append(res.Records, txt(k.Selector+"._domainkey."+domain, PurposeDKIM, k.Key.TXTValue()))
	}
	if in.SPF != nil {
		v, ok, err := spfValue(*in.SPF)
		if err != nil {
			return Composed{}, err
		}
		res.NoSPFSources = !ok
		if ok {
			res.Records = append(res.Records, txt(domain, PurposeSPF, v))
		}
	}
	if in.DMARC != nil {
		v, err := dmarcValue(*in.DMARC)
		if err != nil {
			return Composed{}, err
		}
		res.Records = append(res.Records, txt("_dmarc."+domain, PurposeDMARC, v))
		for _, a := range []struct{ tag, addr string }{{"rua", in.DMARC.Rua}, {"ruf", in.DMARC.Ruf}} {
			if w, ok := reportWarning(domain, a.tag, a.addr); ok {
				res.Warnings = append(res.Warnings, w)
			}
		}
	}
	if in.MTASTS != nil {
		policy, err := MTASTSPolicyText(in.MTASTS.Mode, in.MTASTS.MX)
		if err != nil {
			return Composed{}, err
		}
		if policy == "" {
			res.NoMTASTSHosts = true
		} else {
			id := mtasts.PolicyID(policy)
			res.MTASTS = &MTASTSPolicy{Policy: policy, ID: id}
			res.Records = append(res.Records, txt("_mta-sts."+domain, PurposeMTASTS, "v=STSv1; id="+id))
		}
	}
	if in.TLSRPT != nil {
		rua := in.TLSRPT.Rua
		if rua == "" && in.DMARC != nil {
			rua = in.DMARC.Rua
		}
		if rua != "" {
			res.Records = append(res.Records, txt("_smtp._tls."+domain, PurposeTLSRPT, "v=TLSRPTv1; rua=mailto:"+rua))
		}
	}
	sortRecords(res.Records)
	return res, nil
}

// sortRecords orders records by purpose, then name.
func sortRecords(rs []Record) {
	slices.SortStableFunc(rs, func(a, b Record) int {
		return cmp.Or(cmp.Compare(purposeRank[a.Purpose], purposeRank[b.Purpose]), strings.Compare(a.Name, b.Name))
	})
}

func txt(name string, purpose Purpose, value string) Record {
	return Record{Name: name, Type: TypeTXT, Values: SplitTXT(value), Purpose: purpose}
}

// spfValue builds the SPF text: ip4, ip6, include, each sorted and
// without duplicates, then the qualifier and "all". ok is false when
// there is no source: no record is published then.
func spfValue(s SPF) (value string, ok bool, err error) {
	all := ""
	switch s.Qualifier {
	case "", "softfail":
		all = "~all"
	case "fail":
		all = "-all"
	case "neutral":
		all = "?all"
	default:
		return "", false, fmt.Errorf("unknown SPF qualifier %q", s.Qualifier)
	}
	parts := []string{"v=spf1"}
	for _, m := range []struct {
		prefix string
		list   []string
	}{{"ip4:", s.IP4}, {"ip6:", s.IP6}, {"include:", s.Include}} {
		for _, v := range slices.Compact(slices.Sorted(slices.Values(m.list))) {
			parts = append(parts, m.prefix+v)
		}
	}
	if len(parts) == 1 {
		return "", false, nil
	}
	return strings.Join(append(parts, all), " "), true, nil
}

// dmarcValue builds the DMARC record: v, p, sp, np, t, rua, ruf in that
// order, separated by "; ", no trailing ";" (ADR 0012). pct, rf, ri and
// psd are never emitted.
func dmarcValue(d DMARC) (string, error) {
	tags := []string{"v=DMARC1"}
	for _, p := range []struct{ tag, value string }{
		{"p", cmp.Or(d.Policy, "none")}, {"sp", d.SubdomainPolicy}, {"np", d.NonExistentSubdomainPolicy},
	} {
		if p.value == "" {
			continue
		}
		if !slices.Contains([]string{"none", "quarantine", "reject"}, p.value) {
			return "", fmt.Errorf("unknown DMARC policy %q for %s", p.value, p.tag)
		}
		tags = append(tags, p.tag+"="+p.value)
	}
	if d.Testing {
		tags = append(tags, "t=y")
	}
	if d.Rua != "" {
		tags = append(tags, "rua=mailto:"+d.Rua)
	}
	if d.Ruf != "" {
		tags = append(tags, "ruf=mailto:"+d.Ruf)
	}
	return strings.Join(tags, "; "), nil
}

// MTASTSPolicyText returns the policy text of RFC 8461 section 3.2 for
// the hosts: CRLF-terminated lines, hosts sorted without duplicates.
// It returns "" when there is no host. The mode defaults to testing.
func MTASTSPolicyText(mode string, mx []string) (string, error) {
	mode = cmp.Or(mode, "testing")
	if !slices.Contains([]string{"testing", "enforce", "none"}, mode) {
		return "", fmt.Errorf("unknown MTA-STS mode %q", mode)
	}
	hosts := make([]string, len(mx))
	for i, h := range mx {
		hosts[i] = strings.ToLower(h)
	}
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	if len(hosts) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("version: STSv1\r\nmode: " + mode + "\r\n")
	for _, h := range hosts {
		b.WriteString("mx: " + h + "\r\n")
	}
	fmt.Fprintf(&b, "max_age: %d\r\n", MTASTSMaxAge)
	return b.String(), nil
}

// reportWarning returns a warning when the address of a DMARC report tag
// is in another organizational domain than the policy domain.
func reportWarning(domain, tag, address string) (Warning, bool) {
	if address == "" {
		return Warning{}, false
	}
	host := strings.ToLower(address[strings.LastIndex(address, "@")+1:])
	if organizational(host) == organizational(domain) {
		return Warning{}, false
	}
	return Warning{Tag: tag, Address: address, Record: domain + "._report._dmarc." + host}, true
}

// organizational returns the organizational domain (public suffix plus
// one label); a name without one (a public suffix itself) is returned
// as it is.
func organizational(name string) string {
	if o, err := publicsuffix.EffectiveTLDPlusOne(name); err == nil {
		return o
	}
	return name
}
