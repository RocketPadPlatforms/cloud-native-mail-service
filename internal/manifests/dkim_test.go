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

package manifests

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/dovecot"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/postfix"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/rspamd"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/valkey"
)

func dkimKey(secret, key, selector string, available bool) render.DKIMKey {
	return render.DKIMKey{Selector: selector, SecretName: secret, SecretKey: key, Available: available}
}

// buildWithKeys builds the object set for the authorized domains and
// their signing keys.
func buildWithKeys(t *testing.T, domains []string, keys ...render.DomainKeys) *DataPlane {
	t.Helper()
	req := &render.RenderRequest{Server: testServer(t, nil), AuthorizedDomains: domains, DKIMKeys: keys, HashKey: testHashKey}
	renders := map[render.Component]*render.RenderResult{}
	for _, r := range []render.EngineRenderer{postfix.New(), rspamd.New(), dovecot.New(), valkey.New()} {
		res, err := r.Render(req)
		if err != nil {
			t.Fatalf("render %s: %v", r.Component(), err)
		}
		renders[r.Component()] = res
	}
	dp, err := Build(req, renders)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return dp
}

func rspamdWorkload(t *testing.T, dp *DataPlane) *appsv1.Deployment {
	t.Helper()
	for _, w := range dp.Workloads {
		if d, ok := w.(*appsv1.Deployment); ok && d.Name == testName+"-rspamd" {
			return d
		}
	}
	t.Fatal("no rspamd Deployment")
	return nil
}

func dkimVolumeOf(pod corev1.PodSpec) *corev1.Volume {
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == dkimVolumeName {
			return &pod.Volumes[i]
		}
	}
	return nil
}

func TestRspamdMountsOnlyAvailableActiveKeys(t *testing.T) {
	dp := buildWithKeys(t, []string{domainB, domainA},
		render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{
			dkimKey("a-dkim-cnms-ed25519-20261103", "private.key", "cnms-ed25519-20261103", true),
			dkimKey("a-dkim-cnms-20261103", "private.key", "cnms-20261103", true),
		}},
		// Bring your own keys: two keys of one Secret, one of them missing.
		render.DomainKeys{Domain: domainB, Keys: []render.DKIMKey{
			dkimKey("byo", "s1.key", "s1", true),
			dkimKey("byo", "s2.key", "s2", false),
		}},
		// A revoked domain is not authorized: its key is not mounted.
		render.DomainKeys{Domain: "revoked.example", Keys: []render.DKIMKey{dkimKey("revoked", "private.key", "x", true)}},
	)
	dep := rspamdWorkload(t, dp)
	pod := dep.Spec.Template.Spec
	vol := dkimVolumeOf(pod)
	if vol == nil || vol.Projected == nil {
		t.Fatalf("no projected dkim volume: %+v", pod.Volumes)
	}
	if m := vol.Projected.DefaultMode; m == nil || *m != 0o440 {
		t.Errorf("defaultMode = %v, want 0440", m)
	}
	// One source per Secret, sorted by name; items sorted by path.
	type item struct{ secret, key, path string }
	var got []item
	for _, s := range vol.Projected.Sources {
		if s.Secret == nil {
			t.Fatalf("source %+v is not a Secret", s)
		}
		if s.Secret.Optional == nil || !*s.Secret.Optional {
			t.Errorf("Secret %s is not optional: a deleted key Secret would wedge the pod", s.Secret.Name)
		}
		for _, it := range s.Secret.Items {
			got = append(got, item{s.Secret.Name, it.Key, it.Path})
		}
	}
	want := []item{
		{"a-dkim-cnms-20261103", "private.key", "a.example/cnms-20261103.key"},
		{"a-dkim-cnms-ed25519-20261103", "private.key", "a.example/cnms-ed25519-20261103.key"},
		{"byo", "s1.key", "b.example/s1.key"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("projected items = %v, want %v", got, want)
	}
	if !hasMount(pod.Containers[0].VolumeMounts, dkimVolumeName, render.DKIMDir) {
		t.Errorf("rspamd misses the dkim mount: %+v", pod.Containers[0].VolumeMounts)
	}
	for _, m := range pod.Containers[0].VolumeMounts {
		if m.Name == dkimVolumeName && !m.ReadOnly {
			t.Error("the dkim mount must be read-only")
		}
	}
	// The Rspamd user reads the files through the pod fsGroup.
	if pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != rspamdGroupID {
		t.Errorf("fsGroup = %v, want the Rspamd group %d", pod.SecurityContext.FSGroup, rspamdGroupID)
	}
	// Only the main container sees the keys.
	for _, c := range pod.Containers[1:] {
		for _, m := range c.VolumeMounts {
			if m.Name == dkimVolumeName {
				t.Errorf("%s mounts the keys", c.Name)
			}
		}
	}
}

