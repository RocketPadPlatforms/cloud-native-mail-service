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
	"strings"
	"testing"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// accounts is the account set of the golden files: a plain user, one with
// aliases in two domains, a disabled one, and one whose primary domain
// is not authorized (suspended).
func accounts() []render.Account {
	return []render.Account{
		{Name: "testuser", PasswordHash: "{SHA512-CRYPT}x", Address: "testuser@" + domainA},
		{Name: "alice", PasswordHash: "{SHA512-CRYPT}y", Address: "alice@" + domainA,
			Aliases: []string{"alice@" + domainB, "sales@" + domainB}},
		{Name: "bob", PasswordHash: "{SHA512-CRYPT}z", Address: "bob@" + domainA, Disabled: true,
			Aliases: []string{"bobby@" + domainB}},
		{Name: "carol", PasswordHash: "{SHA512-CRYPT}w", Address: "carol@revoked.example",
			Aliases: []string{"carol@" + domainA}},
	}
}

func TestRenderPassdbAndUserdb(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		res, err := New().Render(testRequest(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		checkGolden(t, "default.passdb", res.LiveData[passdbFile])
		checkGolden(t, "default.userdb", res.LiveData[userdbFile])
	})
	t.Run("aliases, disabled and suspended accounts", func(t *testing.T) {
		req := testRequest(t, nil)
		req.Accounts = accounts()
		res, err := New().Render(req)
		if err != nil {
			t.Fatal(err)
		}
		checkGolden(t, "accounts.passdb", res.LiveData[passdbFile])
		checkGolden(t, "accounts.userdb", res.LiveData[userdbFile])
	})
}

// A disabled and a suspended account cannot log in (no passdb line) and
// still receive mail (userdb lines): ADR 0015, section 3, point 5. An
// alias is no login in either file.
func TestPassdbAndUserdbDiffer(t *testing.T) {
	req := testRequest(t, nil)
	req.Accounts = accounts()
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	passdb, userdb := res.LiveData[passdbFile], res.LiveData[userdbFile]
	for _, in := range []string{"alice@a.example:", "alice:", "testuser:"} {
		if !strings.Contains("\n"+passdb, "\n"+in) {
			t.Errorf("passdb misses a line for %q", in)
		}
	}
	for _, out := range []string{"bob:", "bob@a.example:", "carol:", "carol@revoked.example:"} {
		if strings.Contains("\n"+passdb, "\n"+out) {
			t.Errorf("passdb has a line for %q: a disabled or suspended account must not log in", out)
		}
		if !strings.Contains("\n"+userdb, "\n"+out) {
			t.Errorf("userdb misses a line for %q: it must keep receiving", out)
		}
	}
	for _, alias := range []string{"sales@b.example", "bobby@b.example", "alice@b.example"} {
		if strings.Contains(passdb, alias) || strings.Contains(userdb, alias) {
			t.Errorf("alias %s is in a Dovecot file: aliases are neither logins nor delivery addresses", alias)
		}
	}
	// Both login forms report the account name as the user.
	if !strings.Contains(passdb, "\nalice@a.example:{SHA512-CRYPT}y::::::user=alice\n") ||
		!strings.Contains(passdb, "\nalice:{SHA512-CRYPT}y::::::user=alice\n") {
		t.Errorf("passdb lines have no user= field:\n%s", passdb)
	}
	if strings.Contains(userdb, "SHA512") {
		t.Error("the userdb carries a password hash")
	}
}

// Without an authorized domain nothing is served: no login and no user,
// the fail-closed render (ADR 0015, section 4.6).
func TestNoAuthorizedDomainsRendersNoUsers(t *testing.T) {
	req := testRequest(t, nil)
	req.Accounts, req.AuthorizedDomains = accounts(), nil
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{passdbFile, userdbFile} {
		for line := range strings.Lines(res.LiveData[f]) {
			if !strings.HasPrefix(line, "#") && strings.TrimSpace(line) != "" {
				t.Errorf("%s has a line without an authorized domain: %q", f, line)
			}
		}
	}
}

