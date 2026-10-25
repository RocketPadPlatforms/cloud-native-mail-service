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
		{
			name: "no-domains",
			req: &render.RenderRequest{
				Server: &v1alpha1.MailServer{
					ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
				},
			},
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
	if !strings.EqualFold(strings.Join(res.ValidateCommand, " "), "postfix check") {
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
}
