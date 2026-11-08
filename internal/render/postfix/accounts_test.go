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
	"slices"
	"strings"
	"testing"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// accountRequest is a server with mailboxes and submission and these
// accounts: a plain user, one with aliases in two domains, a disabled
// one, one whose primary domain is not authorized (suspended) and one
// that owns an address that lost a conflict.
func accountRequest(t *testing.T) *render.RenderRequest {
	t.Helper()
	req := testRequest(t, func(s *v1alpha1.MailServer) {
		s.Spec.Mailboxes = &v1alpha1.Mailboxes{Enabled: new(true)}
	})
	req.Accounts = []render.Account{
		{Name: "testuser", PasswordHash: "{SHA512-CRYPT}x", Address: "testuser@" + domainA},
		// Upper case is folded: Postfix folds its lookup keys.
		{Name: "alice", PasswordHash: "{SHA512-CRYPT}y", Address: "Alice@" + domainA,
			Aliases: []string{"alice@" + domainB, "Sales@" + domainB}},
		{Name: "bob", PasswordHash: "{SHA512-CRYPT}z", Address: "bob@" + domainA, Disabled: true,
			Aliases: []string{"bobby@" + domainB}},
		{Name: "carol", PasswordHash: "{SHA512-CRYPT}w", Address: "carol@revoked.example",
			Aliases: []string{"carol@" + domainA, "carol@other.example"}},
		{Name: "dave", PasswordHash: "{SHA512-CRYPT}v", Address: "dave@" + domainA,
			Aliases: []string{"shared@" + domainB}},
	}
	req.ConflictedAddresses = []string{"shared@" + domainB}
	return req
}

func TestRenderAccountTables(t *testing.T) {
	res, err := New().Render(accountRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range render.PostfixTableFiles {
		checkGolden(t, "accounts."+f, res.ConfigData[f])
	}
	checkGolden(t, "accounts.maincf", res.ConfigData["main.cf"])
	checkGolden(t, "accounts.mastercf", res.ConfigData["master.cf"])
}

func lines(s string) map[string]bool {
	out := map[string]bool{}
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out[l] = true
		}
	}
	return out
}

