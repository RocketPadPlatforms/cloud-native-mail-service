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

package render

import "fmt"

// Cluster-internal service ports of the data plane. Renderers and the
// controller must agree on these values; NetworkPolicies (ADR-0006)
// allow exactly these.
const (
	// Postfix.
	PortPostfixInbound    int32 = 25   // postscreen/smtpd inbound
	PortPostfixSubmission int32 = 587  // smtpd submission (Dovecot SASL)
	PortPostfixMetrics    int32 = 9150 // queue-metrics sidecar (ADR-0005)

	// Rspamd (4.x: the milter protocol is served by the proxy worker,
	// the controller worker serves the HTTP API incl. GET /metrics;
	// verified against the 4.2 documentation).
	PortRspamdMilter     int32 = 11332 // proxy worker milter interface
	PortRspamdController int32 = 11334 // controller worker (incl. GET /metrics), loopback clients only
	PortRspamdMetrics    int32 = 9151  // metrics sidecar: loopback copy of the controller /metrics (ADR-0005)
	PortRspamdNormal     int32 = 11333 // normal (scanner) worker, pod-internal: the proxy worker forwards to it
	PortRspamdFuzzy      int32 = 11335 // outbound only: the fuzzy storage of rspamd.com (UDP, SRV _fuzzy._tcp.rspamd.com)

	// Dovecot. The image runs as the non-root vmail user, which can
	// bind a port below 1024 only where the node sets
	// net.ipv4.ip_unprivileged_port_start low (verified: an added
	// NET_BIND_SERVICE does not help a non-root process without
	// ambient capabilities, which Kubernetes cannot grant). Dovecot
	// therefore listens on the unprivileged ports of the official
	// image's rootless configuration (the *Listen constants) and the
	// Service maps the well-known ports to them. NetworkPolicies and
	// probes see the listen ports.
	PortDovecotLMTP        int32 = 24    // Service port: LMTP for Postfix virtual_transport
	PortDovecotLMTPListen  int32 = 31024 // LMTP listener
	PortDovecotAuth        int32 = 12345 // internal auth protocol (Postfix SASL); not 4190, which is ManageSieve
	PortDovecotIMAP        int32 = 143   // Service port
	PortDovecotIMAPListen  int32 = 31143 // IMAP listener
	PortDovecotIMAPS       int32 = 993   // Service port
	PortDovecotIMAPSListen int32 = 31993 // IMAPS listener

	// Valkey.
	PortValkey        int32 = 6379
	PortValkeyMetrics int32 = 9121 // redis_exporter sidecar (ADR-0005)
)

// DovecotUsersDir is where the Secret with the Dovecot passdb and userdb
// files is mounted. It is a separate directory because a file mounted
// inside the read-only ConfigMap mount at /etc/dovecot cannot be created.
const DovecotUsersDir = "/etc/dovecot-users"

// PostfixSecretDir is where the Secret with the directory tables of
// Postfix is mounted (they carry the bind password).
const PostfixSecretDir = "/etc/postfix-secret"

// AllComponents lists the data-plane components in a fixed order.
func AllComponents() []Component {
	return []Component{ComponentPostfix, ComponentRspamd, ComponentDovecot, ComponentValkey}
}

// ComponentPorts are the in-cluster service ports of a component.
func ComponentPorts(c Component) []int32 {
	switch c {
	case ComponentPostfix:
		return []int32{PortPostfixInbound, PortPostfixSubmission, PortPostfixMetrics}
	case ComponentRspamd:
		return []int32{PortRspamdMilter, PortRspamdMetrics}
	case ComponentDovecot:
		return []int32{PortDovecotLMTPListen, PortDovecotAuth}
	case ComponentValkey:
		return []int32{PortValkey, PortValkeyMetrics}
	default:
		return nil
	}
}

// ServiceName is the deterministic Service name of a component of a
// MailServer.
func ServiceName(serverName string, c Component) string {
	return fmt.Sprintf("%s-%s", serverName, c)
}

// ServiceFQDN is the cluster DNS name of a component Service, used by
// renderers for cross-pod endpoints (milter, SASL, LMTP, Valkey).
func ServiceFQDN(serverName, namespace string, c Component) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", ServiceName(serverName, c), namespace)
}

// PostfixTableFiles are the table files of the Postfix render (besides
// main.cf and master.cf). They are always rendered, possibly without a
// line, so the manifests mount exactly these and the validation gate
// parses each of them.
var PostfixTableFiles = []string{"vmailbox", "valias", "sender_login", "recipient_defer", "null_sender"}
