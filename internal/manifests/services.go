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

package manifests

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// services renders the Service set of a MailServer: the inbound
// service (type/traffic policy from spec.inbound), the headless
// services of the StatefulSets, the component services, and the
// metrics services (ADR 0005: every metrics endpoint gets a
// Service).
func services(server *cnmsv1alpha1.MailServer) []client.Object {
	// Inbound service.
	inboundType := strValue(serverInboundServiceTypePtr(server), "LoadBalancer")
	inbound := corev1.Service{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentPostfix), render.ComponentPostfix),
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceType(inboundType),
			Selector: componentSelector(server, render.ComponentPostfix).MatchLabels,
			Ports:    []corev1.ServicePort{{Name: "smtp", Port: render.PortPostfixInbound, TargetPort: intstr.FromInt32(render.PortPostfixInbound)}},
		},
	}
	if inboundType == string(corev1.ServiceTypeLoadBalancer) {
		inbound.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicy(strValue(serverInboundTrafficPolicyPtr(server), "Local"))
	}
	if submissionEnabled(server) {
		inbound.Spec.Ports = append(inbound.Spec.Ports, corev1.ServicePort{
			Name: "submission", Port: render.PortPostfixSubmission,
			TargetPort: intstr.FromInt32(render.PortPostfixSubmission),
		})
	}

	// Rspamd (milter + controller; the controller port carries the
	// metrics, ADR 0005).
	rspamd := corev1.Service{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentRspamd), render.ComponentRspamd),
		Spec: corev1.ServiceSpec{
			Selector: componentSelector(server, render.ComponentRspamd).MatchLabels,
			Ports: []corev1.ServicePort{
				{Name: "milter", Port: render.PortRspamdMilter, TargetPort: intstr.FromInt32(render.PortRspamdMilter)},
				{Name: "rspamd-metrics", Port: render.PortRspamdController, TargetPort: intstr.FromInt32(render.PortRspamdController)},
			},
		},
	}

	// Dovecot (LMTP + internal auth protocol; IMAP only with mail
	// storage).
	dovecotPorts := []corev1.ServicePort{
		{Name: "lmtp", Port: render.PortDovecotLMTP, TargetPort: intstr.FromInt32(render.PortDovecotLMTP)},
		{Name: "auth", Port: render.PortDovecotAuth, TargetPort: intstr.FromInt32(render.PortDovecotAuth)},
	}
	if mailboxesEnabled(server) {
		dovecotPorts = append(dovecotPorts,
			corev1.ServicePort{Name: "imap", Port: render.PortDovecotIMAP, TargetPort: intstr.FromInt32(render.PortDovecotIMAP)},
			corev1.ServicePort{Name: "imaps", Port: render.PortDovecotIMAPS, TargetPort: intstr.FromInt32(render.PortDovecotIMAPS)})
	}
	dovecot := corev1.Service{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentDovecot), render.ComponentDovecot),
		Spec: corev1.ServiceSpec{
			Selector: componentSelector(server, render.ComponentDovecot).MatchLabels,
			Ports:    dovecotPorts,
		},
	}

	// Valkey.
	valkey := corev1.Service{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentValkey), render.ComponentValkey),
		Spec: corev1.ServiceSpec{
			Selector: componentSelector(server, render.ComponentValkey).MatchLabels,
			Ports:    []corev1.ServicePort{{Name: valkeyPortName, Port: render.PortValkey, TargetPort: intstr.FromInt32(render.PortValkey)}},
		},
	}

	// Metrics services (ADR 0005).
	metricsService := func(name, portName string, port int32, c render.Component) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: objectMeta(server, name, c),
			Spec: corev1.ServiceSpec{
				Selector: componentSelector(server, c).MatchLabels,
				Ports:    []corev1.ServicePort{{Name: portName, Port: port, TargetPort: intstr.FromInt32(port)}},
			},
		}
	}

	headless := func(name string, c render.Component) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: objectMeta(server, name, c),
			Spec: corev1.ServiceSpec{
				ClusterIP: "None",
				Selector:  componentSelector(server, c).MatchLabels,
			},
		}
	}

	return []client.Object{
		&inbound,
		headless(render.ServiceName(server.Name, render.ComponentPostfix)+"-headless", render.ComponentPostfix),
		&rspamd,
		&dovecot,
		headless(render.ServiceName(server.Name, render.ComponentDovecot)+"-headless", render.ComponentDovecot),
		&valkey,
		metricsService(render.ServiceName(server.Name, render.ComponentPostfix)+"-metrics", "postfix-metrics", render.PortPostfixMetrics, render.ComponentPostfix),
		metricsService(render.ServiceName(server.Name, render.ComponentValkey)+"-metrics", "valkey-metrics", render.PortValkeyMetrics, render.ComponentValkey),
	}
}

