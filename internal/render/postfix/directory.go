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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The files of the directory tables in the Postfix Secret, and the CA
// bundle next to them (ADR 0015, section 4.8, point 6: the .cf files carry
// the bind password, and Postfix has no file reference for it).
const (
	ldapMailboxFile  = "ldap-mailbox.cf"
	ldapAliasFile    = "ldap-alias.cf"
	pgsqlMailboxFile = "pgsql-mailbox.cf"
	pgsqlAliasFile   = "pgsql-alias.cf"
	caFile           = "ca.crt"

	// lookupTimeout bounds a connection to the directory in seconds.
	// The LDAP table applies it as the timeout of its connection and
	// search; the pgsql table has no timeout setting, so the same value
	// goes to libpq as PGCONNECT_TIMEOUT. Measured on 3.11.7 against an
	// address that drops packets (ADR 0015, verification): the RCPT is
	// answered 451 after about 5 s with LDAP and about 10 s with pgsql,
	// which connects twice; without PGCONNECT_TIMEOUT it was not
	// answered within 300 s.
	lookupTimeout = 5
)

// importEnvironment is the pinned default of import_environment
// (`postconf -d` on 3.11.7) plus the variables libpq reads for the TLS
// settings and the connect timeout of the pgsql tables. Postfix daemons
// see no other variable of the master.
const importEnvironment = "MAIL_CONFIG MAIL_DEBUG MAIL_LOGTAG TZ XAUTHORITY DISPLAY LANG=C " +
	"POSTLOG_SERVICE POSTLOG_HOSTNAME XDG_RUNTIME_DIR PGSSLMODE PGSSLROOTCERT PGCONNECT_TIMEOUT"

// directoryMaps is the Postfix side of a directory: the map specs for
// main.cf, the files of the Secret and the environment of the master.
type directoryMaps struct {
	mailbox string // virtual_mailbox_maps
	alias   string // virtual_alias_maps, empty when the directory has no aliases
	secret  map[string]string
	env     map[string]string
}

// renderDirectory renders the tables of a directory. Every value comes
// from a validated API field, and the lookup key is only ever the
// placeholder of the engine (%s), which Postfix quotes itself.
func renderDirectory(d *render.DirectorySource) (*directoryMaps, error) {
	switch {
	case d.LDAP != nil:
		return renderLDAP(d.LDAP)
	case d.SQL != nil:
		return renderPgSQL(d.SQL)
	}
	return nil, errors.New("directory without settings")
}

func tablePath(file string) string { return render.PostfixSecretDir + "/" + file }

func renderLDAP(t *directory.LDAPTarget) (*directoryMaps, error) {
	if err := t.SettingsProblem(); err != nil {
		return nil, err
	}
	filter := func(by directory.Lookup) (string, error) {
		return directory.LookupFilter(t.Attributes, t.ActiveFilter, by, directory.KeyPostfix)
	}
	table := func(by directory.Lookup, result string) (string, error) {
		q, err := filter(by)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString("# CNMS-rendered Postfix ldap table (ADR 0015) -- do not edit.\n")
		scheme := "ldap"
		if t.TLSMode == directory.LDAPS {
			scheme = "ldaps"
		}
		fmt.Fprintf(&b, "server_host = %s://%s\n", scheme, hostPort(t.Host, directory.LDAPPort(*t)))
		b.WriteString("version = 3\n")
		if t.BindDN != "" {
			b.WriteString("bind = yes\n")
			fmt.Fprintf(&b, "bind_dn = %s\n", t.BindDN)
			fmt.Fprintf(&b, "bind_pw = %s\n", t.BindPassword)
		} else {
			b.WriteString("bind = no\n")
		}
		// A "%" in the search base is a template character of Postfix
		// and must be doubled (verified on 3.11.7); bind_dn and bind_pw
		// are taken literally.
		fmt.Fprintf(&b, "search_base = %s\n", strings.ReplaceAll(t.BaseDN, "%", "%%"))
		b.WriteString("scope = sub\n")
		fmt.Fprintf(&b, "query_filter = %s\n", q)
		fmt.Fprintf(&b, "result_attribute = %s\n", result)
		if t.TLSMode == directory.LDAPStartTLS {
			b.WriteString("start_tls = yes\n")
		} else {
			b.WriteString("start_tls = no\n")
		}
		if t.TLSMode != directory.LDAPNoTLS {
			// Never an unverified connection.
			b.WriteString("tls_require_cert = yes\n")
			if len(t.CA) > 0 {
				fmt.Fprintf(&b, "tls_ca_cert_file = %s\n", tablePath(caFile))
			}
		}
		fmt.Fprintf(&b, "timeout = %d\n", lookupTimeout)
		return b.String(), nil
	}

	m := &directoryMaps{
		mailbox: "ldap:" + tablePath(ldapMailboxFile),
		secret:  map[string]string{},
	}
	var err error
	if m.secret[ldapMailboxFile], err = table(directory.ByMail, t.Attributes.Mail); err != nil {
		return nil, err
	}
	if t.Attributes.Aliases != "" {
		m.alias = "ldap:" + tablePath(ldapAliasFile)
		if m.secret[ldapAliasFile], err = table(directory.ByAlias, t.Attributes.Mail); err != nil {
			return nil, err
		}
	}
	if len(t.CA) > 0 && t.TLSMode != directory.LDAPNoTLS {
		m.secret[caFile] = string(t.CA)
	}
	return m, nil
}

