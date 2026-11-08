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
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The keys of the table files in the ConfigMap and the file names under
// /etc/postfix (mounted next to main.cf). All of them are always
// rendered, possibly without a line, so that the set of files is fixed
// (the manifests mount exactly these) and `postmap -s` of the validation
// gate finds each of them.
const (
	vmailboxFile       = "vmailbox"
	valiasFile         = "valias"
	senderLoginFile    = "sender_login"
	recipientDeferFile = "recipient_defer"
	nullSenderFile     = "null_sender"
)

// The names above are the render.PostfixTableFiles: the same list serves
// the validation gate and the mounts of the manifests. The test
// TestTableFilesAreTheRenderedOnes keeps the constants in step with it.

// tablePathTexthash is the texthash map specification of a table file.
func tablePathTexthash(file string) string { return "texthash:/etc/postfix/" + file }

// deferReply is the reply of a deferred address: a temporary failure,
// because a conflict or a suspension is an operator-correctable state
// and a permanent reply would bounce mail for an address that exists
// again after the fix (ADR 0015, section 4.2, point 4).
const deferReply = "450 4.2.1 The address is temporarily not available"

// nullSenderRule refuses the null sender on the submission listener:
// reject_sender_login_mismatch lets MAIL FROM:<> through (ADR 0015,
// section 4.4). The key is the pinned smtpd_null_access_lookup_key.
const nullSenderRule = "<> REJECT The null sender is not allowed on submission"

// renderTable renders one texthash file: a banner, then the lines.
func renderTable(req *render.RenderRequest, what string, lines []string) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Postfix " + what + " -- do not edit.\n")
	b.WriteString("# Source: MailServer " + req.Server.Namespace + "/" + req.Server.Name + "\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

func pairs(ps []render.Pair) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Key + " " + p.Value
	}
	return out
}

// renderTables renders the table files of the accounts (ADR 0015,
// sections 4.3, 4.4 and 4.6). Postfix folds lookup keys to lower case,
// and the plan holds lower-case addresses only. With a directory the
// plan is empty and the files are the empty tables that main.cf does not
// read, except recipient_defer and null_sender, which hold no
// directory data.
//
//   - vmailbox: the canonical addresses (virtual_mailbox_maps). With the
//     table in place the SMTP server rejects unknown recipients of the
//     virtual domains at RCPT time; without it Postfix would accept them
//     and bounce later (backscatter). An empty table rejects everything.
//   - valias: alias to canonical address (virtual_alias_maps). The target
//     is never an alias, so there are no chains.
//   - sender_login: every owned address to the account name
//     (smtpd_sender_login_maps).
//   - recipient_defer: the addresses that answer 450 (check_recipient_access).
//   - null_sender: the null sender rule.
func renderTables(req *render.RenderRequest, plan *render.AccountPlan) map[string]string {
	mailboxes := make([]string, len(plan.Mailboxes))
	for i, a := range plan.Mailboxes {
		mailboxes[i] = a + " OK"
	}
	deferred := make([]string, len(plan.Deferred))
	for i, a := range plan.Deferred {
		deferred[i] = a + " " + deferReply
	}
	return map[string]string{
		vmailboxFile:       renderTable(req, "recipient table (virtual_mailbox_maps)", mailboxes),
		valiasFile:         renderTable(req, "alias table (virtual_alias_maps)", pairs(plan.Aliases)),
		senderLoginFile:    renderTable(req, "sender table (smtpd_sender_login_maps)", pairs(plan.Senders)),
		recipientDeferFile: renderTable(req, "deferred recipients (check_recipient_access)", deferred),
		nullSenderFile:     renderTable(req, "null sender rule (check_sender_access)", []string{nullSenderRule}),
	}
}
