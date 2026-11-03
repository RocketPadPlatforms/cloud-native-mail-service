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
	"reflect"
	"strings"
	"testing"
)

// spfNone is a short SPF value used across the tests.
const spfNone = "v=spf1 -all"

func TestSplitTXT(t *testing.T) {
	a := strings.Repeat("a", 255)
	b := strings.Repeat("b", 255)
	for _, tc := range []struct {
		name  string
		value string
		want  []string
	}{
		{"empty", "", []string{""}},
		{"short", spfNone, []string{spfNone}},
		{"exactly 255", a, []string{a}},
		{"256", a + "c", []string{a, "c"}},
		{"two full strings", a + b, []string{a, b}},
		{"two full and a rest", a + b + "xyz", []string{a, b, "xyz"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitTXT(tc.value)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %d strings, want %d", len(got), len(tc.want))
			}
			if strings.Join(got, "") != tc.value {
				t.Fatal("the strings do not join to the value")
			}
		})
	}
}

func TestRecordValueJoinsStrings(t *testing.T) {
	r := Record{Values: []string{"v=DKIM1; k=rsa; ", "p=abc"}}
	if got := r.Value(); got != "v=DKIM1; k=rsa; p=abc" {
		t.Fatalf("got %q", got)
	}
}