func TestAccountTablesContent(t *testing.T) {
	res, err := New().Render(accountRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		file    string
		present []string
		absent  []string
	}{{
		file: vmailboxFile,
		// Canonical addresses only; a disabled account keeps receiving;
		// a suspended one does not (its domain is not served).
		present: []string{"alice@a.example OK", "bob@a.example OK", "testuser@a.example OK", "dave@a.example OK"},
		absent:  []string{"alice@b.example OK", "sales@b.example OK", "carol@revoked.example OK", "shared@b.example OK"},
	}, {
		file:    valiasFile,
		present: []string{"alice@b.example alice@a.example", "sales@b.example alice@a.example", "bobby@b.example bob@a.example"},
		// The alias of a suspended account is deferred, not rewritten
		// (its canonical domain is not virtual: the rewritten mail would
		// go out to the real MX); a lost address is served by nobody; an
		// alias in a domain the server does not serve is dropped.
		absent: []string{"carol@a.example carol@revoked.example", "shared@b.example dave@a.example", "carol@other.example carol@revoked.example"},
	}, {
		file: senderLoginFile,
		// Every owned address maps to the account name, in any case; the
		// disabled account owns none.
		present: []string{"alice@a.example alice", "alice@b.example alice", "sales@b.example alice", "testuser@a.example testuser", "dave@a.example dave"},
		absent:  []string{"bob@a.example bob", "bobby@b.example bob", "carol@revoked.example carol", "carol@a.example carol", "shared@b.example dave"},
	}, {
		file:    recipientDeferFile,
		present: []string{"carol@a.example " + deferReply, "shared@b.example " + deferReply},
		absent:  []string{"carol@other.example " + deferReply, "carol@revoked.example " + deferReply},
	}, {
		file:    nullSenderFile,
		present: []string{nullSenderRule},
	}}
	for _, tt := range tests {
		got := lines(res.ConfigData[tt.file])
		for _, p := range tt.present {
			if !got[p] {
				t.Errorf("%s misses %q:\n%s", tt.file, p, res.ConfigData[tt.file])
			}
		}
		for _, a := range tt.absent {
			if got[a] {
				t.Errorf("%s has %q:\n%s", tt.file, a, res.ConfigData[tt.file])
			}
		}
	}
	if strings.Contains(strings.Join(mapValues(res.ConfigData), "\n"), "ALICE") {
		t.Error("an upper-case address reached a table")
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestAccountMainAndMasterCF(t *testing.T) {
	res, err := New().Render(accountRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	mainCF, masterCF := res.ConfigData["main.cf"], res.ConfigData["master.cf"]
	for _, want := range []string{
		"\nvirtual_alias_domains =\n",
		"\nvirtual_alias_maps = texthash:/etc/postfix/valias\n",
		"\nvirtual_mailbox_maps = texthash:/etc/postfix/vmailbox\n",
		"\nsmtpd_sender_login_maps = texthash:/etc/postfix/sender_login\n",
		"\nsmtpd_recipient_restrictions = check_recipient_access texthash:/etc/postfix/recipient_defer, permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination\n",
		// Port 25 keeps its sender restrictions: other servers send
		// with any sender.
		"\nsmtpd_sender_restrictions = reject_non_fqdn_sender\n",
	} {
		if !strings.Contains(mainCF, want) {
			t.Errorf("main.cf misses %q", strings.TrimSpace(want))
		}
	}
	// The braces form: a value with blanks without it fails at the start
	// of the service, and `postfix check` does not notice it.
	want := "  -o { smtpd_sender_restrictions = check_sender_access texthash:/etc/postfix/null_sender, reject_non_fqdn_sender, reject_sender_login_mismatch }\n"
	submission := masterCF[strings.Index(masterCF, "587 "):strings.Index(masterCF, "pickup")]
	if !strings.Contains(submission, want) {
		t.Errorf("the submission listener has no strict sender binding:\n%s", submission)
	}
	if strings.Count(masterCF, "reject_sender_login_mismatch") != 1 {
		t.Error("the sender binding must be on the submission listener only")
	}
	// soft_bounce on the lmtp client only, as the line after its entry.
	if !strings.Contains(masterCF, "lmtp      unix  -       -       n       -       -       lmtp\n  -o soft_bounce=yes\n") ||
		strings.Count(masterCF, "soft_bounce") != 1 {
		t.Errorf("master.cf must set soft_bounce on the lmtp service alone:\n%s", masterCF)
	}
	if strings.Contains(mainCF, "soft_bounce") {
		t.Error("soft_bounce must not be global: the SMTP server must keep rejecting unknown recipients")
	}
}

// A server without submission has no sender binding to do, and a
// send-only one no recipient restriction for deferred addresses.
func TestSenderBindingFollowsSubmission(t *testing.T) {
	req := accountRequest(t)
	req.Server.Spec.Submission = &v1alpha1.Submission{Enabled: new(false)}
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.ConfigData["main.cf"], "smtpd_sender_login_maps") || strings.Contains(res.ConfigData["master.cf"], "reject_sender_login_mismatch") {
		t.Error("submission is off but a sender binding is rendered")
	}

	req = accountRequest(t)
	req.Server.Spec.Mailboxes = nil
	res, err = New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	mainCF := res.ConfigData["main.cf"]
	if strings.Contains(mainCF, "recipient_defer") || strings.Contains(mainCF, "virtual_alias") {
		t.Errorf("a send-only server has recipient tables:\n%s", mainCF)
	}
	if !strings.Contains(mainCF, "smtpd_sender_login_maps = texthash:/etc/postfix/sender_login\n") {
		t.Error("a send-only server with submission needs the sender binding")
	}
}

// Every table file is rendered, in every mode, and the validation gate
// parses each of them: a table that the gate skips is not validated.
func TestEveryTableFileIsRenderedAndValidated(t *testing.T) {
	for name, mutate := range map[string]func(*render.RenderRequest){
		"accounts":    func(*render.RenderRequest) {},
		"no accounts": func(r *render.RenderRequest) { r.Accounts = nil },
		"fail closed": func(r *render.RenderRequest) { r.AuthorizedDomains = nil },
		"directory":   func(r *render.RenderRequest) { r.Accounts, r.Directory = nil, ldapSource(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			req := accountRequest(t)
			mutate(req)
			res, err := New().Render(req)
			if err != nil {
				t.Fatal(err)
			}
			script := res.ValidateCommand[2]
			for _, f := range render.PostfixTableFiles {
				if _, ok := res.ConfigData[f]; !ok {
					t.Errorf("no %s in the config data", f)
				}
				if !strings.Contains(script, f) {
					t.Errorf("the validation gate does not parse %s", f)
				}
			}
		})
	}
}

// The tables of the accounts hold nothing in the fail-closed render.
func TestFailClosedRenderHasEmptyAccountTables(t *testing.T) {
	req := accountRequest(t)
	req.AuthorizedDomains = nil
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{vmailboxFile, valiasFile, senderLoginFile, recipientDeferFile} {
		if got := lines(res.ConfigData[f]); len(got) != 0 {
			t.Errorf("%s has lines without an authorized domain: %v", f, got)
		}
	}
}

// The values of the accounts reach files that Postfix parses by blanks
// and lines. The renderer refuses what would split a line or start a
// new one, and an address that two accounts claim.
func TestRenderRefusesUnsafeAddresses(t *testing.T) {
	for name, a := range map[string][]render.Account{
		"blank in an alias":      {{Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a b@" + domainB}}},
		"newline in an alias":    {{Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"x@" + domainB + "\nevil@" + domainB + " OK"}}},
		"newline in the primary": {{Name: "u", PasswordHash: hashA, Address: "u@" + domainA + "\nevil@" + domainB}},
		"comment in an alias":    {{Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"#x@" + domainB}}},
		"plus in an alias":       {{Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a+b@" + domainB}}},
		"two dots in an alias":   {{Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a..b@" + domainB}}},
		"alias is a primary": {
			{Name: "u", PasswordHash: hashA, Address: "u@" + domainA},
			{Name: "v", PasswordHash: hashA, Address: "v@" + domainA, Aliases: []string{"u@" + domainA}},
		},
		"alias claimed twice": {
			{Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"s@" + domainB}},
			{Name: "v", PasswordHash: hashA, Address: "v@" + domainA, Aliases: []string{"S@" + domainB}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := accountRequest(t)
			req.Accounts, req.ConflictedAddresses = a, nil
			if _, err := New().Render(req); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	req := accountRequest(t)
	req.ConflictedAddresses = []string{"x y@" + domainA}
	if _, err := New().Render(req); err == nil {
		t.Fatal("a conflicted address with a blank must be refused")
	}
}

// A server with a directory binds the sender to the directory too: the
// table maps an address (the mail attribute or an alias) to the login.
// A server without mailboxes needs the sender table alone, and one
// without submission the recipient tables alone.
func TestDirectorySenderTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source func() *render.DirectorySource
		file   string
		scheme string
	}{
		{srcLDAP, func() *render.DirectorySource { return ldapSource(nil) }, ldapSenderFile, "ldap:"},
		{srcPgSQL, func() *render.DirectorySource { return sqlSource(nil) }, pgsqlSenderFile, "pgsql:"},
	} {
		t.Run(tc.name+" with mailboxes and submission", func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, tc.source()))
			if err != nil {
				t.Fatal(err)
			}
			want := "\nsmtpd_sender_login_maps = " + tc.scheme + "/etc/postfix-secret/" + tc.file + "\n"
			if !strings.Contains(res.ConfigData["main.cf"], want) {
				t.Errorf("main.cf misses %q", strings.TrimSpace(want))
			}
			if _, ok := res.SecretData[tc.file]; !ok {
				t.Errorf("no %s in the Secret", tc.file)
			}
		})
		t.Run(tc.name+" send-only", func(t *testing.T) {
			req := directoryRequest(t, tc.source())
			req.Server.Spec.Mailboxes = nil
			res, err := New().Render(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := res.SecretData[tc.file]; !ok {
				t.Errorf("a send-only server with submission has no %s", tc.file)
			}
			for f := range res.SecretData {
				if strings.Contains(f, "mailbox") || strings.Contains(f, "alias") {
					t.Errorf("a send-only server carries the recipient table %s", f)
				}
			}
			if tc.name == srcPgSQL && !strings.Contains(res.ConfigData["main.cf"], "import_environment") {
				t.Error("the pgsql sender table needs import_environment")
			}
		})
		t.Run(tc.name+" without submission", func(t *testing.T) {
			req := directoryRequest(t, tc.source())
			req.Server.Spec.Submission = &v1alpha1.Submission{Enabled: new(false)}
			res, err := New().Render(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := res.SecretData[tc.file]; ok {
				t.Errorf("%s is rendered without submission", tc.file)
			}
			if strings.Contains(res.ConfigData["main.cf"], "smtpd_sender_login_maps") {
				t.Error("main.cf has a sender table without submission")
			}
		})
	}
}

// The sender lookup of the directory finds the owner by the mail
// attribute or an alias and returns the login; it has the guards of the
// other lookups, and the key is only the placeholder of Postfix.
func TestDirectorySenderTableContent(t *testing.T) {
	res, err := New().Render(directoryRequest(t, ldapSource(nil)))
	if err != nil {
		t.Fatal(err)
	}
	ldap := res.SecretData[ldapSenderFile]
	for _, want := range []string{
		"query_filter = (&(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))(|(mail=%s)(mailAlias=%s))(uid=*)(mail=*)(!(uid=.*))(!(uid=*/*))(!(uid=sa.*)))\n",
		"result_attribute = uid\n",
	} {
		if !strings.Contains(ldap, want) {
			t.Errorf("ldap sender table misses %q:\n%s", strings.TrimSpace(want), ldap)
		}
	}
	// Without an alias attribute only the mail attribute is matched.
	res, err = New().Render(directoryRequest(t, ldapSource(func(l *directory.LDAPTarget) { l.Attributes.Aliases = "" })))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.SecretData[ldapSenderFile]; !strings.Contains(got, "(mail=%s)(uid=*)") || strings.Contains(got, "mailAlias") {
		t.Errorf("sender table without aliases:\n%s", got)
	}

	res, err = New().Render(directoryRequest(t, sqlSource(nil)))
	if err != nil {
		t.Fatal(err)
	}
	sql := res.SecretData[pgsqlSenderFile]
	for _, want := range []string{
		`SELECT login FROM "public"."cnms_users" WHERE lower(mail) = '%s' AND login ~ `,
		`UNION SELECT u.login FROM "public"."cnms_aliases" a JOIN "public"."cnms_users" u ON lower(u.mail) = lower(a.target) WHERE lower(a.alias) = '%s' AND u.login ~ `,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("pgsql sender table misses %q:\n%s", want, sql)
		}
	}
	res, err = New().Render(directoryRequest(t, sqlSource(func(s *directory.SQLTarget) { s.Aliases = nil })))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.SecretData[pgsqlSenderFile]; strings.Contains(got, "UNION") {
		t.Errorf("sender table without aliases has a UNION:\n%s", got)
	}
}

const hashA = "{X}a"

// The constants of the table files and render.PostfixTableFiles, which the
// manifests mount and the validation gate parses, are one list.
func TestTableFilesAreTheRenderedOnes(t *testing.T) {
	res, err := New().Render(accountRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	var rendered []string
	for f := range res.ConfigData {
		if f != "main.cf" && f != "master.cf" {
			rendered = append(rendered, f)
		}
	}
	slices.Sort(rendered)
	if want := slices.Sorted(slices.Values(render.PostfixTableFiles)); !slices.Equal(rendered, want) {
		t.Errorf("rendered table files = %v, render.PostfixTableFiles = %v", rendered, want)
	}
	for _, f := range []string{vmailboxFile, valiasFile, senderLoginFile, recipientDeferFile, nullSenderFile} {
		if !slices.Contains(render.PostfixTableFiles, f) {
			t.Errorf("%s is not in render.PostfixTableFiles", f)
		}
	}
}

// The sender table answers only for the authorized domains, sorted (the
// domain parameter of ldap_table(5) and pgsql_table(5) skips the lookup of
// any other key): an address the directory knows in a domain the server
// does not serve is owned by nobody. The recipient tables have no such
// list, virtual_mailbox_domains does that job.
func TestDirectorySenderTableIsLimitedToTheAuthorizedDomains(t *testing.T) {
	for name, tc := range map[string]struct {
		source        *render.DirectorySource
		sender        string
		recipientFile []string
	}{
		srcLDAP:  {ldapSource(nil), ldapSenderFile, []string{ldapMailboxFile, ldapAliasFile}},
		srcPgSQL: {sqlSource(nil), pgsqlSenderFile, []string{pgsqlMailboxFile, pgsqlAliasFile}},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := New().Render(directoryRequest(t, tc.source))
			if err != nil {
				t.Fatal(err)
			}
			if got := res.SecretData[tc.sender]; !strings.Contains(got, "\ndomain = a.example, b.example\n") {
				t.Errorf("%s has no domain list:\n%s", tc.sender, got)
			}
			for _, f := range tc.recipientFile {
				if strings.Contains(res.SecretData[f], "domain =") {
					t.Errorf("%s has a domain list", f)
				}
			}
		})
	}
	for _, bad := range []string{"/etc/passwd", "a.example, b.example", "texthash:/x", "A B", ""} {
		req := directoryRequest(t, ldapSource(nil))
		req.AuthorizedDomains = []string{bad}
		if _, err := New().Render(req); err == nil {
			t.Errorf("the authorized domain %q was accepted", bad)
		}
	}
}
