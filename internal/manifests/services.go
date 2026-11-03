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

	// Rspamd (milter + the metrics sidecar, ADR 0005). The controller
	// port is not exposed: it answers loopback clients only.
	rspamd := corev1.Service{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentRspamd), render.ComponentRspamd),
		Spec: corev1.ServiceSpec{
			Selector: componentSelector(server, render.ComponentRspamd).MatchLabels,
			Ports: []corev1.ServicePort{
				{Name: "milter", Port: render.PortRspamdMilter, TargetPort: intstr.FromInt32(render.PortRspamdMilter)},
				{Name: rspamdMetricsContainer, Port: render.PortRspamdMetrics, TargetPort: intstr.FromInt32(render.PortRspamdMetrics)},
			},
		},
	}

	// Dovecot (internal auth protocol; LMTP and IMAP only with mail
	// storage, auth-only mode has no other listener).
	dovecotPorts := []corev1.ServicePort{
		{Name: "auth", Port: render.PortDovecotAuth, TargetPort: intstr.FromInt32(render.PortDovecotAuth)},
	}
	if mailboxesEnabled(server) {
		dovecotPorts = append(dovecotPorts,
			corev1.ServicePort{Name: "lmtp", Port: render.PortDovecotLMTP, TargetPort: intstr.FromInt32(render.PortDovecotLMTPListen)},
			corev1.ServicePort{Name: "imap", Port: render.PortDovecotIMAP, TargetPort: intstr.FromInt32(render.PortDovecotIMAPListen)},
			corev1.ServicePort{Name: "imaps", Port: render.PortDovecotIMAPS, TargetPort: intstr.FromInt32(render.PortDovecotIMAPSListen)})
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
//   - "<server>-egress-rspamd": TCP 443 and UDP 11335 to public
//     addresses for the Rspamd pods only (fuzzy storage and maps).
//
// The deny of port-25 egress for other pods cannot be expressed in a
// NetworkPolicy; it ships as a static CiliumClusterwideNetworkPolicy
// example (config/network-policy, ADR 0006).
func networkPolicies(server *cnmsv1alpha1.MailServer) []client.Object {
	// Pod ports: a policy sees the port behind the Service.
	componentPorts := []int32{render.PortRspamdMilter, render.PortDovecotAuth, render.PortValkey}
	if mailboxesEnabled(server) {
		componentPorts = append(componentPorts, render.PortDovecotLMTPListen)
	}
	metricsPorts := []int32{render.PortPostfixMetrics, render.PortRspamdMetrics, render.PortValkeyMetrics}
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
	// Cilium CIDR rules never match cluster endpoints, so in-cluster
	// peers need selector peers next to the ipBlocks (ADR 0006).
	anyPod := networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{},
	}
	// namespaceSelector and podSelector in one peer are ANDed: only
	// the kube-dns pods of kube-system.
	kubeDNS := networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
		},
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"k8s-app": "kube-dns"},
		},
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
	if mailboxesEnabled(server) {
		ingressPorts = append(ingressPorts,
			portOf(render.PortDovecotIMAPListen), portOf(render.PortDovecotIMAPSListen))
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
				{From: []networkingv1.NetworkPolicyPeer{anyPod, anywhere(), anywhereV6()}, Ports: metricsPortSpecs},
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
				{To: []networkingv1.NetworkPolicyPeer{kubeDNS, anywhere(), anywhereV6()}, Ports: dnsPorts},
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

	// Rspamd reaches its public services on purpose: the shipped
	// fuzzy_check module talks to fuzzy.rspamd.com (UDP 11335) and the
	// shipped maps are fetched from https://maps.rspamd.com (TCP 443).
	// Private, shared (CGNAT), loopback, link-local (cloud metadata)
	// and unique-local ranges are excluded, so the allow does not open
	// in-cluster services. shortcut: pods with global-unicast IPv6
	// addresses (EKS IPv6, ...) are not excluded; list the pod and
	// service CIDRs once the API can carry them (ADR 0006).
	external := func(cidr string, except ...string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr, Except: except}}
	}
	egressRspamd := &networkingv1.NetworkPolicy{
		ObjectMeta: objectMeta(server, server.Name+"-egress-rspamd", render.ComponentRspamd),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: componentSelector(server, render.ComponentRspamd).MatchLabels,
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{
					external("0.0.0.0/0", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
						"100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16"),
					external("::/0", "fc00::/7", "fe80::/10"),
				},
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: new(corev1.ProtocolTCP), Port: new(intstr.FromInt32(443))},
					{Protocol: new(corev1.ProtocolUDP), Port: new(intstr.FromInt32(render.PortRspamdFuzzy))},
				},
			}},
		},
	}

	return []client.Object{ingress, egress, egressSMTP, egressRspamd}
}

