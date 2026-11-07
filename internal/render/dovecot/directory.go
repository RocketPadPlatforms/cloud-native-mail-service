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
	"errors"
	"fmt"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The files of the directory secrets, in the Secret that is mounted at
// render.DovecotUsersDir. Dovecot reads a setting from a file with `<`
// (verified on 2.4.5), so no secret value is in the ConfigMap and
// `doveconf -n` still validates the whole structure.
const (
	ldapPasswordFile = "ldap-password"
	sqlPasswordFile  = "sql-password"
	caFile           = "ca.crt"
)

func secretPath(file string) string { return render.DovecotUsersDir + "/" + file }

// quote writes a setting value as a double-quoted string: a value with a
// "#" would be cut off as a comment, and a backslash or a quote would
// be read as an escape.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// renderDirectory renders the passdb and userdb blocks of a directory
// (ADR 0015, section 4.8, point 4) and the secret files they read.
// Login is by login name or by primary address, never by an alias: the
// directory tables have no alias in the lookup. The user the session
// runs as is always the login name, and the home is keyed by it.
func renderDirectory(d *render.DirectorySource) (conf string, secret map[string]string, err error) {
	switch {
	case d.LDAP != nil:
		return renderLDAP(d.LDAP)
	case d.SQL != nil:
		return renderSQL(d.SQL)
	}
	return "", nil, errors.New("directory without settings")
}

func renderLDAP(t *directory.LDAPTarget) (string, map[string]string, error) {
	// Refuses a target Dovecot cannot use safely: tls.mode None would send
	// the password of every login in clear text (passdb bind), and "%{" in
	// a DN would be expanded as a variable.
	if err := t.DovecotProblem(); err != nil {
		return "", nil, err
	}
	filter, err := directory.LookupFilter(t.Attributes, t.ActiveFilter, directory.ByLogin, directory.KeyDovecot)
	if err != nil {
		return "", nil, err
	}
	secret := map[string]string{}
	var b strings.Builder
	b.WriteString("# User source: the MailDirectory (ADR 0015), LDAP. The password and the\n")
	b.WriteString("# CA bundle are read from files of the users Secret.\n")
	scheme := "ldap"
	if t.TLSMode == directory.LDAPS {
		scheme = "ldaps"
	}
	fmt.Fprintf(&b, "ldap_uris = %s://%s:%d\n", scheme, t.Host, directory.LDAPPort(*t))
	if t.TLSMode == directory.LDAPStartTLS {
		b.WriteString("ldap_starttls = yes\n")
	}
	// A lone "%" is taken literally in ldap_base and ldap_auth_dn (a
	// doubled one is not: verified on 2.4.5); only "%{" starts a variable.
	fmt.Fprintf(&b, "ldap_base = %s\n", quote(t.BaseDN))
	if t.BindDN != "" {
		fmt.Fprintf(&b, "ldap_auth_dn = %s\n", quote(t.BindDN))
		fmt.Fprintf(&b, "ldap_auth_dn_password = <%s\n", secretPath(ldapPasswordFile))
		secret[ldapPasswordFile] = t.BindPassword
	}
	if len(t.CA) > 0 && t.TLSMode != directory.LDAPNoTLS {
		fmt.Fprintf(&b, "ssl_client_ca_file = %s\n", secretPath(caFile))
		secret[caFile] = string(t.CA)
	}
	// The directory verifies the password (an authentication bind), so
	// no hash leaves it. The filter finds the entry by login name or
	// primary address; the passdb reports the login name as the user.
	b.WriteString("passdb ldap {\n")
	b.WriteString("  bind = yes\n")
	fmt.Fprintf(&b, "  filter = %s\n", quote(filter))
	b.WriteString("  fields {\n")
	fmt.Fprintf(&b, "    user = %%{ldap:%s}\n", t.Attributes.Login)
	b.WriteString("  }\n")
	b.WriteString("}\n")
	// The userdb has its own LDAP connection: the passdb binds as the
	// user on its connection. The mailbox is keyed by the login name.
	b.WriteString("userdb ldap {\n")
	b.WriteString("  ldap_connection_group = userdb\n")
	fmt.Fprintf(&b, "  filter = %s\n", quote(filter))
	b.WriteString("  fields {\n")
	fmt.Fprintf(&b, "    home = %s/%%{ldap:%s}\n", mailRoot, t.Attributes.Login)
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String(), secret, nil
}

func renderSQL(t *directory.SQLTarget) (string, map[string]string, error) {
	if err := t.SettingsProblem(); err != nil {
		return "", nil, err
	}
	secret := map[string]string{sqlPasswordFile: t.Password}
	var b strings.Builder
	b.WriteString("# User source: the MailDirectory (ADR 0015), PostgreSQL. The password and\n")
	b.WriteString("# the CA bundle are read from files of the users Secret.\n")
	b.WriteString("sql_driver = pgsql\n")
	fmt.Fprintf(&b, "pgsql %s {\n", t.Host)
	b.WriteString("  parameters {\n")
	fmt.Fprintf(&b, "    host = %s\n", t.Host)
	fmt.Fprintf(&b, "    port = %d\n", t.Port)
	fmt.Fprintf(&b, "    user = %s\n", t.Username)
	fmt.Fprintf(&b, "    password = <%s\n", secretPath(sqlPasswordFile))
	fmt.Fprintf(&b, "    dbname = %s\n", t.Database)
	fmt.Fprintf(&b, "    sslmode = %s\n", t.TLSMode)
	switch {
	case t.TLSMode == directory.SQLDisable:
	case len(t.CA) > 0:
		fmt.Fprintf(&b, "    sslrootcert = %s\n", secretPath(caFile))
		secret[caFile] = string(t.CA)
	case t.TLSMode == directory.SQLVerifyFull:
		b.WriteString("    sslrootcert = system\n")
	}
	b.WriteString("  }\n")
	b.WriteString("}\n")
	// Fixed columns, the relation is a quoted identifier of validated
	// fields, and the key is %{user}, which Dovecot quotes for SQL. The
	// login guard is the one of the probe. The comparison with lower()
	// relies on the default auth_username_format = %{user | lower} of 2.4
	// (ADR 0015, verification row 23), which this configuration leaves
	// alone: the key arrives in lower case.
	where := fmt.Sprintf("FROM %s WHERE (lower(login) = '%%{user}' OR lower(mail) = '%%{user}') AND %s",
		t.Users.Quoted(), directory.SQLLoginGuard)
	b.WriteString("passdb sql {\n")
	fmt.Fprintf(&b, "  query = %s\n", quote("SELECT login AS user, password "+where))
	b.WriteString("}\n")
	b.WriteString("userdb sql {\n")
	fmt.Fprintf(&b, "  query = %s\n", quote("SELECT '"+mailRoot+"/' || login AS home "+where))
	b.WriteString("}\n")
	return b.String(), secret, nil
}