func TestRspamdWithoutKeysHasNoDKIMVolume(t *testing.T) {
	dp := buildWithKeys(t, []string{domainA},
		render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{dkimKey("gone", "private.key", "s1", false)}})
	pod := rspamdWorkload(t, dp).Spec.Template.Spec
	if dkimVolumeOf(pod) != nil {
		t.Error("no key is available, so no volume is wanted")
	}
	// The domain still fails closed: the guard names the selector.
	cm := configMapOf(t, dp, render.ComponentRspamd)
	if !strings.Contains(cm.Data[render.RspamdLocalLua], `"a.example:s=s1"`) {
		t.Error("the guard dropped the selector of the unavailable key")
	}
	if !strings.Contains(cm.Data["dkim_signing.conf"], "/etc/rspamd/dkim/a.example/s1.key") {
		t.Error("dkim_signing.conf dropped the selector of the unavailable key")
	}
}

func configMapOf(t *testing.T, dp *DataPlane, c render.Component) *corev1.ConfigMap {
	t.Helper()
	for _, o := range dp.Configs {
		if cm, ok := o.(*corev1.ConfigMap); ok && cm.Labels[componentLabel] == string(c) {
			return cm
		}
	}
	t.Fatalf("no ConfigMap for %s", c)
	return nil
}

// The guard script is mounted where Rspamd loads it, by the workload
// and by the validation Job (configtest rejects a broken script).
func TestRspamdGuardScriptIsMounted(t *testing.T) {
	dp := buildWithKeys(t, []string{domainA},
		render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{dkimKey("k", "private.key", "s1", true)}})
	isLua := func(m corev1.VolumeMount) bool {
		return m.Name == configVolumeName && m.MountPath == "/etc/rspamd/rspamd.local.lua" && m.SubPath == render.RspamdLocalLua && m.ReadOnly
	}
	if !slices.ContainsFunc(rspamdWorkload(t, dp).Spec.Template.Spec.Containers[0].VolumeMounts, isLua) {
		t.Error("rspamd does not mount rspamd.local.lua")
	}
	for _, v := range dp.Validations {
		if v.Component != render.ComponentRspamd {
			continue
		}
		pod := v.Job.Spec.Template.Spec
		if !slices.ContainsFunc(pod.Containers[0].VolumeMounts, isLua) {
			t.Error("the rspamd validation Job does not mount rspamd.local.lua")
		}
		// No Secret reaches the Job; a missing key must not hold back
		// the rollout (ADR 0011).
		for _, vol := range pod.Volumes {
			if vol.Projected != nil || vol.Secret != nil {
				t.Errorf("the rspamd validation Job mounts %s", vol.Name)
			}
		}
	}
}

// Key bytes never leave the Secrets: the ConfigMaps hold names only, and
// the pod template references Secrets, never their content.
func TestDKIMKeyBytesStayOutOfConfigAndTemplates(t *testing.T) {
	const marker = "-----BEGIN PRIVATE KEY-----"
	dp := buildWithKeys(t, []string{domainA},
		render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{dkimKey("a-dkim-s1", "private.key", "s1", true)}})
	for _, o := range dp.Configs {
		if cm, ok := o.(*corev1.ConfigMap); ok {
			for f, content := range cm.Data {
				if strings.Contains(content, marker) || strings.Contains(content, "a-dkim-s1") {
					t.Errorf("ConfigMap %s file %s names key material", cm.Name, f)
				}
			}
		}
	}
}

