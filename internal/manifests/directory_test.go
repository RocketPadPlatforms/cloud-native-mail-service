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

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/dovecot"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/postfix"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/rspamd"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/valkey"
)

const directoryCA = "-----BEGIN CERTIFICATE-----\nZmFrZSBjYQ==\n-----END CERTIFICATE-----\n"

// buildWithDirectory renders a server that receives mail (mailboxes) with
// a directory instead of accounts.
func buildWithDirectory(t *testing.T, d *render.DirectorySource) *DataPlane {
	t.Helper()
	server := testServer(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	})
	req := &render.RenderRequest{
		Server: server, AuthorizedDomains: []string{domainA, domainB}, Hostname: testHostname, Directory: d, HashKey: testHashKey,
	}
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

func pgDirectory(tls string, ca []byte) *render.DirectorySource {
	return &render.DirectorySource{SQL: &directory.SQLTarget{
		Host: "sql.example.com", Port: 5432, TLSMode: tls, CA: ca, Database: "maildir", Username: "mailro", Password: "pw",
		Users: directory.Relation{Schema: "public", Name: "cnms_users"},
	}}
}

func ldapDirectory(anonymous bool) *render.DirectorySource {
	t := &directory.LDAPTarget{
		Host: "ldap.example.com", TLSMode: directory.LDAPStartTLS, CA: []byte(directoryCA), BaseDN: "dc=example,dc=org",
		BindDN: "cn=lookup,dc=example,dc=org", BindPassword: "pw", Attributes: directory.Attributes{Login: "uid", Mail: "mail"},
	}
	if anonymous {
		t.CA, t.BindDN, t.BindPassword = nil, "", ""
	}
	return &render.DirectorySource{LDAP: t}
}

func volumeByName(vols []corev1.Volume, name string) *corev1.Volume {
	for i := range vols {
		if vols[i].Name == name {
			return &vols[i]
		}
	}
	return nil
}

func mountByName(mounts []corev1.VolumeMount, name string) *corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == name {
			return &mounts[i]
		}
	}
	return nil
}

// The directory tables are a hash-named Secret that the Postfix pod and
// its validation Job mount, the libpq settings are the environment of the
// main container only, and no secret is in a ConfigMap.
func TestPostfixMountsTheDirectorySecret(t *testing.T) {
	dp := buildWithDirectory(t, pgDirectory(directory.SQLVerifyFull, []byte(directoryCA)))
	objs := slices.Concat(dp.Configs, dp.Workloads)
	sts := find(t, objs, kindStatefulSet, "mail-postfix").(*appsv1.StatefulSet)
	spec := sts.Spec.Template.Spec

	vol := volumeByName(spec.Volumes, directoryVolumeName)
	if vol == nil || vol.Secret == nil || vol.Secret.DefaultMode == nil || *vol.Secret.DefaultMode != 0o440 {
		t.Fatalf("directory volume = %+v, want a Secret volume with mode 0440", vol)
	}
	secret := find(t, objs, kindSecret, vol.Secret.SecretName).(*corev1.Secret)
	if _, ok := secret.Data["pgsql-mailbox.cf"]; !ok || secret.Immutable == nil || !*secret.Immutable {
		t.Errorf("the Secret %s is not an immutable Secret with the table", secret.Name)
	}

	main := spec.Containers[0]
	if m := mountByName(main.VolumeMounts, directoryVolumeName); m == nil || m.MountPath != render.PostfixSecretDir || !m.ReadOnly {
		t.Errorf("main container mount = %+v", m)
	}
	wantEnv := []corev1.EnvVar{{Name: "PGCONNECT_TIMEOUT", Value: "5"}, {Name: "PGSSLMODE", Value: "verify-full"}, {Name: "PGSSLROOTCERT", Value: render.PostfixSecretDir + "/ca.crt"}}
	if !slices.Equal(main.Env, wantEnv) {
		t.Errorf("main container env = %v, want %v", main.Env, wantEnv)
	}
	for _, c := range spec.Containers[1:] {
		if mountByName(c.VolumeMounts, directoryVolumeName) != nil || slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return strings.HasPrefix(e.Name, "PG") }) {
			t.Errorf("sidecar %s has the directory settings", c.Name)
		}
	}

	// The validation Job reads the same tables: Postfix warns about a
	// missing table file when it reads main.cf.
	for _, v := range dp.Validations {
		if v.Component != render.ComponentPostfix {
			continue
		}
		podSpec := v.Job.Spec.Template.Spec
		jv := volumeByName(podSpec.Volumes, directoryVolumeName)
		if jv == nil || jv.Secret.SecretName != vol.Secret.SecretName {
			t.Errorf("validation Job volume = %+v", jv)
		}
		if mountByName(podSpec.Containers[0].VolumeMounts, directoryVolumeName) == nil {
			t.Error("validation Job does not mount the directory tables")
		}
	}

	for _, o := range dp.Configs {
		if cm, ok := o.(*corev1.ConfigMap); ok {
			for k, v := range cm.Data {
				if v != "" && (strings.Contains(v, "pw\n") || strings.Contains(v, "ZmFrZSBjYQ==")) {
					t.Errorf("ConfigMap %s key %s holds a secret value", cm.Name, k)
				}
			}
		}
	}
}

