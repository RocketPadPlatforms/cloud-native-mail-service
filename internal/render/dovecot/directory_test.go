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
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The two kinds of directory, as test names.
const (
	srcLDAP  = "ldap"
	srcPgSQL = "pgsql"
)

// testCA is a stand-in for a CA bundle: the renderer copies it, it never
// parses it.
const testCA = "-----BEGIN CERTIFICATE-----\nZmFrZSBjYQ==\n-----END CERTIFICATE-----\n"

const (
	ldapSecret = "ldap-s3cr3t-$%'\"\\#"
	sqlSecret  = "p@ss$w%rd'x"
)

func ldapSource(mutate func(*directory.LDAPTarget)) *render.DirectorySource {
	t := &directory.LDAPTarget{
		Host: "ldap.example.com", TLSMode: directory.LDAPStartTLS, CA: []byte(testCA),
		BaseDN: "ou=people,dc=example,dc=org", BindDN: "cn=lookup,ou=services,dc=example,dc=org", BindPassword: ldapSecret,
		Attributes:   directory.Attributes{Login: "uid", Mail: "mail", Aliases: "mailAlias"},
		ActiveFilter: "(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))",
	}
	if mutate != nil {
		mutate(t)
	}
	return &render.DirectorySource{LDAP: t}
}

func sqlSource(mutate func(*directory.SQLTarget)) *render.DirectorySource {
	t := &directory.SQLTarget{
		Host: "sql.example.com", Port: 5432, TLSMode: directory.SQLVerifyFull, CA: []byte(testCA),
		Database: "maildir", Username: "mailro", Password: sqlSecret,
		Users:   directory.Relation{Schema: "public", Name: "cnms_users"},
		Aliases: &directory.Relation{Schema: "public", Name: "cnms_aliases"},
	}
	if mutate != nil {
		mutate(t)
	}
	return &render.DirectorySource{SQL: t}
}

// directoryRequest is a request with a directory and no accounts.
func directoryRequest(t *testing.T, d *render.DirectorySource, mailboxes bool) *render.RenderRequest {
	t.Helper()
	req := testRequest(t, func(s *v1alpha1.MailServer) {
		if mailboxes {
			s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
		}
	})
	req.Accounts, req.Directory = nil, d
	return req
}

func secretGolden(files map[string]string) string {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		b.WriteString("=== " + name + " ===\n" + files[name])
		if !strings.HasSuffix(files[name], "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestRenderDirectoryGolden(t *testing.T) {
	for name, d := range map[string]*render.DirectorySource{
		srcLDAP:  ldapSource(nil),
		srcPgSQL: sqlSource(nil),
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, d, true))
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, "directory-"+name+".dovecotconf", res.ConfigData["dovecot.conf"])
			checkGolden(t, "directory-"+name+".secret", secretGolden(res.SecretData))
		})
	}
}

// No secret value reaches the ConfigMap, the settings read them from
// files, and the directory replaces the passwd-file: the users file does
// not exist.
func TestDirectorySecretsStayInTheSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		d      *render.DirectorySource
		secret string
		file   string
	}{
		srcLDAP:  {ldapSource(nil), ldapSecret, ldapPasswordFile},
		srcPgSQL: {sqlSource(nil), sqlSecret, sqlPasswordFile},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, tc.d, true))
			if err != nil {
				t.Fatal(err)
			}
			conf := res.ConfigData["dovecot.conf"]
			for _, leak := range []string{tc.secret, "ZmFrZSBjYQ=="} {
				if strings.Contains(conf, leak) {
					t.Errorf("dovecot.conf holds %q", leak)
				}
			}
			if !strings.Contains(conf, "<"+render.DovecotSecretDir+"/"+tc.file+"\n") {
				t.Errorf("the password is not read from %s:\n%s", tc.file, conf)
			}
			if res.SecretData[tc.file] != tc.secret {
				t.Errorf("the Secret holds %q as %s", res.SecretData[tc.file], tc.file)
			}
			// The passwd-file is the logins of the service accounts, a path of
			// the users Secret and no Secret of the revision.
			if _, ok := res.SecretData["users"]; ok || strings.Contains(conf, "userdb passwd-file") {
				t.Error("a directory renders no users Secret and no userdb of the passwd-file")
			}
		})
	}
}

func TestDirectoryLDAPVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*directory.LDAPTarget)
		want   []string
		absent []string
		files  []string
	}{
		"StartTLS with a CA": {
			want:  []string{"ldap_uris = ldap://ldap.example.com:389\n", "ldap_starttls = yes\n", "ssl_client_ca_file = /etc/dovecot-secret/ca.crt\n", "ldap_auth_dn = \"cn=lookup,ou=services,dc=example,dc=org\"\n"},
			files: []string{caFile, ldapPasswordFile},
		},
		"LDAPS": {
			mutate: func(l *directory.LDAPTarget) { l.TLSMode = directory.LDAPS },
			want:   []string{"ldap_uris = ldaps://ldap.example.com:636\n"},
			absent: []string{"ldap_starttls"},
			files:  []string{caFile, ldapPasswordFile},
		},
		"no CA uses the system roots": {
			mutate: func(l *directory.LDAPTarget) { l.CA = nil },
			absent: []string{"ssl_client_ca_file"},
			files:  []string{ldapPasswordFile},
		},
		"a percent sign is literal, a hash is quoted": {
			mutate: func(l *directory.LDAPTarget) { l.BaseDN = `ou=50%,o=a#b,dc="x"` },
			want:   []string{`ldap_base = "ou=50%,o=a#b,dc=\"x\""` + "\n"},
			files:  []string{caFile, ldapPasswordFile},
		},
		"other attribute names": {
			mutate: func(l *directory.LDAPTarget) {
				l.Attributes = directory.Attributes{Login: "sAMAccountName", Mail: "proxyMail"}
				l.ActiveFilter = ""
			},
			want:  []string{"user = %{ldap:sAMAccountName}\n", "home = /srv/mail/%{ldap:sAMAccountName}\n", "(sAMAccountName=%{user})(proxyMail=%{user})"},
			files: []string{caFile, ldapPasswordFile},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, ldapSource(tc.mutate), false))
			if err != nil {
				t.Fatal(err)
			}
			conf := res.ConfigData["dovecot.conf"]
			for _, w := range tc.want {
				if !strings.Contains(conf, w) {
					t.Errorf("dovecot.conf misses %q:\n%s", strings.TrimSpace(w), conf)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(conf, a) {
					t.Errorf("dovecot.conf holds %q", a)
				}
			}
			if tc.files != nil {
				if got := slices.Sorted(maps.Keys(res.SecretData)); !slices.Equal(got, tc.files) {
					t.Errorf("secret files = %v, want %v", got, tc.files)
				}
			} else if len(res.SecretData) != 0 {
				t.Errorf("secret files = %v, want none", slices.Sorted(maps.Keys(res.SecretData)))
			}
			// Both lookups use the filter of the probe, with the key
			// of Dovecot, and log in by the login name or the primary
			// address and never by an alias.
			d := ldapSource(tc.mutate).LDAP
			f, err := directory.LookupFilter(d.Attributes, d.ActiveFilter, directory.ByLogin, directory.KeyDovecot)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(conf, "filter = \""+f+"\"\n"); got != 2 {
				t.Errorf("the shared filter %q is used %d times, want 2 (passdb and userdb)", f, got)
			}
			if d.Attributes.Aliases != "" && strings.Contains(conf, d.Attributes.Aliases) {
				t.Error("an alias attribute is in the Dovecot lookup")
			}
		})
	}
}

func TestDirectoryPgSQLVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*directory.SQLTarget)
		want   []string
		absent []string
		files  []string
	}{
		"verify-full with a CA": {
			want:  []string{"    sslmode = verify-full\n", "    sslrootcert = /etc/dovecot-secret/ca.crt\n", "    password = </etc/dovecot-secret/sql-password\n"},
			files: []string{caFile, sqlPasswordFile},
		},
		"verify-full without a CA": {
			mutate: func(s *directory.SQLTarget) { s.CA = nil },
			want:   []string{"    sslmode = verify-full\n", "    sslrootcert = system\n"},
			files:  []string{sqlPasswordFile},
		},
		"require with a CA": {
			mutate: func(s *directory.SQLTarget) { s.TLSMode = directory.SQLRequire },
			want:   []string{"    sslmode = require\n", "    sslrootcert = /etc/dovecot-secret/ca.crt\n"},
			files:  []string{caFile, sqlPasswordFile},
		},
		"require without a CA": {
			mutate: func(s *directory.SQLTarget) { s.TLSMode, s.CA = directory.SQLRequire, nil },
			want:   []string{"    sslmode = require\n"},
			absent: []string{"sslrootcert"},
			files:  []string{sqlPasswordFile},
		},
		"disable": {
			mutate: func(s *directory.SQLTarget) { s.TLSMode, s.CA = directory.SQLDisable, nil },
			want:   []string{"    sslmode = disable\n"},
			absent: []string{"sslrootcert"},
			files:  []string{sqlPasswordFile},
		},
		"another schema": {
			mutate: func(s *directory.SQLTarget) { s.Users = directory.Relation{Schema: "directory", Name: "mail_users"} },
			want:   []string{`FROM \"directory\".\"mail_users\" WHERE`},
			files:  []string{caFile, sqlPasswordFile},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, sqlSource(tc.mutate), false))
			if err != nil {
				t.Fatal(err)
			}
			conf := res.ConfigData["dovecot.conf"]
			for _, w := range tc.want {
				if !strings.Contains(conf, w) {
					t.Errorf("dovecot.conf misses %q:\n%s", strings.TrimSpace(w), conf)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(conf, a) {
					t.Errorf("dovecot.conf holds %q", a)
				}
			}
			if got := slices.Sorted(maps.Keys(res.SecretData)); !slices.Equal(got, tc.files) {
				t.Errorf("secret files = %v, want %v", got, tc.files)
			}
			// The queries use the login guard of the probe, the key is
			// the quoted placeholder only, and no alias relation is read.
			if got := strings.Count(conf, strings.ReplaceAll(directory.SQLLoginGuard, `\`, `\\`)); got != 2 {
				t.Errorf("the login guard is used %d times, want 2 (passdb and userdb)", got)
			}
			if got := strings.Count(conf, "'%{user}'"); got != 4 {
				t.Errorf("the key appears %d times, want 4 (two per query)", got)
			}
			if strings.Contains(conf, "alias") {
				t.Error("an alias relation is in the Dovecot lookup")
			}
		})
	}
}

// Auth-only mode (submission without mailboxes) keeps working with a
// directory: the passdb is there, and no LMTP or IMAP listener.
func TestDirectoryAuthOnly(t *testing.T) {
	for name, d := range map[string]*render.DirectorySource{srcLDAP: ldapSource(nil), srcPgSQL: sqlSource(nil)} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, d, false))
			if err != nil {
				t.Fatal(err)
			}
			conf := res.ConfigData["dovecot.conf"]
			if !strings.Contains(conf, "\nprotocols =\n") || !strings.Contains(conf, "\npassdb ") || !strings.Contains(conf, "inet_listener auth") {
				t.Errorf("auth-only configuration with a directory is incomplete:\n%s", conf)
			}
			for _, s := range []string{"service lmtp", "service imap-login"} {
				if strings.Contains(conf, s) {
					t.Errorf("auth-only configuration contains %q", s)
				}
			}
		})
	}
}

func TestDirectoryRefusesUnsafeSettings(t *testing.T) {
	for name, d := range map[string]*render.DirectorySource{
		"newline in the LDAP password": ldapSource(func(l *directory.LDAPTarget) { l.BindPassword = "pw\nldap_uris = ldap://evil" }),
		"trailing newline":             ldapSource(func(l *directory.LDAPTarget) { l.BindPassword = "pw\n" }),
		"newline in the bind DN":       ldapSource(func(l *directory.LDAPTarget) { l.BindDN = "cn=a\npasswd" }),
		"a variable in the base DN":    ldapSource(func(l *directory.LDAPTarget) { l.BaseDN = "ou=%{user},dc=example,dc=org" }),
		"a variable in the bind DN":    ldapSource(func(l *directory.LDAPTarget) { l.BindDN = "cn=%{user},dc=example,dc=org" }),
		"blank around the base DN":     ldapSource(func(l *directory.LDAPTarget) { l.BaseDN = "dc=example,dc=org " }),
		"no TLS: the password of a login would travel in clear text": ldapSource(func(l *directory.LDAPTarget) {
			l.TLSMode, l.CA, l.BindDN, l.BindPassword = directory.LDAPNoTLS, nil, "", ""
		}),
		"filter in an attribute name": ldapSource(func(l *directory.LDAPTarget) { l.Attributes.Login = "uid)(x" }),
		"broken active filter":        ldapSource(func(l *directory.LDAPTarget) { l.ActiveFilter = "(a=b))(|(c=d)" }),
		"newline in the SQL password": sqlSource(func(s *directory.SQLTarget) { s.Password = "pw\nquery = x" }),
		"no settings":                 {},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, d, true))
			if err == nil {
				t.Fatalf("rendered %v, want an error", res)
			}
			if strings.Contains(err.Error(), "pw") {
				t.Errorf("the error holds a secret value: %v", err)
			}
		})
	}
}
