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

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// vmailboxFile is the key of the recipient table in the ConfigMap and
// the file name under /etc/postfix (mounted next to main.cf).
const vmailboxFile = "vmailbox"

// renderVMailbox renders the valid recipient table behind
// virtual_mailbox_maps. With the table in place the SMTP server
// rejects unknown recipients of the virtual domains at RCPT time;
// without it Postfix would accept them and bounce later after Dovecot
// refuses them over LMTP (backscatter to forged senders).
//
// The table is a texthash file: unlike inline: it may be empty, and
// an empty table rejects every recipient (a server without users has
// no valid mailbox). It is rendered even when mailboxes are disabled
// (and then unused) so that the file is always part of the ConfigMap.
//
// Seam for the user phases: the static test users are the only source
// today; the directory maps and operator-managed users replace this
// table (ADR 0008), with a tempfail on lookup errors.
func renderVMailbox(req *render.RenderRequest) string {
	addrs := make([]string, 0, len(req.StaticUsers))
	for _, u := range req.StaticUsers {
		// Postfix folds lookup keys to lower case.
		addrs = append(addrs, strings.ToLower(u.Mailbox))
	}
	slices.Sort(addrs)
	addrs = slices.Compact(addrs)

	var b strings.Builder
	b.WriteString("# CNMS-rendered Postfix recipient table (virtual_mailbox_maps) -- do not edit.\n")
	b.WriteString("# Source: MailServer " + req.Server.Namespace + "/" + req.Server.Name + "\n")
	for _, a := range addrs {
		b.WriteString(a + " OK\n")
	}
	return b.String()
}
