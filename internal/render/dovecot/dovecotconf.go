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

// Layout constants (ADR 0004): the rendered configuration is mounted
// at /etc/dovecot (shadowing the image's shipped tree), the TLS
// Secret at /etc/dovecot/ssl (the image's default ssl_server
// location), the mail volume (when enabled) at /srv/mail. vmailUID
// and vmailGID are the vmail account of the official 2.4.5 image.
const (
	tlsMountPath = "/etc/dovecot/ssl"
	usersPath    = "/etc/dovecot/users"
	mailRoot     = "/srv/mail"
	vmailUID     = 1000
	vmailGID     = 1000
	// The 2.4 configuration format version (verified against the
	// 2.4.5 image); the first setting of the file must be this one.
	configVersion  = "2.4.3"
	storageVersion = "2.4.3"
)

// renderDovecotConf renders the self-contained dovecot.conf. The
// file is the complete configuration (no includes), so the listener
// set is exactly the one rendered here. Every setting and its
// runtime effect was verified against the running 2.4.5 image
// (doveconf -n plus live protocol probes on ports 24, 143, 993,
// 4190 and LMTP/IMAP deliveries).
func renderDovecotConf(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Dovecot dovecot.conf -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0004)\n\n",
		req.Server.Namespace, req.Server.Name)
	b.WriteString("# Self-contained configuration: mounted at /etc/dovecot it\n")
	b.WriteString("# shadows the image's shipped configuration tree (no conf.d\n")
	b.WriteString("# includes); the listener set is exactly the one rendered\n")
	b.WriteString("# here. Verified against dovecot/dovecot:2.4.5 (ADR 0001).\n\n")
	b.WriteString("# 2.4 configuration format markers: the first setting must\n")
	b.WriteString("# be dovecot_config_version; unknown settings are fatal.\n")
	fmt.Fprintf(&b, "dovecot_config_version = %s\n", configVersion)
	fmt.Fprintf(&b, "dovecot_storage_version = %s\n", storageVersion)
	if mailboxesEnabled(req) {
		b.WriteString("protocols = lmtp imap\n")
	} else {
		b.WriteString("# Mailboxes disabled: auth-only mode (no IMAP, no mail\n")
		b.WriteString("# storage). The lmtp listener stays for config uniformity;\n")
		b.WriteString("# Postfix in send-only mode does not LMTP-deliver (ADR 0004).\n")
		b.WriteString("protocols = lmtp\n")
	}
	b.WriteString("log_path = /dev/stdout\n")
	b.WriteString("base_dir = /var/run/dovecot\n")
	b.WriteString("state_dir = /var/run/dovecot\n")
	b.WriteString("default_internal_user = vmail\n")
	b.WriteString("default_internal_group = vmail\n")
	b.WriteString("default_login_user = vmail\n")
	b.WriteString("mail_uid = vmail\n")
	b.WriteString("mail_gid = vmail\n")
	b.WriteString("# 2.4 requires an explicit mail driver; autodetection fails\n")
	b.WriteString("# on the first delivery into a not-yet-existing maildir.\n")
	b.WriteString("mail_driver = maildir\n")
	b.WriteString("mail_path = ~/mail\n")
	b.WriteString("# TLS floor (ADR 0006): all login protocols require TLS.\n")
	b.WriteString("# The imaps listener does implicit TLS; AUTH on the imap\n")
	b.WriteString("# listener is only possible after STARTTLS.\n")
	b.WriteString("ssl = required\n")
	b.WriteString("ssl_min_protocol = TLSv1.2\n")
	b.WriteString("# Certificate of the MailServer TLS Secret (ADR 0004); the\n")
	b.WriteString("# paths are the image's default ssl_server location.\n")
	b.WriteString("ssl_server {\n")
	fmt.Fprintf(&b, "  cert_file = %s/tls.crt\n", tlsMountPath)
	fmt.Fprintf(&b, "  key_file = %s/tls.key\n", tlsMountPath)
	b.WriteString("}\n")
	b.WriteString("# SASL backend for Postfix (smtpd_sasl_type = dovecot): plain\n")
	b.WriteString("# TCP inside the NetworkPolicy-restricted pod pair (ADR 0006).\n")
	b.WriteString("# The name \"auth\" selects the auth-client protocol (VERSION,\n")
	b.WriteString("# MECH, SPID, CUID) that the Postfix 3.11 client speaks; the\n")
	b.WriteString("# names \"auth-master\" and \"auth-userdb\" select other protocols.\n")
	b.WriteString("service auth {\n")
	b.WriteString("  inet_listener auth {\n")
	fmt.Fprintf(&b, "    port = %d\n", render.PortDovecotAuth)
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("# Internal LMTP hop (Postfix virtual_transport): plain TCP by\n")
	b.WriteString("# design (ADR 0006). `ssl = no` keeps the listener exempt from\n")
	b.WriteString("# the global `ssl = required` (without it 2.4 forces implicit\n")
	b.WriteString("# TLS on the listener; verified).\n")
	b.WriteString("service lmtp {\n")
	b.WriteString("  inet_listener lmtp {\n")
	fmt.Fprintf(&b, "    port = %d\n", render.PortDovecotLMTP)
	b.WriteString("    ssl = no\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	if mailboxesEnabled(req) {
		b.WriteString("service imap-login {\n")
		b.WriteString("  inet_listener imap {\n")
		fmt.Fprintf(&b, "    port = %d\n", render.PortDovecotIMAP)
		b.WriteString("  }\n")
		b.WriteString("  inet_listener imaps {\n")
		fmt.Fprintf(&b, "    port = %d\n", render.PortDovecotIMAPS)
		b.WriteString("  }\n")
		b.WriteString("}\n")
	}
	b.WriteString("# Phase 2 test source: the passwd-file rendered into this same\n")
	b.WriteString("# tree (users, from insecureStaticUsers). Phase 3 replaces it\n")
	b.WriteString("# with the directory (MailDirectory).\n")
	b.WriteString("passdb passwd-file {\n")
	fmt.Fprintf(&b, "  passwd_file_path = %s\n", usersPath)
	b.WriteString("}\n")
	b.WriteString("userdb passwd-file {\n")
	fmt.Fprintf(&b, "  passwd_file_path = %s\n", usersPath)
	b.WriteString("}\n")
	return b.String()
}