// The hash of the Rspamd configuration follows the selectors: a rotation
// goes through the validation gate. The Secrets are in the pod template,
// not in the hash.
func TestRspamdHashFollowsSelectorsNotSecrets(t *testing.T) {
	hashOf := func(secret string, selector string, available bool) (string, string) {
		dp := buildWithKeys(t, []string{domainA},
			render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{dkimKey(secret, "private.key", selector, available)}})
		dep := rspamdWorkload(t, dp)
		return dep.Spec.Template.Annotations[ConfigHashAnnotation], configMapOf(t, dp, render.ComponentRspamd).Name
	}
	base, baseName := hashOf("s-a", "s1", true)
	if h, n := hashOf("s-a", "s2", true); h == base || n == baseName {
		t.Error("another selector must give another hash and ConfigMap")
	}
	if h, n := hashOf("s-b", "s1", true); h != base || n != baseName {
		t.Error("another Secret for the same selector must not change the configuration")
	}
	if h, _ := hashOf("s-a", "s1", false); h != base {
		t.Error("the availability of a key must not change the configuration")
	}
	// ...but it does change what the pods mount.
	mounted := func(secret string) string {
		dp := buildWithKeys(t, []string{domainA},
			render.DomainKeys{Domain: domainA, Keys: []render.DKIMKey{dkimKey(secret, "private.key", "s1", true)}})
		return dkimVolumeOf(rspamdWorkload(t, dp).Spec.Template.Spec).Projected.Sources[0].Secret.Name
	}
	if mounted("s-a") != "s-a" || mounted("s-b") != "s-b" {
		t.Error("the pod template must name the Secret that holds the key")
	}
}

// A revoked domain disappears from the signer and the guard, and a server
// without any authorized domain still renders a safe guard.
func TestRevokedDomainLeavesSignerAndGuard(t *testing.T) {
	keys := []render.DomainKeys{
		{Domain: domainA, Keys: []render.DKIMKey{dkimKey("a-s1", "private.key", "s1", true)}},
		{Domain: domainB, Keys: []render.DKIMKey{dkimKey("b-s1", "private.key", "s1", true)}},
	}
	both := buildWithKeys(t, []string{domainA, domainB}, keys...)
	onlyA := buildWithKeys(t, []string{domainA}, keys...)
	none := buildWithKeys(t, nil, keys...)

	cfg := func(dp *DataPlane) map[string]string { return configMapOf(t, dp, render.ComponentRspamd).Data }
	for _, f := range []string{"dkim_signing.conf", render.RspamdLocalLua} {
		if !strings.Contains(cfg(both)[f], domainB) {
			t.Errorf("%s misses the authorized %s", f, domainB)
		}
		if strings.Contains(cfg(onlyA)[f], domainB) || !strings.Contains(cfg(onlyA)[f], domainA) {
			t.Errorf("%s of a server without %s: %s", f, domainB, cfg(onlyA)[f])
		}
		if strings.Contains(cfg(none)[f], domainA) || strings.Contains(cfg(none)[f], domainB) {
			t.Errorf("%s of a server without domains still names a domain", f)
		}
	}
	if !strings.Contains(cfg(none)[render.RspamdLocalLua], "local required = {\n}\n") {
		t.Error("the guard of a server without domains has no empty table")
	}
	if dkimVolumeOf(rspamdWorkload(t, none).Spec.Template.Spec) != nil {
		t.Error("a server without domains mounts keys")
	}
	if got := dkimVolumeOf(rspamdWorkload(t, onlyA).Spec.Template.Spec); got == nil || len(got.Projected.Sources) != 1 {
		t.Errorf("the revoked domain's key is still mounted: %+v", got)
	}
}
