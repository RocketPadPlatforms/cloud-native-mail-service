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

package images

import (
	"regexp"
	"testing"
)

var (
	// upstreamRe is name:tag@sha256:<64 hex> (ADR 0004, section 6).
	upstreamRe = regexp.MustCompile(`^[a-z0-9]+(?:\.[a-z0-9-]+)*/[a-z0-9._/-]+:[^@"]+@sha256:[0-9a-f]{64}$`)
	// localRe is name:tag without digest (CNMS-built images).
	localRe = regexp.MustCompile(`^cnms/[a-z0-9-]+:\d+\.\d+\.\d+$`)
)

func TestUpstreamPinsAreTagAndDigest(t *testing.T) {
	for name, ref := range map[string]string{
		"Dovecot":       Dovecot,
		"Rspamd":        Rspamd,
		"Valkey":        Valkey,
		"RedisExporter": RedisExporter,
		"Mailpit":       Mailpit,
	} {
		if !upstreamRe.MatchString(ref) {
			t.Errorf("%s: %q is not name:tag@sha256:<digest>", name, ref)
		}
	}
}

func TestLocalPinsAreTagOnly(t *testing.T) {
	for name, ref := range map[string]string{
		"Postfix":   Postfix,
		"E2EClient": E2EClient,
	} {
		if !localRe.MatchString(ref) {
			t.Errorf("%s: %q is not cnms/<name>:<semver>", name, ref)
		}
	}
}

func TestPinsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for name, ref := range map[string]string{
		"Dovecot": Dovecot, "Rspamd": Rspamd, "Valkey": Valkey,
		"RedisExporter": RedisExporter, "Mailpit": Mailpit,
		"Postfix": Postfix, "E2EClient": E2EClient,
	} {
		if prev, dup := seen[ref]; dup {
			t.Errorf("%s and %s pin the same image reference %q", prev, name, ref)
		}
		seen[ref] = name
	}
}
