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

package rspamd

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

// testRequest builds a RenderRequest for the test MailServer. Domains
// are intentionally passed unsorted; the Phase 2 Rspamd config does not
// reference them (DKIM arrives with Phase 5), but the contract is the
// same as for every other renderer.
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

func TestRenderConfigFiles(t *testing.T) {
	tests := []struct {
		golden string
		key    string
	}{
		{golden: "default.worker-controller.inc", key: "worker-controller.inc"},
		{golden: "default.worker-normal.inc", key: "worker-normal.inc"},
		{golden: "default.redis.conf", key: "redis.conf"},
	}
	res, err := New().Render(testRequest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			got, ok := res.ConfigData[tt.key]
			if !ok {
				t.Fatalf("missing %s in config data", tt.key)
			}
			checkGolden(t, tt.golden, got)
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
	if !strings.EqualFold(strings.Join(res.ValidateCommand, " "), "rspamadm configtest") {
		t.Errorf("unexpected validate command: %v", res.ValidateCommand)
	}
	if !strings.EqualFold(strings.Join(res.ReloadCommand, " "), "rspamadm control reload") {
		t.Errorf("unexpected reload command: %v", res.ReloadCommand)
	}
	for _, key := range []string{
		"worker-controller.inc",
		"worker-normal.inc",
		"redis.conf",
	} {
		if _, ok := res.ConfigData[key]; !ok {
			t.Errorf("missing %s in config data", key)
		}
	}
	if want := "mail-valkey.tenant1.svc.cluster.local:6379"; !strings.Contains(res.ConfigData["redis.conf"], want) {
		t.Errorf("redis.conf does not target %s", want)
	}
}
