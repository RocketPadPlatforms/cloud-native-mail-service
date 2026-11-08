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

package dovecot

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

var updateGolden = flag.Bool("update", false, "update golden files")

const (
	testNamespace = "tenant1"
	testName      = "mail"
	domainA       = "a.example"
	domainB       = "b.example"
)

// testRequest builds a RenderRequest for the test MailServer.
func testRequest(t *testing.T, mutate func(*v1alpha1.MailServer)) *render.RenderRequest {
	t.Helper()
	server := &v1alpha1.MailServer{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
	}
	if mutate != nil {
		mutate(server)
	}
	return &render.RenderRequest{
		Server:            server,
		AuthorizedDomains: []string{domainB, domainA},
		Accounts: []render.Account{
			{Name: "testuser", PasswordHash: "{SHA512-CRYPT}x", Address: "testuser@" + domainA},
		},
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s",
			name, got, string(want))
	}
}

func TestRenderDovecotConf(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.MailServer)
	}{
		{name: "send-only"},
		{
			name: "mailboxes",
			mutate: func(s *v1alpha1.MailServer) {
				s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := New().Render(testRequest(t, tt.mutate))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := res.ConfigData["dovecot.conf"]
			if !ok {
				t.Fatal("missing dovecot.conf in config data")
			}
			checkGolden(t, tt.name+".dovecotconf", got)
		})
	}
}

// Auth-only mode runs no protocol: no IMAP and no LMTP service.
func TestAuthOnlyHasNoLMTP(t *testing.T) {
	res, err := New().Render(testRequest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	conf := res.ConfigData["dovecot.conf"]
	if !strings.Contains(conf, "\nprotocols =\n") {
		t.Error("auth-only must render an empty protocols list")
	}
	for _, s := range []string{"service lmtp", "inet_listener lmtp", "service imap-login"} {
		if strings.Contains(conf, s) {
			t.Errorf("auth-only configuration contains %q", s)
		}
	}
	if !strings.Contains(conf, "inet_listener auth") {
		t.Error("auth-only configuration must keep the auth listener")
	}
}

// The layout is exactly maildir++ with mailboxes and absent without.
// `doveconf -n` accepts an unknown value such as maildirpp, and it is
// all the validation gate runs (ADR 0016, S1); only the first use of a
// mailbox fails on it. This test and the golden files are the guard.
func TestMailboxLayoutIsPinned(t *testing.T) {
	const want = "\nmailbox_list_layout = maildir++\n"
	on, err := New().Render(testRequest(t, func(s *v1alpha1.MailServer) {
		s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if conf := on.ConfigData["dovecot.conf"]; strings.Count(conf, "mailbox_list_layout") != 1 ||
		!strings.Contains(conf, want) {
		t.Errorf("mailboxes: want exactly one %q in\n%s", strings.TrimSpace(want), conf)
	}
	off, err := New().Render(testRequest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if conf := off.ConfigData["dovecot.conf"]; strings.Contains(conf, "mailbox_list_layout") {
		t.Errorf("auth-only configuration sets a mailbox layout:\n%s", conf)
	}
}

func TestRenderErrors(t *testing.T) {
	tests := []struct {
		name string
		req  *render.RenderRequest
	}{
		{name: "nil-request", req: nil},
		{
			name: "no-server",
			req:  &render.RenderRequest{AuthorizedDomains: []string{domainA}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New().Render(tt.req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestRenderResultCommands(t *testing.T) {
	res, err := New().Render(testRequest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(strings.Join(res.ValidateCommand, " "), "doveconf -n") {
		t.Errorf("unexpected validate command: %v", res.ValidateCommand)
	}
	if !strings.EqualFold(strings.Join(res.ReloadCommand, " "), "doveadm reload") {
		t.Errorf("unexpected reload command: %v", res.ReloadCommand)
	}
	if _, ok := res.ConfigData["dovecot.conf"]; !ok {
		t.Error("missing dovecot.conf in config data")
	}
	if _, ok := res.ConfigData["passdb"]; ok {
		t.Error("the users file (password hashes) must not be in the ConfigMap data")
	}
	if _, ok := res.LiveData["passdb"]; !ok {
		t.Error("missing passdb in the live data")
	}
	if _, ok := res.SecretData["passdb"]; ok {
		t.Error("the users files are the live data, not the hash-named Secret")
	}
}

// Domain revocation leaves the Dovecot configuration unchanged: it
// carries no domain list, so an empty authorized set must still render.
func TestRenderWithoutAuthorizedDomains(t *testing.T) {
	req := testRequest(t, nil)
	req.AuthorizedDomains = nil
	if _, err := New().Render(req); err != nil {
		t.Fatalf("Render() = %v, want success", err)
	}
}
