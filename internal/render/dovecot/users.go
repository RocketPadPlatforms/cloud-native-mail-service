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

// renderUsers renders the passwd-file at usersPath. Dovecot 2.4 looks
// users up by the full recipient address (LMTP delivery) with no
// local-part fallback, so each static user is listed twice: with the
// full mailbox address (delivery and full-address IMAP login) and
// with the bare username (local-part IMAP login). The home directory
// lives on the mail volume (ADR 0004).
func renderUsers(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Dovecot passwd-file (users) -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0004)\n\n",
		req.Server.Namespace, req.Server.Name)
	b.WriteString("# Format: user:password:uid:gid::home:\n")
	for _, u := range req.StaticUsers {
		home := mailRoot + "/" + u.Username
		fmt.Fprintf(&b, "%s:%s:%d:%d::%s:\n",
			u.Mailbox, u.PasswordHash, vmailUID, vmailGID, home)
		fmt.Fprintf(&b, "%s:%s:%d:%d::%s:\n",
			u.Username, u.PasswordHash, vmailUID, vmailGID, home)
	}
	return b.String()
}