// The environment is part of the config hash, so a changed TLS mode rolls
// the pods and runs the validation again; no environment leaves the hash
// of an earlier revision as it was.
func TestConfigHashCoversTheEnvironment(t *testing.T) {
	hash := func(d *render.DirectorySource) string {
		dp := buildWithDirectory(t, d)
		for _, v := range dp.Validations {
			if v.Component == render.ComponentPostfix {
				return v.Hash
			}
		}
		t.Fatal("no Postfix validation")
		return ""
	}
	if hash(pgDirectory(directory.SQLVerifyFull, nil)) == hash(pgDirectory(directory.SQLRequire, nil)) {
		t.Error("the TLS mode of the pgsql tables is not in the hash")
	}
	r := &render.RenderResult{ConfigData: map[string]string{"a": "b"}}
	base := configHash(render.ComponentPostfix, r)
	r.Env = map[string]string{"X": "y"}
	if configHash(render.ComponentPostfix, r) == base {
		t.Error("the environment is not in the hash")
	}
}

// A directory without a bind password and CA renders no Dovecot Secret: no
// users volume and no mount, instead of a reference to a Secret that does
// not exist. Without a directory the users Secret is mounted as before.
func TestDovecotUsersVolumeFollowsTheSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		d    *render.DirectorySource
		want bool
	}{
		"directory with credentials": {ldapDirectory(false), true},
		"anonymous directory":        {ldapDirectory(true), false},
	} {
		t.Run(name, func(t *testing.T) {
			dp := buildWithDirectory(t, tc.d)
			sts := find(t, slices.Concat(dp.Configs, dp.Workloads), kindStatefulSet, "mail-dovecot").(*appsv1.StatefulSet)
			spec := sts.Spec.Template.Spec
			if got := volumeByName(spec.Volumes, usersVolumeName) != nil; got != tc.want {
				t.Errorf("users volume present = %v, want %v", got, tc.want)
			}
			if got := mountByName(spec.Containers[0].VolumeMounts, usersVolumeName) != nil; got != tc.want {
				t.Errorf("users mount present = %v, want %v", got, tc.want)
			}
			if !tc.want {
				for _, o := range dp.Configs {
					if s, ok := o.(*corev1.Secret); ok && s.Labels[componentLabel] == string(render.ComponentDovecot) {
						t.Errorf("Secret %s rendered for nothing", s.Name)
					}
				}
			}
		})
	}
	// The static case keeps its volume.
	dp := buildPlane(t, nil, []render.Account{{Name: "u", PasswordHash: "{SHA512-CRYPT}z", Address: "u@" + domainA}})
	sts := find(t, slices.Concat(dp.Configs, dp.Workloads), kindStatefulSet, "mail-dovecot").(*appsv1.StatefulSet)
	if volumeByName(sts.Spec.Template.Spec.Volumes, usersVolumeName) == nil {
		t.Error("the users volume is gone for static users")
	}
}

