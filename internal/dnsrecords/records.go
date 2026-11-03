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
// 0012) and verifies them through an injectable resolver. It uses no
// Kubernetes API.
package dnsrecords

import "strings"

// maxTXTString is the longest single character-string of a TXT record
// (RFC 1035 section 3.3).
const maxTXTString = 255

// TypeTXT is the only record type CNMS publishes.
const TypeTXT = "TXT"

// Purpose says why a record exists.
type Purpose string

// The purposes, in the order records are listed.
const (
	PurposeDKIM   Purpose = "dkim"
	PurposeSPF    Purpose = "spf"
	PurposeDMARC  Purpose = "dmarc"
	PurposeMTASTS Purpose = "mta-sts"
	PurposeTLSRPT Purpose = "tls-rpt"
)

// Record is one required DNS record.
type Record struct {
	// Name is the fully qualified, lower-case name without a trailing dot.
	Name string
	Type string
	// Values holds the TXT character-strings of the record, each at most
	// 255 octets; a resolver joins them (RFC 6376, RFC 7208).
	Values  []string
	Purpose Purpose
}

// Value returns the record value as one string: the character-strings
// joined, which is what the status shows and what is compared.
func (r Record) Value() string { return strings.Join(r.Values, "") }

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
