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

package valkey

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

// testRequest builds a RenderRequest for the test MailServer. The
// rendered configuration does not reference the spec (the volume
// source of /data is a manifest concern), but the contract is the
// same as for every other renderer.
func testRequest(t *testing.T) *render.RenderRequest {
	t.Helper()
	return &render.RenderRequest{
		Server: &v1alpha1.MailServer{
			ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		},
		AuthorizedDomains: []string{domainB, domainA},
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

func TestRenderValkeyConf(t *testing.T) {
	res, err := New().Render(testRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.ConfigData["valkey.conf"]
	if !ok {
		t.Fatal("missing valkey.conf in config data")
	}
	checkGolden(t, "default.valkeyconf", got)
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
	res, err := New().Render(testRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.ValidateCommand, " ")
	if !strings.Contains(joined, "valkey-server /etc/valkey/valkey.conf") {
		t.Errorf("validate command does not target the rendered config: %v", res.ValidateCommand)
	}
	if !strings.Contains(joined, "valkey-cli -p 6379 ping") {
		t.Errorf("validate command does not ping the rendered port: %v", res.ValidateCommand)
	}
	// The start-up wait is a bounded poll, not a fixed sleep.
	if !strings.Contains(joined, "until ") || !strings.Contains(joined, "-le 50") || strings.Contains(joined, "sleep 1 ") {
		t.Errorf("validate command must poll the ping with a timeout: %v", res.ValidateCommand)
	}
	if !strings.Contains(joined, "shutdown nosave") {
		t.Errorf("validate command must shut the server down: %v", res.ValidateCommand)
	}
	if len(res.ReloadCommand) != 0 {
		t.Errorf("unexpected reload command: %v", res.ReloadCommand)
	}
	if _, ok := res.ConfigData["valkey.conf"]; !ok {
		t.Error("missing valkey.conf in config data")
	}
}

// Domain revocation leaves the Valkey configuration unchanged: it
// carries no domain list, so an empty authorized set must still render.
func TestRenderWithoutAuthorizedDomains(t *testing.T) {
	req := testRequest(t)
	req.AuthorizedDomains = nil
	if _, err := New().Render(req); err != nil {
		t.Fatalf("Render() = %v, want success", err)
	}
}
