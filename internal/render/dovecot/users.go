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
	"strings"

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
	var b strings.Builder
	b.WriteString("# CNMS-rendered Dovecot passdb (logins) -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0015)\n\n", req.Server.Namespace, req.Server.Name)
	b.WriteString("# Format: login:password::::::user=<account name>\n")
	for _, u := range plan.Logins {
		fmt.Fprintf(&b, "%s:%s::::::user=%s\n", u.Address, u.PasswordHash, u.Name)
		fmt.Fprintf(&b, "%s:%s::::::user=%s\n", u.Name, u.PasswordHash, u.Name)
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
