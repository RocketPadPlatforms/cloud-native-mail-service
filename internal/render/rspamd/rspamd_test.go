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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// are intentionally passed unsorted; without keys they appear in the
// guard only, which makes their outbound mail fail closed.
func testRequest(t *testing.T) *render.RenderRequest {
	t.Helper()
	server := &v1alpha1.MailServer{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
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
		{golden: "default.worker-proxy.inc", key: "worker-proxy.inc"},
		{golden: "default.redis.conf", key: "redis.conf"},
		{golden: "default.history_redis.conf", key: "history_redis.conf"},
		{golden: "default.options.inc", key: "options.inc"},
		{golden: "default.dkim_signing.conf", key: "dkim_signing.conf"},
		{golden: "default.rspamd.local.lua", key: render.RspamdLocalLua},
	}
	res, err := New().Render(testRequest(t))
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
	if !strings.HasPrefix(strings.Join(res.ValidateCommand, " "), "rspamadm configtest -c ") {
		t.Errorf("unexpected validate command: %v", res.ValidateCommand)
	}
	if !strings.EqualFold(strings.Join(res.ReloadCommand, " "), "rspamadm control reload") {
		t.Errorf("unexpected reload command: %v", res.ReloadCommand)
	}
	for _, key := range []string{
		"worker-controller.inc",
		"worker-normal.inc",
		"worker-proxy.inc",
		"redis.conf",
		"history_redis.conf",
		"options.inc",
		"dkim_signing.conf",
		render.RspamdLocalLua,
	} {
		if _, ok := res.ConfigData[key]; !ok {
			t.Errorf("missing %s in config data", key)
		}
	}
	if want := "mail-valkey.tenant1.svc.cluster.local:6379"; !strings.Contains(res.ConfigData["redis.conf"], want) {
		t.Errorf("redis.conf does not target %s", want)
	}
}

// With every domain revoked the guard still renders, with an empty
// table: nothing is served, so nothing is required.
func TestRenderWithoutAuthorizedDomains(t *testing.T) {
	req := testRequest(t)
	req.AuthorizedDomains = nil
	// Keys of a revoked domain must not reach the configuration.
	req.DKIMKeys = []render.DomainKeys{{Domain: domainA, Keys: []render.DKIMKey{{Selector: "s1", Available: true}}}}
	res, err := New().Render(req)
	if err != nil {
		t.Fatalf("Render() = %v, want success", err)
	}
	checkGolden(t, "nodomains.dkim_signing.conf", res.ConfigData["dkim_signing.conf"])
	checkGolden(t, "nodomains.rspamd.local.lua", res.ConfigData[render.RspamdLocalLua])
}

func key(selector string) render.DKIMKey {
	return render.DKIMKey{
		Selector: selector, SecretName: "secret-" + selector, SecretKey: "private.key", Available: true,
	}
}

// dkimRequests are the signing scenarios of the golden files.
func dkimRequests(t *testing.T) map[string]*render.RenderRequest {
	t.Helper()
	with := func(domains []string, keys ...render.DomainKeys) *render.RenderRequest {
		req := testRequest(t)
		req.AuthorizedDomains, req.DKIMKeys = domains, keys
		return req
	}
	unreadable := key("cnms-20261103")
	unreadable.Available = false
	return map[string]*render.RenderRequest{
		"rsa": with([]string{domainA},
			render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{key("cnms-20261103")}}),
		"dual": with([]string{domainA},
			render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{key("cnms-ed25519-20261103"), key("cnms-20261103")}}),
		// Unsorted input: the output is ordered by domain and selector.
		"multi": with([]string{domainB, domainA},
			render.DomainKeys{Domain: domainB, Keys: []render.DKIMKey{key("cnms-20261104")}},
			render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{key("cnms-ed25519-20261103"), key("cnms-20261103")}}),
		// b has no key at all, c has one that could not be read: both
		// stay in the guard, so their outbound mail fails closed.
		"missing": with([]string{domainA, domainB, "c.example"},
			render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{key("cnms-20261103")}},
			render.DomainKeys{Domain: "c.example", Keys: []render.DKIMKey{unreadable}}),
	}
}

func TestRenderDKIM(t *testing.T) {
	for name, req := range dkimRequests(t) {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(req)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, name+".dkim_signing.conf", res.ConfigData["dkim_signing.conf"])
			checkGolden(t, name+".rspamd.local.lua", res.ConfigData[render.RspamdLocalLua])
		})
	}
}

var (
	signingSelector = regexp.MustCompile(`path = "` + regexp.QuoteMeta(render.DKIMDir) + `/([^/"]+)/([^"]+)\.key"; selector = "([^"]+)";`)
	guardOption     = regexp.MustCompile(`"([^":]+):s=([^"]+)"`)
)

// The signer and the guard are rendered from the same keys: every
// signature the guard requires is one dkim_signing is configured to make,
// and the other way round.
func TestGuardAndSigningListTheSameSelectors(t *testing.T) {
	for name, req := range dkimRequests(t) {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(req)
			if err != nil {
				t.Fatal(err)
			}
			signed := signingSelector.FindAllStringSubmatch(res.ConfigData["dkim_signing.conf"], -1)
			guarded := guardOption.FindAllStringSubmatch(res.ConfigData[render.RspamdLocalLua], -1)
			signing := make([]string, 0, len(signed))
			guard := make([]string, 0, len(guarded))
			for _, m := range signed {
				if m[2] != m[3] {
					t.Errorf("key path names selector %q, the entry %q", m[2], m[3])
				}
				signing = append(signing, m[1]+":s="+m[3])
			}
			for _, m := range guarded {
				guard = append(guard, m[1]+":s="+m[2])
			}
			slices.Sort(signing)
			slices.Sort(guard)
			if !slices.Equal(signing, guard) {
				t.Errorf("signing %v, guard %v", signing, guard)
			}
		})
	}
}

