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
	"strings"
)

// DKIMDir is where the signing keys of a MailServer are mounted in the
// Rspamd pods (ADR 0011, section 5).
const DKIMDir = "/etc/rspamd/dkim"

// RspamdLocalLua is the key of the guard script in the Rspamd
// configuration (ADR 0013). It is mounted with a subPath at
// /etc/rspamd/rspamd.local.lua, which Rspamd loads after its own rules.
const RspamdLocalLua = "rspamd.local.lua"

// DKIMKey is one active signing key of a domain, by reference. It never
// holds key bytes: the configuration names the key file, and the pod
// mounts it from the Secret (ADR 0011).
type DKIMKey struct {
	// Selector is the DKIM selector the key signs with.
	Selector string

	// SecretName and SecretKey locate the private key.
	SecretName string
	SecretKey  string

	// Available is false when the key could not be read (the Secret or
	// its data key is missing, or the content is not a usable key). The
	// selector is then still required of every signed message, so the
	// domain fails closed (ADR 0013), but no file is mounted for it.
	Available bool
}

// DomainKeys are the active signing keys of one domain.
type DomainKeys struct {
	Domain string
	Keys   []DKIMKey
}

// DKIMKeyPath is the path of the key file of a domain and selector,
// relative to DKIMDir. Domains share selector names (same prefix, same
// day), so the domain is part of the path.
func DKIMKeyPath(domain, selector string) string {
	return domain + "/" + selector + ".key"
}

// ActiveKeys returns the active keys of an authorized domain, sorted by
// selector. Keys of a domain that is not authorized are ignored, so a
// revoked domain cannot reach the configuration through stale input.
func (r *RenderRequest) ActiveKeys(domain string) []DKIMKey {
	if !slices.Contains(r.AuthorizedDomains, domain) {
		return nil
	}
	var keys []DKIMKey
	for _, dk := range r.DKIMKeys {
		if dk.Domain == domain {
			keys = append(keys, dk.Keys...)
		}
	}
	slices.SortFunc(keys, func(a, b DKIMKey) int { return strings.Compare(a.Selector, b.Selector) })
	return keys
}
