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

package postfix

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
// Domains are intentionally passed unsorted to pin the sorted rendering.
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
		StaticUsers: []render.StaticUser{
			{Username: "testuser", PasswordHash: "{SHA512-CRYPT}x", Mailbox: "testuser@" + domainA},
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

func TestRenderMainCF(t *testing.T) {
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
		{
			name: "submission-disabled",
			mutate: func(s *v1alpha1.MailServer) {
				s.Spec.Submission = &v1alpha1.Submission{Enabled: new(false)}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := testRequest(t, tt.mutate)
			got := renderMainCF(req)
			checkGolden(t, tt.name+".maincf", got)
		})
	}
}

// ADR 0013: Postfix needs nothing more for signing. Mail from
// sendmail(1) and re-injected mail go through the same milter as SMTP
// mail (non_smtpd_milters), a milter that fails tempfails, and the
// content filter stays off for mail Postfix generates itself
// (internal_mail_filter_classes unset) until a Postfix with
// {postfix_internal_origin} is pinned.
func TestMainCFSignsThroughTheMilterOnly(t *testing.T) {
	req := testRequest(t, nil)
	req.DKIMKeys = []render.DomainKeys{{Domain: domainA, Keys: []render.DKIMKey{{Selector: "s1", Available: true}}}}
	for _, got := range []string{renderMainCF(req), renderMasterCF(req)} {
		if strings.Contains(got, "internal_mail_filter_classes") {
			t.Error("internal_mail_filter_classes must stay unset (ADR 0013)")
		}
	}
	main := renderMainCF(req)
	milter := "inet:" + render.ServiceFQDN(testName, testNamespace, render.ComponentRspamd) + ":11332"
	for _, want := range []string{
		"\nsmtpd_milters = " + milter + "\n",
		"\nnon_smtpd_milters = " + milter + "\n",
		"\nmilter_default_action = tempfail\n",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.cf misses %q", strings.TrimSpace(want))
		}
	}
}

func TestRenderMasterCF(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.MailServer)
	}{
		{name: "send-only"},
		{
			name: "submission-disabled",
			mutate: func(s *v1alpha1.MailServer) {
				s.Spec.Submission = &v1alpha1.Submission{Enabled: new(false)}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := testRequest(t, tt.mutate)
			got := renderMasterCF(req)
			checkGolden(t, tt.name+".mastercf", got)
		})
	}
}

func TestRenderVMailbox(t *testing.T) {
	tests := []struct {
		name  string
		users []render.StaticUser
	}{
		{name: "no-users"},
		{
			// Sorted, lower-cased and de-duplicated.
			name: "multi-user",
			users: []render.StaticUser{
				{Username: "zed", Mailbox: "Zed@" + domainB},
				{Username: "amy", Mailbox: "amy@" + domainA},
				{Username: "amy2", Mailbox: "AMY@" + domainA},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := testRequest(t, nil)
			req.StaticUsers = tt.users
			checkGolden(t, tt.name+".vmailbox", renderVMailbox(req))
		})
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
	if v := strings.Join(res.ValidateCommand, " "); !strings.Contains(v, "postfix check") || !strings.Contains(v, "postconf -n") || !strings.Contains(v, "warning:") {
		t.Errorf("unexpected validate command: %v", res.ValidateCommand)
	}
	if !strings.EqualFold(strings.Join(res.ReloadCommand, " "), "postfix reload") {
		t.Errorf("unexpected reload command: %v", res.ReloadCommand)
	}
	if _, ok := res.ConfigData["main.cf"]; !ok {
		t.Error("missing main.cf in config data")
	}
	if _, ok := res.ConfigData["master.cf"]; !ok {
		t.Error("missing master.cf in config data")
	}
	if got := res.ConfigData[vmailboxFile]; !strings.Contains(got, "testuser@"+domainA+" OK\n") {
		t.Errorf("recipient table misses the static user:\n%s", got)
	}
}

// With every domain revoked the configuration is fail closed: no
// virtual domains, no SASL, no submission listener, and the hostname
// is the in-cluster Service name.
func TestRenderNoAuthorizedDomains(t *testing.T) {
	req := testRequest(t, func(s *v1alpha1.MailServer) {
		s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
	})
	req.AuthorizedDomains = nil
	res, err := New().Render(req)
	if err != nil {
		t.Fatalf("Render() = %v, want success", err)
	}
	mainCF, masterCF := res.ConfigData["main.cf"], res.ConfigData["master.cf"]
	checkGolden(t, "no-domains.maincf", mainCF)
	checkGolden(t, "no-domains.mastercf", masterCF)
	for _, bad := range []string{"virtual_mailbox_domains", "virtual_transport", "smtpd_sasl", "a.example", "b.example"} {
		if strings.Contains(mainCF, bad) {
			t.Errorf("main.cf contains %q, want it fail closed", bad)
		}
	}
	if strings.Contains(masterCF, "587") {
		t.Error("master.cf has a submission listener, want none")
	}
	if !strings.Contains(mainCF, "myhostname = mail-postfix.tenant1.svc.cluster.local\n") {
		t.Error("main.cf myhostname is not the Service name")
	}
}