func serverInboundServiceTypePtr(server *cnmsv1alpha1.MailServer) *string {
	if server.Spec.Inbound == nil {
		return nil
	}
	return server.Spec.Inbound.ServiceType
}

func serverInboundTrafficPolicyPtr(server *cnmsv1alpha1.MailServer) *string {
	if server.Spec.Inbound == nil {
		return nil
	}
	return server.Spec.Inbound.ExternalTrafficPolicy
}

// networkPolicies renders the ADR 0006 NetworkPolicy set:
//   - "<server>-ingress": default-deny ingress for the server's pods
//     with allows for the inbound ports (anywhere), the component
//     ports (same server only) and the metrics ports (cluster-wide,
//     documented v1 trade-off).
//   - "<server>-egress": default-deny egress for the server's pods
//     with allows for DNS and the component ports.
//   - "<server>-egress-smtp": port-25 egress for the Postfix pods
//     only (Phase 2: any destination; Phase 7 narrows this via the
//     Cilium egress gateway).
//   - "cnms-no-port25-egress": namespace-wide deny of port-25 egress
//     for non-CNMS pods, so foreign pods cannot send mail from node
//     IPs (reputation damage).
func networkPolicies(server *cnmsv1alpha1.MailServer) []client.Object {
	componentPorts := []int32{render.PortRspamdMilter, render.PortDovecotLMTP, render.PortDovecotAuth, render.PortValkey}
	metricsPorts := []int32{render.PortPostfixMetrics, render.PortRspamdController, render.PortValkeyMetrics}
	anywhere := func() networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"},
		}
	}
	anywhereV6 := func() networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: "::/0"},
		}
	}
	sameServer := networkingv1.NetworkPolicyPeer{
		PodSelector: &metav1.LabelSelector{
			MatchLabels: serverSelector(server).MatchLabels,
		},
	}
	portOf := func(p int32) networkingv1.NetworkPolicyPort {
		return networkingv1.NetworkPolicyPort{Port: new(intstr.FromInt32(p))}
	}
	ingressPorts := []networkingv1.NetworkPolicyPort{portOf(render.PortPostfixInbound)}
	if submissionEnabled(server) {
		ingressPorts = append(ingressPorts, portOf(render.PortPostfixSubmission))
	}
	componentPortSpecs := make([]networkingv1.NetworkPolicyPort, 0, len(componentPorts))
	for _, p := range componentPorts {
		componentPortSpecs = append(componentPortSpecs, portOf(p))
	}
	metricsPortSpecs := make([]networkingv1.NetworkPolicyPort, 0, len(metricsPorts))
	for _, p := range metricsPorts {
		metricsPortSpecs = append(metricsPortSpecs, portOf(p))
	}
	dnsPorts := []networkingv1.NetworkPolicyPort{
		{Protocol: new(corev1.ProtocolTCP), Port: new(intstr.FromInt32(53))},
		{Protocol: new(corev1.ProtocolUDP), Port: new(intstr.FromInt32(53))},
	}

	ingress := &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(server, server.Name+"-ingress", ""),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: serverSelector(server).MatchLabels,
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{Ports: ingressPorts}, // from anywhere
				{From: []networkingv1.NetworkPolicyPeer{sameServer}, Ports: componentPortSpecs},
				{From: []networkingv1.NetworkPolicyPeer{anywhere(), anywhereV6()}, Ports: metricsPortSpecs},
			},
		},
	}

	egress := &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(server, server.Name+"-egress", ""),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: serverSelector(server).MatchLabels,
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{To: []networkingv1.NetworkPolicyPeer{anywhere(), anywhereV6()}, Ports: dnsPorts},
				{To: []networkingv1.NetworkPolicyPeer{sameServer}, Ports: componentPortSpecs},
			},
		},
	}

	egressSMTP := &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(server, server.Name+"-egress-smtp", render.ComponentPostfix),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: componentSelector(server, render.ComponentPostfix).MatchLabels,
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{Ports: []networkingv1.NetworkPolicyPort{portOf(render.PortPostfixInbound)}},
			},
		},
	}

	// Non-CNMS pods: everything except TCP port 25 egress. The
	// complement ranges (1-24, 26-65535) express "all TCP except 25"
	// in the allow-only NetworkPolicy semantics; UDP is fully allowed
	// (SMTP is TCP).
	noPort25 := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cnms-no-port25-egress",
			Namespace: server.Namespace,
			Labels: map[string]string{
				appLabel:       appValue,
				ManagedByLabel: managedByValue,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      ManagedByLabel,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{managedByValue},
				}},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				// Ranges are Port + EndPort; NetworkPolicyPort.Port
				// itself only accepts an integer or a named port.
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: new(corev1.ProtocolTCP), Port: new(intstr.FromInt32(1)), EndPort: new(int32(24))},
					{Protocol: new(corev1.ProtocolTCP), Port: new(intstr.FromInt32(26)), EndPort: new(int32(65535))},
					{Protocol: new(corev1.ProtocolUDP), Port: new(intstr.FromInt32(1)), EndPort: new(int32(65535))},
				},
			}},
		},
	}

	return []client.Object{ingress, egress, egressSMTP, noPort25}
}

