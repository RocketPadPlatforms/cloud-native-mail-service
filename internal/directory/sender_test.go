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

package directory

import (
	"strings"
	"testing"

	"github.com/go-ldap/ldap/v3"
)

const (
	aliasAttribute = "mailAlias"
	usersView      = "cnms_users"
	loopbackHost   = "127.0.0.1"
	publicSchema   = "public"
)

func TestSenderLDAPFilter(t *testing.T) {
	target := LDAPTarget{Attributes: Attributes{Login: DefaultLogin, Mail: DefaultMail, Aliases: aliasAttribute}, ActiveFilter: "(mailEnabled=TRUE)"}
	got, err := senderLDAPFilter(target, "App@Example.org")
	if err != nil {
		t.Fatal(err)
	}
	want := "(&(mailEnabled=TRUE)(|(mail=app@example.org)(mailAlias=app@example.org))(uid=*)(mail=*)(!(uid=.*))(!(uid=*/*))(!(uid=sa.*)))"
	if got != want {
		t.Errorf("filter = %q, want %q", got, want)
	}

	// The address is data: filter syntax in it is escaped, not interpreted.
	got, err = senderLDAPFilter(target, "a*)(uid=*@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "a*)") || !strings.Contains(got, `a\2a\29\28uid=\2a@example.org`) {
		t.Errorf("the address is not escaped: %q", got)
	}
	if _, err := ldap.CompileFilter(got); err != nil {
		t.Errorf("the filter does not compile: %v", err)
	}
}

func TestSenderSQL(t *testing.T) {
	users := Relation{Schema: publicSchema, Name: usersView}
	aliases := Relation{Schema: publicSchema, Name: "cnms_aliases"}

	plain := senderSQL(SQLTarget{Users: users})
	if want := `SELECT 1 FROM "public"."cnms_users" WHERE lower(mail) = $1 AND ` + SQLLoginGuard + " LIMIT 1"; plain != want {
		t.Errorf("query = %q, want %q", plain, want)
	}
	withAliases := senderSQL(SQLTarget{Users: users, Aliases: &aliases})
	if !strings.Contains(withAliases, `UNION SELECT 1 FROM "public"."cnms_aliases" a JOIN "public"."cnms_users" u`) ||
		!strings.Contains(withAliases, "lower(a.alias) = $1") || !strings.HasSuffix(withAliases, " LIMIT 1") {
		t.Errorf("query = %q", withAliases)
	}
	if strings.Contains(withAliases, "@") {
		t.Errorf("the address is in the text of the query: %q", withAliases)
	}
}

// The query of the service account lookup and the one of the Postfix table
// are one builder: only the selected column and the key differ.
func TestSQLSenderQueryIsOneBuilder(t *testing.T) {
	users := Relation{Schema: publicSchema, Name: usersView}
	aliases := Relation{Schema: publicSchema, Name: "cnms_aliases"}
	target := SQLTarget{Users: users, Aliases: &aliases}
	postfix := SQLSenderQuery(target, true, "'%s'")
	if !strings.HasPrefix(postfix, "SELECT login FROM") || !strings.Contains(postfix, "UNION SELECT u.login FROM") || strings.Contains(postfix, "$1") {
		t.Errorf("postfix query = %q", postfix)
	}
	lookup := senderSQL(target)
	if !strings.HasPrefix(lookup, "SELECT 1 FROM") || !strings.Contains(lookup, "UNION SELECT 1 FROM") || strings.Contains(lookup, "'%s'") {
		t.Errorf("lookup query = %q", lookup)
	}
	// Past the selected column, the two are the same text.
	rest := func(q, first, second string) string {
		q = strings.Replace(q, "SELECT "+first+" FROM", "SELECT _ FROM", 1)
		q = strings.Replace(q, "UNION SELECT "+second+" FROM", "UNION SELECT _ FROM", 1)
		return strings.NewReplacer("'%s'", "KEY", "$1", "KEY").Replace(q)
	}
	if rest(postfix, "login", "u.login") != rest(strings.TrimSuffix(lookup, " LIMIT 1"), "1", "1") {
		t.Errorf("the queries differ beyond the column and the key:\n%s\n%s", postfix, lookup)
	}
}

// A lookup that cannot reach the directory says so and finds nothing.
func TestOwnsAgainstClosedPorts(t *testing.T) {
	sql := SQLTarget{Host: loopbackHost, Port: closedPort(t), Database: testDB, Username: lookupUser, Password: "x",
		TLSMode: SQLVerifyFull, Users: Relation{Schema: publicSchema, Name: usersView}}
	if owned, got := (NetProber{}).OwnsSQL(t.Context(), sql, "app@example.org"); owned || got != Unreachable {
		t.Errorf("a closed SQL port = %v, %q", owned, got)
	}
	ldapT := LDAPTarget{Host: loopbackHost, Port: closedPort(t), BaseDN: "dc=example,dc=org", TLSMode: LDAPS, Attributes: uidMail}
	if owned, got := (NetProber{}).OwnsLDAP(t.Context(), ldapT, "app@example.org"); owned || got != Unreachable {
		t.Errorf("a closed LDAPS port = %v, %q", owned, got)
	}
}