// Nothing of a key but its name reaches the configuration.
func TestRenderHoldsNoKeyMaterial(t *testing.T) {
	req := dkimRequests(t)["dual"]
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	for file, content := range res.ConfigData {
		for _, secret := range []string{"secret-cnms-20261103", "private.key", "BEGIN"} {
			if strings.Contains(content, secret) {
				t.Errorf("%s contains %q", file, secret)
			}
		}
	}
}

// ownerRequest is a server with accounts: alice owns two addresses, bob one,
// an account that is disabled none, and one in a domain that is not served
// none.
func ownerRequest(t *testing.T) *render.RenderRequest {
	t.Helper()
	req := testRequest(t)
	req.Accounts = []render.Account{
		{Name: "bob", Address: "Bob@" + domainA, PasswordHash: "{X}b"},
		{Name: "alice", Address: "alice@" + domainA, PasswordHash: "{X}a", Aliases: []string{"Sales@" + domainB, "alice@" + domainB}},
		{Name: "off", Address: "off@" + domainA, PasswordHash: "{X}o", Disabled: true, Aliases: []string{"off@" + domainB}},
		{Name: "gone", Address: "gone@revoked.example", PasswordHash: "{X}g", Aliases: []string{"gone@" + domainA}},
	}
	return req
}

// Check 2 of ADR 0015, section 4.4: the header From of authenticated mail
// must be owned by the login. The table is the one of the envelope: the served
// addresses of the accounts that may send, lower case and sorted, in a data
// file of its own; the mode is rendered, not guessed from the table.
func TestRenderFromOwners(t *testing.T) {
	res, err := New().Render(ownerRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	owners, ok := res.DataFiles[render.RspamdOwnersFile]
	if !ok || len(res.DataFiles) != 1 {
		t.Fatalf("data files = %v, want %s alone", slices.Collect(maps.Keys(res.DataFiles)), render.RspamdOwnersFile)
	}
	checkGolden(t, "owners.from_owners", owners)
	checkGolden(t, "owners.rspamd.local.lua", res.ConfigData[render.RspamdLocalLua])
	if _, leaked := res.ConfigData[render.RspamdOwnersFile]; leaked {
		t.Error("the table of the owners is in the main ConfigMap, which it would push over its limit")
	}
	for line := range strings.SplitSeq(strings.TrimSpace(owners), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		addr, login, found := strings.Cut(line, " ")
		if !found || addr != strings.ToLower(addr) || strings.ContainsAny(login, " \t") {
			t.Errorf("line %q is not 'address login' in lower case", line)
		}
		for _, who := range []string{"off", "gone"} {
			if login == who {
				t.Errorf("line %q: a disabled or suspended account owns no header From", line)
			}
		}
	}
}

// The table of the owners is the envelope table: the same addresses and the
// same logins as smtpd_sender_login_maps, from the same rows.
func TestFromOwnersAreTheSendersOfThePlan(t *testing.T) {
	req := ownerRequest(t)
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.PlanAccounts(req.Accounts, req.AuthorizedDomains, req.ConflictedAddresses)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(plan.Senders))
	for _, s := range plan.Senders {
		want = append(want, s.Key+" "+s.Value)
	}
	var got []string
	for line := range strings.SplitSeq(res.DataFiles[render.RspamdOwnersFile], "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			got = append(got, line)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("from_owners = %v, want the senders %v", got, want)
	}
}

// A directory owns its addresses outside the cluster: no table, and the mode
// is off. A server of the operator without a single account has an empty table
// and the mode on: it refuses every address instead of letting every one pass.
func TestFromOwnersModeIsRenderedNotInferred(t *testing.T) {
	dir := testRequest(t)
	dir.Directory = &render.DirectorySource{}
	res, err := New().Render(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DataFiles) != 0 {
		t.Errorf("a directory server renders data files %v", slices.Collect(maps.Keys(res.DataFiles)))
	}
	if !strings.Contains(res.ConfigData[render.RspamdLocalLua], `local owners_mode = "off"`) {
		t.Error("a directory server does not switch check 2 off")
	}
	checkGolden(t, "directory.rspamd.local.lua", res.ConfigData[render.RspamdLocalLua])

	empty, err := New().Render(testRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.ConfigData[render.RspamdLocalLua], `local owners_mode = "on"`) {
		t.Error("a server without accounts does not keep check 2 on")
	}
	table, ok := empty.DataFiles[render.RspamdOwnersFile]
	if !ok {
		t.Fatal("a server without accounts has no table: the guard could not tell it from a missing file")
	}
	for line := range strings.SplitSeq(table, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("line %q in the table of a server without accounts", line)
		}
	}
	checkGolden(t, "empty.from_owners", table)
}

// An account that the renderer refuses stops the render of the table too.
func TestFromOwnersRefusesWhatThePlanRefuses(t *testing.T) {
	req := testRequest(t)
	req.Accounts = []render.Account{{Name: "a/b", Address: "a@" + domainA, PasswordHash: "{X}a"}}
	if _, err := New().Render(req); err == nil {
		t.Error("an account name with a slash was rendered")
	}
}
