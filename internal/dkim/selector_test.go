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
	"strings"
	"testing"
	"time"
)

const selectorRSA = "cnms-20261103"

func TestSelector(t *testing.T) {
	plus2 := time.FixedZone("CEST", 2*3600)
	for _, tc := range []struct {
		name    string
		prefix  string
		alg     Algorithm
		created time.Time
		want    string
	}{
		{"rsa", DefaultPrefix, RSA, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), selectorRSA},
		{"ed25519", DefaultPrefix, Ed25519, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), "cnms-ed25519-20261103"},
		{"default prefix", "", RSA, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "cnms-20260102"},
		{"custom prefix", "mail-x", RSA, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "mail-x-20260102"},
		{"date is UTC, not local", DefaultPrefix, RSA, time.Date(2026, 11, 4, 1, 30, 0, 0, plus2), selectorRSA},
		{"just before UTC midnight", DefaultPrefix, RSA, time.Date(2026, 11, 3, 23, 59, 59, 0, time.UTC), selectorRSA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Selector(tc.prefix, tc.alg, tc.created); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectorLongestFitsDNSLabel(t *testing.T) {
	// ADR 0011: at most 30 + 9 + 8 characters, within the 63 of a label.
	got := Selector(strings.Repeat("a", 30), Ed25519, time.Now())
	if len(got) != 30+len("-ed25519-")+8 || len(got) > 63 {
		t.Fatalf("selector %q has length %d", got, len(got))
	}
}

func TestSecretName(t *testing.T) {
	if got := SecretName("example", selectorRSA); got != "example-dkim-cnms-20261103" {
		t.Fatalf("got %q", got)
	}
}
