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

// Command render-rspamd-fixture writes the Rspamd configuration of a
// fixed MailServer scenario, as the pod sees it, for
// hack/test-rspamd-guard.sh (ADR 0013, section 6):
//
//	<out>/local.d/*           the rendered local.d overrides
//	<out>/rspamd.local.lua    the never-unsigned guard
//	<out>/dkim/<domain>/*.key generated test keys (RSA and Ed25519)
//	<out>/validate.args       the validation command, one argument per line
//
// The configuration comes from the production renderer, so the script
// tests what the operator ships. The keys are throwaway test keys.
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/rspamd"
)

const (
	rsaSelector     = "cnms-20261103"
	ed25519Selector = "cnms-ed25519-20261103"
)

// key is a key of the scenario; an unavailable key is named in the
// configuration but has no file, like a key whose Secret is gone.
type key struct {
	selector  string
	available bool
}

// scenario maps each served domain to its active keys. The domain names
// are the contract with the script.
var scenario = map[string][]key{
	"dual.example":       {{rsaSelector, true}, {ed25519Selector, true}},
	"rsa.example":        {{rsaSelector, true}},
	"partial.example":    {{rsaSelector, true}, {ed25519Selector, false}},
	"missing.example":    {{rsaSelector, false}},
	"unreadable.example": {{rsaSelector, true}}, // the script revokes the permissions
	"nokeys.example":     nil,
}

func main() {
	out := flag.String("out", "", "directory to write the fixture to")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: render-rspamd-fixture -out <dir>")
		os.Exit(2)
	}
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "render-rspamd-fixture:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	req := &render.RenderRequest{
		Server: &v1alpha1.MailServer{ObjectMeta: metav1.ObjectMeta{Name: "mail", Namespace: "tenant1"}},
	}
	for domain, keys := range scenario {
		req.AuthorizedDomains = append(req.AuthorizedDomains, domain)
		dk := render.DomainKeys{Domain: domain}
		for _, k := range keys {
			dk.Keys = append(dk.Keys, render.DKIMKey{Selector: k.selector, Available: k.available})
			if !k.available {
				continue
			}
			alg := dkim.RSA
			if strings.Contains(k.selector, "ed25519") {
				alg = dkim.Ed25519
			}
			pem, err := dkim.Generate(alg, rand.Reader)
			if err != nil {
				return err
			}
			if err := write(filepath.Join(out, "dkim", render.DKIMKeyPath(domain, k.selector)), pem, 0o444); err != nil {
				return err
			}
		}
		req.DKIMKeys = append(req.DKIMKeys, dk)
	}
	res, err := rspamd.New().Render(req)
	if err != nil {
		return err
	}
	for name, content := range res.ConfigData {
		path := filepath.Join(out, "local.d", name)
		if name == render.RspamdLocalLua {
			path = filepath.Join(out, name)
		}
		if err := write(path, []byte(content), 0o444); err != nil {
			return err
		}
	}
	return write(filepath.Join(out, "validate.args"), []byte(strings.Join(res.ValidateCommand, "\n")+"\n"), 0o444)
}

// write creates the file and its directories world-readable: the
// container runs as the Rspamd user, not as the owner of the files.
func write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil { // MkdirAll honours the umask
		return err
	}
	return os.WriteFile(path, data, mode)
}
