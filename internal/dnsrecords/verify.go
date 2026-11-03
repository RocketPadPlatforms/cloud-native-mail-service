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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode"
)

// Result is the outcome of verifying one record.
type Result struct {
	// Verified is true when a record at the name matches.
	Verified bool
	// Unknown is true when the lookup failed temporarily (timeout,
	// SERVFAIL): the caller keeps the previous state and retries sooner,
	// and Verified is meaningless (ADR 0012).
	Unknown bool
	// Reason says why the record is not verified; empty when it is.
	Reason string
	// Observed lists the TXT records found at the name, each with its
	// strings joined.
	Observed []string
}

// Verify looks the record up and judges the answer (ADR 0012,
// section 4): the TXT records at the name are compared, other records
// there (site verification tokens) are ignored.
func Verify(ctx context.Context, r Resolver, rec Record) Result {
	txts, err := r.LookupTXT(ctx, rec.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		return Result{Reason: "no TXT record at " + rec.Name}
	case err != nil:
		return Result{Unknown: true, Reason: "lookup failed: " + err.Error()}
	}
	return Evaluate(rec, txts)
}

// Evaluate judges the TXT records observed at the name of rec. It is the
// pure part of Verify.
func Evaluate(rec Record, observed []string) Result {
	res := Result{Observed: observed}
	var reason string
	switch rec.Purpose {
	case PurposeDKIM:
		reason = checkDKIM(rec.Value(), observed)
	case PurposeSPF:
		reason = checkSPF(rec.Value(), observed)
	case PurposeDMARC:
		reason = checkTagSet(rec.Value(), "DMARC1", observed)
	case PurposeMTASTS:
		reason = checkTagSet(rec.Value(), "STSv1", observed)
	case PurposeTLSRPT:
		reason = checkTagSet(rec.Value(), "TLSRPTv1", observed)
	default:
		reason = fmt.Sprintf("unsupported record purpose %q", rec.Purpose)
	}
	res.Verified = reason == ""
	res.Reason = reason
	return res
}

// tags is a parsed tag-value list: tag names lower-cased, values trimmed.
type tags map[string]string

// parseTags parses "a=b; c=d;" (RFC 6376 section 3.2). A duplicate or
// empty tag name, or a segment without "=", makes the record invalid.
func parseTags(s string) (tags, bool) {
	out := tags{}
	for seg := range strings.SplitSeq(s, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		name, value, ok := strings.Cut(seg, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if _, dup := out[name]; !ok || name == "" || dup {
			return nil, false
		}
		out[name] = strings.TrimSpace(value)
	}
	return out, true
}

// startsWithVersion reports whether the first tag of s is v=<version>.
func startsWithVersion(s, version string) bool {
	first, _, _ := strings.Cut(s, ";")
	name, value, ok := strings.Cut(first, "=")
	return ok && strings.EqualFold(strings.TrimSpace(name), "v") && strings.TrimSpace(value) == version
}

// normalizeList removes the whitespace around the commas of a URI list.
func normalizeList(v string) string {
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

// checkTagSet verifies DMARC, TLS-RPT and MTA-STS: exactly one record
// starts with the version tag, and its tags equal the expected ones.
func checkTagSet(expected, version string, observed []string) string {
	var candidates []string
	for _, o := range observed {
		if startsWithVersion(o, version) {
			candidates = append(candidates, o)
		}
	}
	switch len(candidates) {
	case 0:
		return fmt.Sprintf("no TXT record starts with v=%s", version)
	case 1:
	default:
		return fmt.Sprintf("%d TXT records start with v=%s; exactly one is allowed", len(candidates), version)
	}
	want, _ := parseTags(expected)
	got, ok := parseTags(candidates[0])
	if !ok {
		return "the record is not a valid tag-value list"
	}
	for _, m := range []tags{want, got} {
		for k, v := range m {
			m[k] = normalizeList(v)
		}
	}
	if !maps.Equal(got, want) {
		return "the record differs from the expected one"
	}
	return ""
}

// isSPF reports whether s is an SPF record (RFC 7208 section 4.5: the
// version term, case-insensitive, then the end or a space).
func isSPF(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) >= 6 && strings.EqualFold(s[:6], "v=spf1") && (len(s) == 6 || s[6] == ' ')
}

func normalizeSPF(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// checkSPF verifies the SPF record: exactly one v=spf1 record exists
// (several are a permerror for receivers) and its normalized text equals
// the expected one.
func checkSPF(expected string, observed []string) string {
	var candidates []string
	for _, o := range observed {
		if isSPF(o) {
			candidates = append(candidates, o)
		}
	}
	switch len(candidates) {
	case 0:
		return "no SPF record found"
	case 1:
	default:
		return fmt.Sprintf("%d SPF records found; receivers treat several as a permanent error", len(candidates))
	}
	if normalizeSPF(candidates[0]) != normalizeSPF(expected) {
		return "the SPF record differs from the expected one: " + candidates[0]
	}
	return ""
}

// checkDKIM verifies a DKIM record: exactly one record at the name
// parses as a DKIM key record (a tag list with p and v=DKIM1 or no v;
// several give an undefined result, RFC 6376 section 3.6.2.2), and it has
// the expected key type and the same decoded public key; extra tags and
// base64 re-wrapping are allowed.
func checkDKIM(expected string, observed []string) string {
	var candidates []tags
	for _, o := range observed {
		got, ok := parseTags(o)
		if !ok {
			continue
		}
		if _, has := got["p"]; !has { // p is required; this is some other record
			continue
		}
		if v, has := got["v"]; has && v != "DKIM1" {
			continue
		}
		candidates = append(candidates, got)
	}
	switch len(candidates) {
	case 0:
		return "no DKIM record found"
	case 1:
	default:
		return fmt.Sprintf("%d DKIM records found; exactly one is allowed", len(candidates))
	}
	want, _ := parseTags(expected)
	got := candidates[0]
	if got["k"] == "" {
		got["k"] = "rsa" // the default key type
	}
	if !strings.EqualFold(got["k"], want["k"]) {
		return fmt.Sprintf("the key type differs: found k=%s, want k=%s", got["k"], want["k"])
	}
	wantKey, _ := decodeKey(want["p"])
	key, err := decodeKey(got["p"])
	if err != nil || !bytes.Equal(key, wantKey) {
		return "the public key differs from the key of the Secret"
	}
	return ""
}

// decodeKey decodes the p= value: whitespace anywhere is folding white
// space and padding is optional.
func decodeKey(p string) ([]byte, error) {
	p = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, p)
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(p, "="))
}
