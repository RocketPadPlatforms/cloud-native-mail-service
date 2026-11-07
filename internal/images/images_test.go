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
	"os"
	"regexp"
	"strings"
	"testing"
)

var (
	// upstreamRe is name:tag@sha256:<64 hex> (ADR 0004, section 6); the
	// registry or namespace prefix is optional (official images such as
	// postgres have none).
	upstreamRe = regexp.MustCompile(`^(?:[a-z0-9]+(?:\.[a-z0-9-]+)*/)?[a-z0-9._/-]+:[^@"]+@sha256:[0-9a-f]{64}$`)
	// localRe is name:tag without digest (CNMS-built images).
	localRe = regexp.MustCompile(`^ghcr\.io/rocketpadplatforms/cnms-[a-z0-9-]+:\d+\.\d+\.\d+-\d+$`)
)

func TestUpstreamPinsAreTagAndDigest(t *testing.T) {
	for name, ref := range map[string]string{
		"Dovecot":       Dovecot,
		"Rspamd":        Rspamd,
		"Valkey":        Valkey,
		"RedisExporter": RedisExporter,
		"Mailpit":       Mailpit,
		"CoreDNS":       CoreDNS,
		"OpenLDAP":      OpenLDAP,
		"PostgreSQL":    PostgreSQL,
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
			t.Errorf("%s: %q is not ghcr.io/rocketpadplatforms/cnms-<name>:<semver>-<revision>", name, ref)
		}
	}
}

// The tag of both CNMS-built images is <POSTFIX_VERSION>-<IMAGE_REVISION> of
// the Postfix Dockerfile (ADR 0004, item 6); the constants must follow it.
func TestLocalPinsMatchDockerfile(t *testing.T) {
	df, err := os.ReadFile("../../images/postfix/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	arg := func(name string) string {
		m := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)$`).FindSubmatch(df)
		if m == nil {
			t.Fatalf("ARG %s not found in images/postfix/Dockerfile", name)
		}
		return string(m[1])
	}
	tag := arg("POSTFIX_VERSION") + "-" + arg("IMAGE_REVISION")
	for _, ref := range []string{Postfix, E2EClient} {
		if !strings.HasSuffix(ref, ":"+tag) {
			t.Errorf("%q does not carry the Dockerfile tag %q", ref, tag)
		}
	}

	// The Makefile defaults build the images the e2e suite loads.
	mk, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"POSTFIX_IMAGE": Postfix, "E2E_CLIENT_IMAGE": E2EClient} {
		m := regexp.MustCompile(`(?m)^` + name + ` \?= (\S+)$`).FindSubmatch(mk)
		if m == nil {
			t.Fatalf("%s not found in Makefile", name)
		}
		if string(m[1]) != want {
			t.Errorf("Makefile %s = %q, want %q", name, m[1], want)
		}
	}
}

func TestPinsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for name, ref := range map[string]string{
		"Dovecot": Dovecot, "Rspamd": Rspamd, "Valkey": Valkey,
		"RedisExporter": RedisExporter, "Mailpit": Mailpit, "CoreDNS": CoreDNS,
		"OpenLDAP": OpenLDAP, "PostgreSQL": PostgreSQL,
		"Postfix": Postfix, "E2EClient": E2EClient,
	} {
		if prev, dup := seen[ref]; dup {
			t.Errorf("%s and %s pin the same image reference %q", prev, name, ref)
		}
		seen[ref] = name
	}
}
