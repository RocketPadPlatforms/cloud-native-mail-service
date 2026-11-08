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
	"fmt"
	"path"
	"slices"
	"strings"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The two files of the accounts, keys of the one Dovecot Secret (ADR
// 0015, section 3, point 5, and section 4.7): who may log in (passdb) and
// where the mail goes (userdb). Both change in one Secret update.
const (
	passdbFile = "passdb"
	userdbFile = "userdb"
)

// renderPassdb renders the passwd-file of the logins (passdbFile): the
// account name and the primary address are the two login forms, an
// alias is none (ADR 0015, section 4.5). The extra field user= makes
// the authenticated user the account name for both forms, so Postfix
// sees one SASL name per account and sender_login needs one owner. An
// account that is disabled or suspended has no line: no login, in IMAP
// and in SASL. The fields are
// user:password:uid:gid:gecos:home:shell:extra; the uid, gid and home of
// the userdb are not repeated here.
func renderPassdb(req *render.RenderRequest, plan *render.AccountPlan) string {
	logins := make([]Login, 0, len(plan.Logins))
	for _, u := range plan.Logins {
		l := Login{Address: u.Address, Name: u.Name, Hash: u.PasswordHash}
		if u.SendOnly {
			l.Address = ""
		}
		logins = append(logins, l)
	}
	return RenderLogins(req.Server, logins)
}

// Login is one account of the passdb file.
type Login struct{ Address, Name, Hash string }

// RenderLogins renders the passdb file for the given logins. The MailServer
// controller uses it for the live Secret of a blocked revision, where only the
// lines that are already in the file stay (ADR 0015, section 4.7); the
// revision itself goes through renderPassdb and the same function.
func RenderLogins(server *cnmsv1alpha1.MailServer, logins []Login) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Dovecot passdb (logins) -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0015)\n\n", server.Namespace, server.Name)
	b.WriteString("# Format: login:password::::::user=<account name>\n")
	for _, u := range logins {
		// A service account has no address to log in with: its login is
		// the name alone (ADR 0015, section 4.9).
		if u.Address != "" {
			fmt.Fprintf(&b, "%s:%s::::::user=%s\n", u.Address, u.Hash, u.Name)
		}
		fmt.Fprintf(&b, "%s:%s::::::user=%s\n", u.Name, u.Hash, u.Name)
	}
	return b.String()
}

// renderUserdb renders the passwd-file of the mailboxes (userdbFile).
// Dovecot 2.4 looks a user up by the full recipient address for the
// LMTP delivery (Postfix hands over the canonical address, never an
// alias) and by the account name after a login, with no local-part
// fallback, so each account is listed twice, with the address and with
// the name. Every account is in it, a disabled or suspended one too:
// the mail that Postfix accepted is delivered, not bounced (ADR 0015,
// section 3, point 5). The home directory is keyed by the name and lives
// on the mail volume (ADR 0004). There is no password field.
func renderUserdb(req *render.RenderRequest, plan *render.AccountPlan) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Dovecot userdb (mailboxes) -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0015)\n\n", req.Server.Namespace, req.Server.Name)
	b.WriteString("# Format: user::uid:gid::home:\n")
	for _, u := range plan.Users {
		home := mailRoot + "/" + u.Name
		fmt.Fprintf(&b, "%s::%d:%d::%s:\n", u.Address, vmailUID, vmailGID, home)
		fmt.Fprintf(&b, "%s::%d:%d::%s:\n", u.Name, vmailUID, vmailGID, home)
	}
	return b.String()
}

// ParseLive reads the two live files back into the accounts they carry: the
// state of the running revision for the revocation-only rule of a blocked
// revision (ADR 0015, section 4.7). It is the one place that knows both
// formats, next to the renderers. An account with a passdb line can log in and
// has its hash and primary address from it; an account that is only in the
// userdb (disabled, suspended, or without a credential) is returned as
// Disabled. Lines that are not of the form the renderers write are ignored.
func ParseLive(passdb, userdb string) []addresses.Entry {
	byName := map[string]*addresses.Entry{}
	entry := func(name string) *addresses.Entry {
		if byName[name] == nil {
			byName[name] = &addresses.Entry{Name: name, Disabled: true}
		}
		return byName[name]
	}
	for line := range strings.Lines(userdb) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 6 || !strings.HasPrefix(f[5], mailRoot+"/") {
			continue
		}
		e := entry(path.Base(f[5]))
		if strings.Contains(f[0], "@") {
			e.Primary = f[0]
		}
	}
	for line := range strings.Lines(passdb) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		name, ok := strings.CutPrefix(f[len(f)-1], "user=")
		if len(f) < 7 || !ok || name == "" {
			continue
		}
		e := entry(name)
		e.Disabled, e.Credential = false, f[1]
		if strings.Contains(f[0], "@") {
			e.Primary = f[0]
		}
	}
	out := make([]addresses.Entry, 0, len(byName))
	for _, e := range byName {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b addresses.Entry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// The keys of the live Secret: the names of the two files (ADR 0015, section
// 4.7).
const (
	PassdbKey = passdbFile
	UserdbKey = userdbFile
)
