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
	"fmt"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The certificate Secret is mounted read-only at this path; the keys
// follow the cert-manager standard (tls.crt, tls.key).
const tlsMountPath = "/etc/postfix/tls"

// renderMainCF renders main.cf. The order of blocks is fixed so that
// the output (and its content hash) is deterministic.
func renderMainCF(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(header(req))

	domains := sortedDomains(req)

	b.WriteString("# --- identity (D2: the first authorized domain) ---\n")
	fmt.Fprintf(&b, "myhostname = %s\n", primaryDomain(req))
	b.WriteString("myorigin = $myhostname\n")

	b.WriteString("# --- version (ADR 0001 pin: Postfix 3.11) ---\n")
	b.WriteString("compatibility_level = 3.11\n\n")

	b.WriteString("# --- network (ADR 0006: loopback only is trusted) ---\n")
	b.WriteString("inet_protocols = all\n")
	b.WriteString("inet_interfaces = all\n")
	b.WriteString("mynetworks = 127.0.0.0/8\n\n")

	b.WriteString("# --- recipient and sender checks (ADR 0006 floor) ---\n")
	b.WriteString("smtpd_helo_required = yes\n")
	b.WriteString("smtpd_sender_restrictions = reject_non_fqdn_sender\n")
	b.WriteString("smtpd_recipient_restrictions = permit_mynetworks, " +
		"permit_sasl_authenticated, reject_unauth_destination\n\n")

	if mailboxesEnabled(req) {
		b.WriteString("# --- delivery routing: mailboxes enabled ---\n")
		b.WriteString("# Recipients of the authorized domains are delivered to\n")
		b.WriteString("# Dovecot, which validates users against the directory.\n")
		b.WriteString("mydestination =\n")
		fmt.Fprintf(&b, "virtual_mailbox_domains = %s\n", strings.Join(domains, ", "))
		fmt.Fprintf(&b, "virtual_transport = lmtp:%s:%d\n\n",
			render.ServiceFQDN(req.Server.Name, req.Server.Namespace, render.ComponentDovecot),
			render.PortDovecotLMTP)
	} else {
		b.WriteString("# --- delivery routing: send-only (mailboxes disabled) ---\n")
		b.WriteString("# This server is a sending system: port 25 rejects all\n")
		b.WriteString("# mail (reject_unauth_destination), authenticated\n")
		b.WriteString("# submission is the way in. Enable mailboxes to receive.\n")
		b.WriteString("mydestination =\n\n")
	}

	if submissionEnabled(req) {
		b.WriteString("# --- submission: Dovecot is the SASL backend (ADR 0004) ---\n")
		b.WriteString("smtpd_sasl_type = dovecot\n")
		fmt.Fprintf(&b, "smtpd_sasl_path = inet:%s:%d\n",
			render.ServiceFQDN(req.Server.Name, req.Server.Namespace, render.ComponentDovecot),
			render.PortDovecotAuth)
		b.WriteString("smtpd_sasl_security_options = noanonymous\n\n")
	}

	b.WriteString("# --- TLS (ADR 0006 floor: AUTH only over TLS, TLS >= 1.2) ---\n")
	b.WriteString("# Inbound on port 25 is opportunistic (STARTTLS offered,\n")
	b.WriteString("# cleartext accepted); the submission listener (587)\n")
	b.WriteString("# requires TLS via the per-service override in\n")
	b.WriteString("# master.cf. Outbound delivery is opportunistic.\n")
	b.WriteString("smtpd_tls_security_level = may\n")
	b.WriteString("smtpd_tls_auth_only = yes\n")
	fmt.Fprintf(&b, "smtpd_tls_mandatory_protocols = %s\n", tlsMinProtocol)
	fmt.Fprintf(&b, "smtpd_tls_mandatory_ciphers = %s\n", tlsMinCipherGrade)
	fmt.Fprintf(&b, "smtpd_tls_cert_file = %s/tls.crt\n", tlsMountPath)
	fmt.Fprintf(&b, "smtpd_tls_key_file = %s/tls.key\n", tlsMountPath)
	b.WriteString("smtpd_tls_loglevel = 1\n")
	b.WriteString("smtp_tls_security_level = may\n")
	fmt.Fprintf(&b, "smtp_tls_mandatory_protocols = %s\n", tlsMinProtocol)
	fmt.Fprintf(&b, "smtp_tls_mandatory_ciphers = %s\n", tlsMinCipherGrade)
	b.WriteString("smtp_tls_loglevel = 1\n\n")

	b.WriteString("# --- Rspamd milter (principle 5: never unsigned) ---\n")
	b.WriteString("# An unreachable or timing-out milter tempfails mail; it is\n")
	b.WriteString("# never relayed unsigned.\n")
	// The endpoint needs the explicit inet transport; without it
	// Postfix rejects the milter name and tempfails every message.
	fmt.Fprintf(&b, "smtpd_milters = inet:%s:%d\n",
		render.ServiceFQDN(req.Server.Name, req.Server.Namespace, render.ComponentRspamd),
		render.PortRspamdMilter)
	fmt.Fprintf(&b, "non_smtpd_milters = inet:%s:%d\n",
		render.ServiceFQDN(req.Server.Name, req.Server.Namespace, render.ComponentRspamd),
		render.PortRspamdMilter)
	b.WriteString("milter_default_action = tempfail\n")
	fmt.Fprintf(&b, "milter_connect_timeout = %s\n", milterConnectTimeout)
	fmt.Fprintf(&b, "milter_command_timeout = %s\n", milterCommandTimeout)
	fmt.Fprintf(&b, "milter_content_timeout = %s\n\n", milterContentTimeout)

	b.WriteString("# --- per-client rate limits (anvil; ADR 0006 floor) ---\n")
	fmt.Fprintf(&b, "smtpd_client_connection_rate_limit = %d\n", perClientConnectionLimit)
	fmt.Fprintf(&b, "smtpd_client_message_rate_limit = %d\n", perClientMessageLimit)
	fmt.Fprintf(&b, "smtpd_client_recipient_rate_limit = %d\n", perClientRecipientLimit)
	fmt.Fprintf(&b, "anvil_rate_time_unit = %s\n", anvilTimeUnit)
	b.WriteString("smtpd_error_sleep_time = 1s\n")
	b.WriteString("smtpd_soft_error_limit = 3\n")
	b.WriteString("smtpd_hard_error_limit = 5\n\n")

	b.WriteString("# --- limits and logging (ADR 0004) ---\n")
	fmt.Fprintf(&b, "message_size_limit = %d\n", messageSizeLimit)
	b.WriteString("# postlog writes to the pod stdout.\n")
	b.WriteString("maillog_file = /dev/stdout\n")

	return b.String()
}

// header is the rendered-by banner.
func header(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Postfix main.cf -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s, authorized domains: %s\n\n",
		req.Server.Namespace, req.Server.Name,
		strings.Join(sortedDomains(req), ", "))
	return b.String()
}