// PostfixReplicas is the effective number of Postfix replicas
// (spec.postfix.replicas, default 2).
func PostfixReplicas(server *cnmsv1alpha1.MailServer) int32 {
	if server.Spec.Postfix == nil {
		return 2
	}
	return int32Value(server.Spec.Postfix.Replicas, 2)
}

// PostfixMinAvailable is the effective minAvailable of the Postfix
// disruption budget (spec.availability.postfixMinAvailable, default 1).
func PostfixMinAvailable(server *cnmsv1alpha1.MailServer) intstr.IntOrString {
	if server.Spec.Availability != nil && server.Spec.Availability.PostfixMinAvailable != nil {
		return *server.Spec.Availability.PostfixMinAvailable
	}
	return intstr.FromInt32(1)
}

// postfixPodDisruptionBudget is the Postfix disruption budget.
func postfixPodDisruptionBudget(server *cnmsv1alpha1.MailServer) *policyv1.PodDisruptionBudget {
	minAvailable := PostfixMinAvailable(server)
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

// rspamdPodDisruptionBudget lets a node drain evict one Rspamd pod at
// a time. maxUnavailable 1 (not minAvailable 1) so the budget never
// blocks a drain: it always allows one eviction, even with a single
// replica, and with two or more replicas at least all but one stay
// available.
func rspamdPodDisruptionBudget(server *cnmsv1alpha1.MailServer) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: objectMeta(server, render.ServiceName(server.Name, render.ComponentRspamd), render.ComponentRspamd),
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: new(intstr.FromInt32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: componentSelector(server, render.ComponentRspamd).MatchLabels,
			},
		},
	}
}

// certificate renders the cert-manager Certificate for the MailServer
// (unstructured, so the operator works without the cert-manager
// CRDs, principle 10). Unauthorized domains are dropped from the SANs
// (ADR 0003); the primary domain (first sorted) is the common name.
// With no authorized domain the certificate keeps only the in-cluster
// server name (cert-manager needs at least one name, and the data
// plane mounts the Secret) and has no common name.
func certificate(req *render.RenderRequest) *unstructured.Unstructured {
	server := req.Server
	domains := slices.Clone(req.AuthorizedDomains)
	slices.Sort(domains)
	spec := map[string]any{
		"issuerRef": map[string]any{
			"kind": server.Spec.TLS.IssuerRef.Kind,
			"name": server.Spec.TLS.IssuerRef.Name,
		},
		"secretName": TLSSecretName(server),
		"dnsNames":   domainsToAny(domains),
		"usages":     []any{"server auth", "client auth"},
	}
	if len(domains) > 0 {
		spec["commonName"] = domains[0]
	} else {
		spec["dnsNames"] = domainsToAny([]string{req.ServerHostname()})
	}
	return unstructuredObj("cert-manager.io/v1", "Certificate",
		server.Name, server.Namespace, objectLabels(server, ""), spec)
}

// domainsToAny converts a []string to a []any (unstructured).
func domainsToAny(domains []string) []any {
	out := make([]any, 0, len(domains))
	for _, d := range domains {
		out = append(out, d)
	}
	return out
}