// The hash of the secret files is keyed (ADR 0015, section 6): a plain
// SHA-256 over a bind password would let anyone who can read the pod
// template or the Secret names confirm a guess offline.
func TestSecretDigestIsKeyed(t *testing.T) {
	files := map[string]string{"ldap-password": "bind-secret"}
	plain := dataHash(files)
	a, b := secretDigest([]byte("key-a"), files), secretDigest([]byte("key-b"), files)
	if a == plain || b == plain {
		t.Error("the digest of the secret files is the plain hash")
	}
	if a == b {
		t.Error("the digest does not depend on the key")
	}
	if a != secretDigest([]byte("key-a"), files) {
		t.Error("the digest is not deterministic")
	}
	if got := secretDigest([]byte("key-a"), nil); got != dataHash(nil) {
		t.Errorf("no secret files: digest %s, want the plain hash of nothing", got)
	}
	if got := secretDigest([]byte("key-a"), map[string]string{"users": "# header only\n\n"}); got != dataHash(map[string]string{"users": "# header only\n\n"}) {
		t.Errorf("secret files of comments only: digest %s, want the plain hash", got)
	}

	// The hash of the pod template and the name of the Secret follow it,
	// and neither is the plain hash.
	dp := buildWithDirectory(t, pgDirectory(directory.SQLVerifyFull, []byte(directoryCA)))
	var pg *render.RenderResult
	for _, o := range dp.Configs {
		if s, ok := o.(*corev1.Secret); ok && strings.HasPrefix(s.Name, testName+"-postfix-") {
			if !strings.HasSuffix(s.Name, "-"+secretDigest(testHashKey, pgSecretFiles(s))[:8]) {
				t.Errorf("Secret %s is not named by the keyed digest", s.Name)
			}
			if strings.HasSuffix(s.Name, "-"+dataHash(pgSecretFiles(s))[:8]) {
				t.Errorf("Secret %s is named by the plain hash", s.Name)
			}
			pg = &render.RenderResult{SecretData: pgSecretFiles(s)}
		}
	}
	if pg == nil {
		t.Fatal("no Postfix Secret")
	}
}

func pgSecretFiles(s *corev1.Secret) map[string]string {
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out
}

// Secret content without a key is refused, so no caller can fall back to a
// plain hash by leaving the key out; files without content (the fail-closed
// render) need none.
func TestBuildNeedsAHashKey(t *testing.T) {
	server := testServer(t, nil)
	req := &render.RenderRequest{Server: server, AuthorizedDomains: []string{domainA}, Hostname: testHostname,
		Accounts: []render.Account{{Name: "alice", PasswordHash: testCryptHash, Address: "alice@" + domainA}}}
	renders := map[render.Component]*render.RenderResult{}
	for _, r := range []render.EngineRenderer{postfix.New(), rspamd.New(), dovecot.New(), valkey.New()} {
		res, err := r.Render(req)
		if err != nil {
			t.Fatal(err)
		}
		renders[r.Component()] = res
	}
	if _, err := Build(req, renders); err == nil {
		t.Error("Build accepted secret files without a hash key")
	}

	// Without secret content there is nothing to key: no key is needed.
	req.Accounts = nil
	for _, r := range []render.EngineRenderer{postfix.New(), rspamd.New(), dovecot.New(), valkey.New()} {
		res, err := r.Render(req)
		if err != nil {
			t.Fatal(err)
		}
		renders[r.Component()] = res
	}
	if _, err := Build(req, renders); err != nil {
		t.Errorf("Build needs a hash key for a render without secret content: %v", err)
	}
}