// The values of an account reach a file that Dovecot parses by colons
// and lines; the renderer refuses what the API would not let through.
func TestRenderRefusesUnsafeAccounts(t *testing.T) {
	for name, a := range map[string]render.Account{
		"colon in the hash":    {Name: "u", PasswordHash: "{SHA512-CRYPT}a:b", Address: "u@" + domainA},
		"newline in the hash":  {Name: "u", PasswordHash: "{SHA512-CRYPT}a\nb@x:", Address: "u@" + domainA},
		"slash in the name":    {Name: "a/../b", PasswordHash: hashA, Address: "u@" + domainA},
		"dot name":             {Name: ".hidden", PasswordHash: hashA, Address: "u@" + domainA},
		"blank in an alias":    {Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a b@" + domainB}},
		"plus in an alias":     {Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a+b@" + domainB}},
		"newline in an alias":  {Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a\nevil@" + domainB}},
		"alias without domain": {Name: "u", PasswordHash: hashA, Address: "u@" + domainA, Aliases: []string{"a"}},
	} {
		t.Run(name, func(t *testing.T) {
			req := testRequest(t, nil)
			req.Accounts = []render.Account{a}
			if _, err := New().Render(req); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// The passwd-file passdb checks a hash in an auth worker, and the workers
// are limited (ADR 0015, section 3, point 4). The setting names were
// verified with doveconf -n on 2.4.5; a directory renders neither.
func TestAccountsLimitTheAuthWorkers(t *testing.T) {
	res, err := New().Render(testRequest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	conf := res.ConfigData["dovecot.conf"]
	for _, want := range []string{
		"  passwd_file_path = /etc/dovecot-users/passdb\n",
		"  passdb_use_worker = yes\n",
		"\nservice auth-worker {\n  process_limit = 4\n}\n",
		"  passwd_file_path = /etc/dovecot-users/userdb\n",
	} {
		if !strings.Contains("\n"+conf, want) {
			t.Errorf("dovecot.conf misses %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(conf, "/etc/dovecot-users/users") {
		t.Error("the single users file is gone")
	}
}

const hashA = "{SHA512-CRYPT}a"

// A directory checks the password itself (a bind, or a query), but the logins
// of the service accounts are lines of the passwd-file, which is asked first
// (ADR 0015, section 4.9, point 2): the render has that passdb before the
// directory's, no userdb of the file (a service account has no mailbox), and
// the worker limit.
func TestDirectoryAsksThePasswdFileFirst(t *testing.T) {
	for name, source := range map[string]*render.DirectorySource{"ldap": ldapSource(nil), "sql": sqlSource(nil)} {
		res, err := New().Render(directoryRequest(t, source, true))
		if err != nil {
			t.Fatal(err)
		}
		conf := res.ConfigData["dovecot.conf"]
		file := strings.Index(conf, "passdb passwd-file {\n  passwd_file_path = /etc/dovecot-users/passdb\n  passdb_use_worker = yes\n}\n")
		dir := strings.Index(conf, "passdb "+name)
		if file < 0 || dir < 0 || file > dir {
			t.Errorf("%s: the passwd-file passdb is not before the directory's (%d, %d):\n%s", name, file, dir, conf)
		}
		if strings.Contains(conf, "userdb passwd-file") || !strings.Contains(conf, "\nservice auth-worker {\n  process_limit = 4\n}\n") {
			t.Errorf("%s: a userdb of the file, or no worker limit:\n%s", name, conf)
		}
	}
}

// A service account is a login by its name alone, in the passdb file, and has no
// userdb line: nothing is delivered to it.
func TestServiceAccountIsALoginByNameWithoutUserdb(t *testing.T) {
	const login = "sa.0123456789abcdef"
	req := testRequest(t, nil)
	req.Accounts = []render.Account{{
		Name: login, PasswordHash: hashA, Address: "app@a.example", SendOnly: true,
		Verdict: addresses.UserResult{Name: login, Rows: []addresses.Row{{
			Address: "app@a.example", Kind: addresses.KindPrimary, Served: true, Reason: addresses.ReasonServed, MaySend: true,
		}}},
	}}
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	passdb := res.LiveData[passdbFile]
	if want := login + ":" + hashA + "::::::user=" + login + "\n"; !strings.Contains(passdb, want) {
		t.Errorf("passdb misses %q:\n%s", want, passdb)
	}
	if strings.Contains(passdb, "app@a.example") {
		t.Errorf("a service account logs in with its address:\n%s", passdb)
	}
	if userdb := res.LiveData[userdbFile]; strings.Contains(userdb, login) || strings.Contains(userdb, "app@a.example") {
		t.Errorf("a service account is in the userdb:\n%s", userdb)
	}
	// ParseLive reads it back as a login with its hash, and no address.
	live := ParseLive(passdb, res.LiveData[userdbFile])
	if len(live) != 1 || live[0].Name != login || live[0].Credential != hashA || live[0].Primary != "" || !live[0].CanLogin() {
		t.Errorf("ParseLive = %+v", live)
	}
}

// ParseLive reads the live files back for the revocation-only rule of a
// blocked revision (ADR 0015, section 4.7): what the renderers write, the
// parser returns, with a disabled or suspended account (only in the userdb)
// marked as unable to log in.
func TestParseLiveReadsWhatTheRenderersWrite(t *testing.T) {
	req := testRequest(t, nil)
	req.Accounts = accounts()
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	got := ParseLive(res.LiveData[passdbFile], res.LiveData[userdbFile])
	byName := map[string]render.Account{}
	for _, a := range req.Accounts {
		byName[a.Name] = a
	}
	want := map[string]addresses.Entry{}
	for name, a := range byName {
		// Disabled and suspended accounts are only in the userdb.
		w := addresses.Entry{Name: name, Credential: a.PasswordHash, Primary: a.Address}
		if a.Disabled || strings.HasSuffix(a.Address, "@revoked.example") {
			w.Disabled, w.Credential = true, ""
		}
		want[name] = w
	}
	if len(got) != len(want) {
		t.Fatalf("ParseLive() = %+v, want %d accounts", got, len(want))
	}
	for _, e := range got {
		w, ok := want[e.Name]
		if !ok || e.Credential != w.Credential || e.Primary != w.Primary || e.Disabled != w.Disabled || e.CanLogin() == w.Disabled {
			t.Errorf("ParseLive() has %+v, want %+v", e, w)
		}
	}
	if len(ParseLive("# nothing\n", "garbage\n:::\n")) != 0 {
		t.Error("lines that are not of the form of the renderers are ignored")
	}
}

// RenderLogins is the function the passdb of a revision is rendered with: the
// lines of a blocked revision are written the same way.
func TestRenderLoginsIsTheFormOfThePassdb(t *testing.T) {
	req := testRequest(t, nil)
	req.Accounts = accounts()
	res, err := New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	var logins []Login
	for _, e := range ParseLive(res.LiveData[passdbFile], res.LiveData[userdbFile]) {
		if e.CanLogin() {
			logins = append(logins, Login{Address: e.Primary, Name: e.Name, Hash: e.Credential})
		}
	}
	if got := RenderLogins(req.Server, logins); got != res.LiveData[passdbFile] {
		t.Errorf("RenderLogins() =\n%s\nwant\n%s", got, res.LiveData[passdbFile])
	}
}
