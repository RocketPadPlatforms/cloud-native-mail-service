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
	"maps"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The two kinds of directory, as test names, and values the tests share.
const (
	srcLDAP  = "ldap"
	srcPgSQL = "pgsql"

	requireCert  = "tls_require_cert = yes\n"
	caPath       = "/etc/postfix-secret/ca.crt"
	aliasDomains = "\nvirtual_alias_domains =\n"
	envSSLMode   = "PGSSLMODE"
	envRootCert  = "PGSSLROOTCERT"
	envTimeout   = "PGCONNECT_TIMEOUT"
)

// testCA is a stand-in for a CA bundle: the renderer copies it, it never
// parses it.
const testCA = "-----BEGIN CERTIFICATE-----\nZmFrZSBjYQ==\n-----END CERTIFICATE-----\n"

// The passwords hold the characters that need care in a .cf file: they
// are taken literally there (verified on 3.11.7), so none may be escaped.
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

func directoryRequest(t *testing.T, d *render.DirectorySource) *render.RenderRequest {
	t.Helper()
	req := testRequest(t, func(s *v1alpha1.MailServer) {
		s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
	})
	req.Accounts, req.Directory = nil, d
	return req
}

// secretGolden joins the files of the Secret in a stable order.
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
			res, err := New().Render(directoryRequest(t, d))
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, "directory-"+name+".maincf", res.ConfigData["main.cf"])
			checkGolden(t, "directory-"+name+".secret", secretGolden(res.SecretData))
			if len(res.Env) > 0 {
				checkGolden(t, "directory-"+name+".env", secretGolden(res.Env))
			} else if name == srcPgSQL {
				t.Error("pgsql renders no environment")
			}
		})
	}
}

// No secret value reaches a ConfigMap, and the maps do not use the URI
// form of pgsql (it logs the password on a failed connection).
func TestDirectorySecretsStayInTheSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		d      *render.DirectorySource
		secret string
	}{
		srcLDAP:  {ldapSource(nil), ldapSecret},
		srcPgSQL: {sqlSource(nil), sqlSecret},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, tc.d))
			if err != nil {
				t.Fatal(err)
			}
			for file, content := range res.ConfigData {
				for _, leak := range []string{tc.secret, "cn=lookup", "ZmFrZSBjYQ=="} {
					if strings.Contains(content, leak) {
						t.Errorf("ConfigMap file %s holds %q", file, leak)
					}
				}
			}
			if !strings.Contains(secretGolden(res.SecretData), tc.secret) {
				t.Error("the password is not in the Secret")
			}
			for file, content := range res.SecretData {
				if strings.Contains(content, "postgresql://") {
					t.Errorf("%s uses the URI form of pgsql", file)
				}
			}
			for key, v := range res.Env {
				if strings.Contains(v, tc.secret) {
					t.Errorf("environment variable %s holds the password", key)
				}
			}
		})
	}
}

func TestDirectoryLDAPVariants(t *testing.T) {
	filterOf := func(by directory.Lookup, mutate func(*directory.LDAPTarget)) string {
		t.Helper()
		d := ldapSource(mutate)
		f, err := directory.LookupFilter(d.LDAP.Attributes, d.LDAP.ActiveFilter, by, directory.KeyPostfix)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	for name, tc := range map[string]struct {
		mutate func(*directory.LDAPTarget)
		want   []string
		absent []string
	}{
		"LDAPS": {
			mutate: func(l *directory.LDAPTarget) { l.TLSMode = directory.LDAPS },
			want:   []string{"server_host = ldaps://ldap.example.com:636\n", "start_tls = no\n", requireCert, "tls_ca_cert_file = /etc/postfix-secret/ca.crt\n"},
		},
		"StartTLS on another port": {
			mutate: func(l *directory.LDAPTarget) { l.Port = 3890 },
			want:   []string{"server_host = ldap://ldap.example.com:3890\n", "start_tls = yes\n", requireCert},
		},
		"StartTLS without a CA uses the system roots": {
			mutate: func(l *directory.LDAPTarget) { l.CA = nil },
			want:   []string{"start_tls = yes\n", requireCert},
			absent: []string{"tls_ca_cert_file"},
		},
		"no TLS, anonymous": {
			mutate: func(l *directory.LDAPTarget) {
				l.TLSMode, l.CA, l.BindDN, l.BindPassword = directory.LDAPNoTLS, nil, "", ""
			},
			want:   []string{"server_host = ldap://ldap.example.com:389\n", "bind = no\n", "start_tls = no\n"},
			absent: []string{"bind_dn", "bind_pw", "tls_require_cert", "tls_ca_cert_file"},
		},
		"a percent sign in the search base is doubled": {
			mutate: func(l *directory.LDAPTarget) { l.BaseDN = "ou=50%,dc=example,dc=org" },
			want:   []string{"search_base = ou=50%%,dc=example,dc=org\n"},
		},
		"other attribute names": {
			mutate: func(l *directory.LDAPTarget) {
				l.Attributes = directory.Attributes{Login: "sAMAccountName", Mail: "proxyMail", Aliases: "otherMailbox"}
				l.ActiveFilter = ""
			},
			want: []string{"result_attribute = proxyMail\n"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, ldapSource(tc.mutate)))
			if err != nil {
				t.Fatal(err)
			}
			got := res.SecretData[ldapMailboxFile]
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%s misses %q:\n%s", ldapMailboxFile, strings.TrimSpace(w), got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("%s holds %q:\n%s", ldapMailboxFile, a, got)
				}
			}
			// The tables use the one filter builder of the probe.
			for file, by := range map[string]directory.Lookup{ldapMailboxFile: directory.ByMail, ldapAliasFile: directory.ByAlias} {
				if want := "query_filter = " + filterOf(by, tc.mutate) + "\n"; !strings.Contains(res.SecretData[file], want) {
					t.Errorf("%s does not use the shared filter %q:\n%s", file, want, res.SecretData[file])
				}
			}
			if _, ok := res.SecretData[caFile]; ok != (len(ldapSource(tc.mutate).LDAP.CA) > 0 && ldapSource(tc.mutate).LDAP.TLSMode != directory.LDAPNoTLS) {
				t.Errorf("CA file present = %v", ok)
			}
		})
	}
}

func TestDirectoryPgSQLVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*directory.SQLTarget)
		env    map[string]string
		files  []string
		want   string
	}{
		"verify-full with a CA": {
			env:   map[string]string{envTimeout: "5", envSSLMode: directory.SQLVerifyFull, envRootCert: caPath},
			files: []string{caFile, pgsqlAliasFile, pgsqlMailboxFile, pgsqlSenderFile},
		},
		"verify-full without a CA verifies against the system roots": {
			mutate: func(s *directory.SQLTarget) { s.CA = nil },
			env:    map[string]string{envTimeout: "5", envSSLMode: directory.SQLVerifyFull, envRootCert: "system"},
			files:  []string{pgsqlAliasFile, pgsqlMailboxFile, pgsqlSenderFile},
		},
		"require with a CA checks the chain": {
			mutate: func(s *directory.SQLTarget) { s.TLSMode = directory.SQLRequire },
			env:    map[string]string{envTimeout: "5", envSSLMode: "require", envRootCert: caPath},
			files:  []string{caFile, pgsqlAliasFile, pgsqlMailboxFile, pgsqlSenderFile},
		},
		"require without a CA": {
			mutate: func(s *directory.SQLTarget) { s.TLSMode, s.CA = directory.SQLRequire, nil },
			env:    map[string]string{envTimeout: "5", envSSLMode: "require"},
			files:  []string{pgsqlAliasFile, pgsqlMailboxFile, pgsqlSenderFile},
		},
		"disable": {
			mutate: func(s *directory.SQLTarget) { s.TLSMode, s.CA = directory.SQLDisable, nil },
			env:    map[string]string{envTimeout: "5", envSSLMode: "disable"},
			files:  []string{pgsqlAliasFile, pgsqlMailboxFile, pgsqlSenderFile},
		},
		"no aliases relation": {
			mutate: func(s *directory.SQLTarget) { s.Aliases = nil },
			env:    map[string]string{envTimeout: "5", envSSLMode: directory.SQLVerifyFull, envRootCert: caPath},
			files:  []string{caFile, pgsqlMailboxFile, pgsqlSenderFile},
		},
		"another schema": {
			mutate: func(s *directory.SQLTarget) { s.Users = directory.Relation{Schema: "directory", Name: "mail_users"} },
			env:    map[string]string{envTimeout: "5", envSSLMode: directory.SQLVerifyFull, envRootCert: caPath},
			files:  []string{caFile, pgsqlAliasFile, pgsqlMailboxFile, pgsqlSenderFile},
			want:   `FROM "directory"."mail_users" WHERE`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, sqlSource(tc.mutate)))
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(res.Env, tc.env) {
				t.Errorf("env = %v, want %v", res.Env, tc.env)
			}
			if got := sets.List(sets.KeySet(res.SecretData)); !slices.Equal(got, tc.files) {
				t.Errorf("secret files = %v, want %v", got, tc.files)
			}
			if tc.want != "" && !strings.Contains(res.SecretData[pgsqlMailboxFile], tc.want) {
				t.Errorf("%s misses %q:\n%s", pgsqlMailboxFile, tc.want, res.SecretData[pgsqlMailboxFile])
			}
			// The login guard is the one of the probe, and the key is
			// the quoted placeholder only.
			mailbox := res.SecretData[pgsqlMailboxFile]
			if !strings.Contains(mailbox, directory.SQLLoginGuard) || strings.Count(mailbox, "'%s'") != 1 {
				t.Errorf("unexpected mailbox query:\n%s", mailbox)
			}
		})
	}
}

