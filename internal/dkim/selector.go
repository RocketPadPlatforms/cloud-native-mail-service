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

import "time"

// DefaultPrefix is the selector prefix when the spec sets none.
const DefaultPrefix = "cnms"

// Selector returns the selector name of a generated key (ADR 0011):
// <prefix>-<yyyymmdd> for RSA and <prefix>-ed25519-<yyyymmdd> for
// Ed25519, with the UTC date the key is created. An empty prefix means
// DefaultPrefix.
func Selector(prefix string, alg Algorithm, created time.Time) string {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if alg == Ed25519 {
		prefix += "-ed25519"
	}
	return prefix + "-" + created.UTC().Format("20060102")
}

// SecretName returns the name of the Secret that holds a generated key:
// <maildomain>-dkim-<selector> (ADR 0011).
func SecretName(mailDomain, selector string) string {
	return mailDomain + "-dkim-" + selector
}