// postfixPodDisruptionBudget is the Postfix disruption budget
// (spec.availability.postfixMinAvailable, default 1).
func postfixPodDisruptionBudget(server *cnmsv1alpha1.MailServer) *policyv1.PodDisruptionBudget {
	minAvailable := intstr.FromInt32(1)
	if server.Spec.Availability != nil && server.Spec.Availability.PostfixMinAvailable != nil {
		minAvailable = *server.Spec.Availability.PostfixMinAvailable
	}
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentPostfix), render.ComponentPostfix),
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: componentSelector(server, render.ComponentPostfix).MatchLabels,
			},
		},
	}
}

// certificate renders the cert-manager Certificate for the MailServer
// (unstructured, so the operator works without the cert-manager
// CRDs, principle 10). Unauthorized domains are dropped from the SANs
// (ADR 0003); the primary domain (first sorted) is the common name.
func certificate(req *render.RenderRequest) *unstructured.Unstructured {
	server := req.Server
	domains := slices.Clone(req.AuthorizedDomains)
	slices.Sort(domains)
	primary := domains[0]
	return unstructuredObj("cert-manager.io/v1", "Certificate",
		server.Name, server.Namespace, objectLabels(server, ""), map[string]any{
			"issuerRef": map[string]any{
				"kind": server.Spec.TLS.IssuerRef.Kind,
				"name": server.Spec.TLS.IssuerRef.Name,
			},
			"secretName": TLSSecretName(server),
			"commonName": primary,
			"dnsNames":   domainsToAny(domains),
			"usages":     []any{"server auth", "client auth"},
		})
}

// domainsToAny converts a []string to a []any (unstructured).
func domainsToAny(domains []string) []any {
	out := make([]any, 0, len(domains))
	for _, d := range domains {
		out = append(out, d)
	}
	return out
}