// The environment and the aliases line of main.cf appear only with the
// tables that need them.
func TestDirectoryMainCF(t *testing.T) {
	ldapOnly := func(l *directory.LDAPTarget) { l.Attributes.Aliases = "" }
	for name, tc := range map[string]struct {
		d      *render.DirectorySource
		want   []string
		absent []string
	}{
		"ldap": {
			d:      ldapSource(nil),
			want:   []string{aliasDomains, "\nvirtual_alias_maps = ldap:/etc/postfix-secret/ldap-alias.cf\n", "\nvirtual_mailbox_maps = ldap:/etc/postfix-secret/ldap-mailbox.cf\n"},
			absent: []string{"import_environment", "texthash:/etc/postfix/v", "texthash:/etc/postfix/sender"},
		},
		"ldap without aliases": {
			d:      ldapSource(ldapOnly),
			want:   []string{aliasDomains, "\nvirtual_mailbox_maps = ldap:"},
			absent: []string{"virtual_alias_maps"},
		},
		"pgsql": {
			d:    sqlSource(nil),
			want: []string{aliasDomains, "\nvirtual_alias_maps = pgsql:/etc/postfix-secret/pgsql-alias.cf\n", "\nvirtual_mailbox_maps = pgsql:/etc/postfix-secret/pgsql-mailbox.cf\n", "PGSSLMODE PGSSLROOTCERT PGCONNECT_TIMEOUT\n"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, tc.d))
			if err != nil {
				t.Fatal(err)
			}
			got := res.ConfigData["main.cf"]
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("main.cf misses %q", strings.TrimSpace(w))
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("main.cf holds %q", a)
				}
			}
		})
	}
}

// The directory tables are only rendered where they are read: the
// recipient tables by a server that receives mail for an authorized
// domain, the sender table by one with submission (TestDirectorySenderTable).
// The fail-closed render has no tables and no secret.
func TestDirectoryOnlyWhereRead(t *testing.T) {
	failClosed := directoryRequest(t, ldapSource(nil))
	failClosed.AuthorizedDomains = nil
	for name, req := range map[string]*render.RenderRequest{"fail closed": failClosed} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(req)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.SecretData) != 0 || len(res.Env) != 0 {
				t.Errorf("secret %v, env %v, want none", sets.KeySet(res.SecretData), res.Env)
			}
			if strings.Contains(res.ConfigData["main.cf"], "ldap:") {
				t.Error("main.cf reads an ldap: table")
			}
		})
	}
}

// A static test user and no directory renders as before: no secret, no
// environment, the texthash table.
func TestNoDirectoryRendersTheTexthash(t *testing.T) {
	res, err := New().Render(testRequest(t, func(s *v1alpha1.MailServer) {
		s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SecretData) != 0 || len(res.Env) != 0 {
		t.Errorf("secret %v, env %v, want none", sets.KeySet(res.SecretData), res.Env)
	}
	if got := res.ConfigData["main.cf"]; !strings.Contains(got, "virtual_mailbox_maps = texthash:/etc/postfix/vmailbox\n") ||
		!strings.Contains(got, "virtual_alias_maps = texthash:/etc/postfix/valias\n") {
		t.Errorf("unexpected recipient maps in main.cf:\n%s", got)
	}
}

func TestDirectoryRefusesUnsafeSettings(t *testing.T) {
	for name, d := range map[string]*render.DirectorySource{
		"newline in the LDAP password":  ldapSource(func(l *directory.LDAPTarget) { l.BindPassword = "pw\nbind_dn = cn=other" }),
		"trailing newline":              ldapSource(func(l *directory.LDAPTarget) { l.BindPassword = "pw\n" }),
		"blank around the LDAP secret":  ldapSource(func(l *directory.LDAPTarget) { l.BindPassword = " pw" }),
		"control character in the base": ldapSource(func(l *directory.LDAPTarget) { l.BaseDN = "dc=a\x00b" }),
		"blank around the base DN":      ldapSource(func(l *directory.LDAPTarget) { l.BaseDN = " dc=example,dc=org" }),
		"blank after the bind DN":       ldapSource(func(l *directory.LDAPTarget) { l.BindDN += " " }),
		"filter in an attribute name":   ldapSource(func(l *directory.LDAPTarget) { l.Attributes.Mail = "mail=*)(|(uid" }),
		"broken active filter":          ldapSource(func(l *directory.LDAPTarget) { l.ActiveFilter = "(a=b))(|(c=d)" }),
		"newline in the SQL password":   sqlSource(func(s *directory.SQLTarget) { s.Password = "pw\nquery = x" }),
		"no settings":                   {},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, d))
			if err == nil {
				t.Fatalf("rendered %v, want an error", res)
			}
			if strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), ldapSecret) {
				t.Errorf("the error holds a secret value: %v", err)
			}
		})
	}
}
