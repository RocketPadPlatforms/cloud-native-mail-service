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
	PortRspamdController int32 = 11334 // controller worker (incl. GET /metrics)
	PortRspamdNormal     int32 = 11333 // normal (scanner) worker, pod-internal: the proxy worker forwards to it

	// Dovecot.
	PortDovecotLMTP  int32 = 24   // LMTP for Postfix virtual_transport
	PortDovecotAuth  int32 = 4190 // internal auth protocol (Postfix SASL)
	PortDovecotIMAP  int32 = 143
	PortDovecotIMAPS int32 = 993

	// Valkey.
	PortValkey        int32 = 6379
	PortValkeyMetrics int32 = 9121 // redis_exporter sidecar (ADR-0005)
)

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
		return []int32{PortRspamdMilter, PortRspamdController}
	case ComponentDovecot:
		return []int32{PortDovecotLMTP, PortDovecotAuth}
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
