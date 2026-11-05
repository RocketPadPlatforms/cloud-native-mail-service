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

// Package dnsrecords composes the DNS records of a MailDomain (ADR
// 0012) and of a MailServer hostname (ADR 0014) and verifies them
// through an injectable resolver. It uses no Kubernetes API.
package dnsrecords

import "strings"

// maxTXTString is the longest single character-string of a TXT record
// (RFC 1035 section 3.3).
const maxTXTString = 255

// The record types CNMS composes. Only TXT records are published (as a
// DNSEndpoint); the others are reported and verified (ADR 0014).
const (
	TypeTXT  = "TXT"
	TypeA    = "A"
	TypeAAAA = "AAAA"
	TypeMX   = "MX"
)

// MXPreference is the preference of the MX record of a domain: the
// record points at the one server of the domain, so there is nothing to
// rank.
const MXPreference = 10

// Purpose says why a record exists.
type Purpose string

// The purposes, in the order records are listed.
const (
	PurposeDKIM    Purpose = "dkim"
	PurposeSPF     Purpose = "spf"
	PurposeDMARC   Purpose = "dmarc"
	PurposeMTASTS  Purpose = "mta-sts"
	PurposeTLSRPT  Purpose = "tls-rpt"
	PurposeMX      Purpose = "mx"
	PurposeAddress Purpose = "address"
)

// Record is one required DNS record.
type Record struct {
	// Name is the fully qualified, lower-case name without a trailing dot.
	Name string
	Type string
	// Values holds the TXT character-strings of the record, each at most
	// 255 octets; a resolver joins them (RFC 6376, RFC 7208). An A or
	// AAAA record has the address as its one value, an MX record
	// "<preference> <host>".
	Values  []string
	Purpose Purpose
}

// Value returns the record value as one string: the character-strings
// joined, which is what the status shows and what is compared.
func (r Record) Value() string { return strings.Join(r.Values, "") }

// mxHost returns the host of an MX record ("<preference> <host>").
func (r Record) mxHost() string {
	_, host, _ := strings.Cut(r.Value(), " ")
	return strings.TrimSpace(host)
}

// SplitTXT splits a TXT value into character-strings of at most 255
// octets. The values CNMS publishes are ASCII, so a byte split never cuts
// a character. An empty value is one empty string.
func SplitTXT(value string) []string {
	if len(value) <= maxTXTString {
		return []string{value}
	}
	out := make([]string, 0, len(value)/maxTXTString+1)
	for len(value) > maxTXTString {
		out = append(out, value[:maxTXTString])
		value = value[maxTXTString:]
	}
	return append(out, value)
}
