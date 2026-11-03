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

package render

import (
	"slices"
	"testing"
)

func TestActiveKeys(t *testing.T) {
	req := &RenderRequest{
		AuthorizedDomains: []string{"a.example", "b.example"},
		DKIMKeys: []DomainKeys{
			{Domain: "a.example", Keys: []DKIMKey{{Selector: "s2"}, {Selector: "s1"}}},
			{Domain: "revoked.example", Keys: []DKIMKey{{Selector: "x"}}},
		},
	}
	selectors := func(domain string) []string {
		var out []string
		for _, k := range req.ActiveKeys(domain) {
			out = append(out, k.Selector)
		}
		return out
	}
	if got := selectors("a.example"); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Errorf("a.example keys = %v, want sorted [s1 s2]", got)
	}
	if got := selectors("b.example"); len(got) != 0 {
		t.Errorf("b.example keys = %v, want none", got)
	}
	if got := selectors("revoked.example"); len(got) != 0 {
		t.Errorf("a domain that is not authorized has keys %v", got)
	}
}

func TestDKIMKeyPath(t *testing.T) {
	if got, want := DKIMKeyPath("a.example", "cnms-20261103"), "a.example/cnms-20261103.key"; got != want {
		t.Errorf("DKIMKeyPath() = %q, want %q", got, want)
	}
}