func renderPgSQL(t *directory.SQLTarget) (*directoryMaps, error) {
	if err := t.SettingsProblem(); err != nil {
		return nil, err
	}
	table := func(query string) string {
		var b strings.Builder
		b.WriteString("# CNMS-rendered Postfix pgsql table (ADR 0015) -- do not edit.\n")
		// The non-URI form: a failed connection logs the URI of the
		// URI form with the password in it, and only the host here.
		fmt.Fprintf(&b, "hosts = %s\n", hostPort(t.Host, t.Port))
		fmt.Fprintf(&b, "user = %s\n", t.Username)
		fmt.Fprintf(&b, "password = %s\n", t.Password)
		fmt.Fprintf(&b, "dbname = %s\n", t.Database)
		fmt.Fprintf(&b, "query = %s\n", query)
		return b.String()
	}
	// The relation is a quoted identifier of validated fields, the
	// columns are fixed, and the key is the quoted placeholder. The
	// comparison is case-insensitive: Postfix folds the key to lower case.
	m := &directoryMaps{
		mailbox: "pgsql:" + tablePath(pgsqlMailboxFile),
		secret: map[string]string{
			pgsqlMailboxFile: table(fmt.Sprintf("SELECT mail FROM %s WHERE lower(mail) = '%%s' AND %s",
				t.Users.Quoted(), directory.SQLLoginGuard)),
		},
		env: map[string]string{"PGSSLMODE": t.TLSMode, "PGCONNECT_TIMEOUT": strconv.Itoa(lookupTimeout)},
	}
	if t.Aliases != nil {
		m.alias = "pgsql:" + tablePath(pgsqlAliasFile)
		// The target must be an account that the users query would find:
		// an alias of a removed or guarded login is no recipient (550 at
		// RCPT, like LDAP), not mail that is accepted and bounces later.
		m.secret[pgsqlAliasFile] = table(fmt.Sprintf(
			"SELECT a.target FROM %s a JOIN %s u ON lower(u.mail) = lower(a.target) WHERE lower(a.alias) = '%%s' AND %s",
			t.Aliases.Quoted(), t.Users.Quoted(), directory.SQLLoginGuardOn("u.login")))
	}
	// libpq reads the root certificate from the environment; "system"
	// (libpq 16 and later) verifies against the system roots, like the
	// probe does without a CA.
	switch {
	case t.TLSMode == directory.SQLDisable:
	case len(t.CA) > 0:
		m.secret[caFile] = string(t.CA)
		m.env["PGSSLROOTCERT"] = tablePath(caFile)
	case t.TLSMode == directory.SQLVerifyFull:
		m.env["PGSSLROOTCERT"] = "system"
	}
	return m, nil
}

// hostPort joins a host name (the API allows no IPv6 literal) and a port.
func hostPort(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}
