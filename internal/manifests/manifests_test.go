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
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/dovecot"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/postfix"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/rspamd"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/valkey"
)

const (
	testNamespace = "tenant1"
	testName      = "mail"
	domainA       = "a.example"
	domainB       = "b.example"
)

func testServer(t *testing.T, mutate func(*cnmsv1alpha1.MailServer)) *cnmsv1alpha1.MailServer {
	t.Helper()
	server := &cnmsv1alpha1.MailServer{}
	server.Name = testName
	server.Namespace = testNamespace
	server.Spec.DirectoryRef = cnmsv1alpha1.NameReference{Name: "dir"}
	server.Spec.Outbound.Direct = &cnmsv1alpha1.DirectOutbound{}
	server.Spec.Domains = []cnmsv1alpha1.NameReference{{Name: "dom"}}
	server.Spec.TLS.IssuerRef = cnmsv1alpha1.IssuerReference{Kind: "Issuer", Name: "selfsigned"}
	if mutate != nil {
		mutate(server)
	}
	return server
}

// build renders all four components and builds the object set.
func build(t *testing.T, mutate func(*cnmsv1alpha1.MailServer)) []client.Object {
	t.Helper()
	server := testServer(t, mutate)
	req := &render.RenderRequest{
		Server:            server,
		AuthorizedDomains: []string{domainB, domainA}, // unsorted on purpose
	}
	renders := map[render.Component]*render.RenderResult{}
	for _, r := range []render.EngineRenderer{
		postfix.New(), rspamd.New(), dovecot.New(), valkey.New(),
	} {
		res, err := r.Render(req)
		if err != nil {
			t.Fatalf("render %s: %v", r.Component(), err)
		}
		renders[r.Component()] = res
	}
	built, err := Build(req, renders)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return built
}

// find returns the first object with the given kind and name.
func find(t *testing.T, objs []client.Object, kind, name string) client.Object {
	t.Helper()
	for _, o := range objs {
		if kindOf(o) == kind && o.GetName() == name {
			return o
		}
	}
	t.Fatalf("object %s/%s not found", kind, name)
	return nil
}

// kindOf reports the kind of a client.Object without relying on
// TypeMeta (typed objects carry an empty TypeMeta).
const (
	kindConfigMap             = "ConfigMap"
	kindService               = "Service"
	kindPersistentVolumeClaim = "PersistentVolumeClaim"
	kindStatefulSet           = "StatefulSet"
	kindDeployment            = "Deployment"
	kindNetworkPolicy         = "NetworkPolicy"
	kindPodDisruptionBudget   = "PodDisruptionBudget"
	kindCertificate           = "Certificate"
)

func kindOf(o client.Object) string {
	switch v := o.(type) {
	case *corev1.ConfigMap:
		return kindConfigMap
	case *corev1.Service:
		return kindService
	case *corev1.PersistentVolumeClaim:
		return kindPersistentVolumeClaim
	case *appsv1.StatefulSet:
		return kindStatefulSet
	case *appsv1.Deployment:
		return kindDeployment
	case *networkingv1.NetworkPolicy:
		return kindNetworkPolicy
	case *policyv1.PodDisruptionBudget:
		return kindPodDisruptionBudget
	case *unstructured.Unstructured:
		return v.GetKind()
	}
	return ""
}

func mustFind[T any](t *testing.T, o client.Object, name string) T {
	t.Helper()
	v, ok := o.(T)
	if !ok {
		t.Fatalf("%s is not %T", name, v)
	}
	return v
}

func TestBuildObjectSet(t *testing.T) {
	objs := build(t, nil)
	n := testName
	want := []struct{ kind, name string }{
		{kindConfigMap, n + "-postfix"},
		{kindConfigMap, n + "-rspamd"},
		{kindConfigMap, n + "-dovecot"},
		{kindConfigMap, n + "-valkey"},
		{kindService, n + "-postfix"},
		{kindService, n + "-postfix-headless"},
		{kindService, n + "-rspamd"},
		{kindService, n + "-dovecot"},
		{kindService, n + "-dovecot-headless"},
		{kindService, n + "-valkey"},
		{kindService, n + "-postfix-metrics"},
		{kindService, n + "-valkey-metrics"},
		{kindStatefulSet, n + "-postfix"},
		{kindStatefulSet, n + "-dovecot"},
		{kindDeployment, n + "-rspamd"},
		{kindDeployment, n + "-valkey"},
		{kindNetworkPolicy, n + "-ingress"},
		{kindNetworkPolicy, n + "-egress"},
		{kindNetworkPolicy, n + "-egress-smtp"},
		{kindNetworkPolicy, "cnms-no-port25-egress"},
		{kindPodDisruptionBudget, n + "-postfix"},
		{kindCertificate, n},
	}
	for _, wk := range want {
		if find(t, objs, wk.kind, wk.name) == nil {
			t.Errorf("missing %s %q", wk.kind, wk.name)
		}
	}
	if len(objs) != len(want) {
		t.Errorf("object count = %d, want %d", len(objs), len(want))
	}
	// No Valkey PVC without persistence.
	if exists(objs, kindPersistentVolumeClaim, n+"-valkey-valkey-data") {
		t.Error("unexpected Valkey PVC without persistence")
	}
}

// exists reports whether an object with the given kind and name is
// in the list (non-fatal, for negative assertions).
func exists(objs []client.Object, kind, name string) bool {
	for _, o := range objs {
		if kindOf(o) == kind && o.GetName() == name {
			return true
		}
	}
	return false
}

func TestPostfixStatefulSet(t *testing.T) {
	objs := build(t, nil)
	sts := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-postfix"), testName+"-postfix")
	if *sts.Spec.Replicas != 2 {
		t.Errorf("replicas = %d, want 2 (default)", *sts.Spec.Replicas)
	}
	if sts.Spec.ServiceName != testName+"-postfix-headless" {
		t.Errorf("serviceName = %q", sts.Spec.ServiceName)
	}
	if len(sts.Spec.VolumeClaimTemplates) != 1 || sts.Spec.VolumeClaimTemplates[0].Name != postfixQueueVolume {
		t.Fatalf("volumeClaimTemplates = %v", sts.Spec.VolumeClaimTemplates)
	}
	if q := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]; q.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Errorf("queue storage = %v, want 1Gi (default)", q)
	}
	if g := sts.Spec.Template.Spec.SecurityContext.FSGroup; g == nil || *g != postfixQueueGroup {
		t.Errorf("fsGroup = %v, want %d", g, postfixQueueGroup)
	}
	if g := sts.Spec.Template.Spec.TerminationGracePeriodSeconds; g == nil || *g != postfixTerminationGrace {
		t.Errorf("terminationGracePeriodSeconds = %v", g)
	}
	if ann := sts.Spec.Template.Annotations[ConfigHashAnnotation]; ann == "" {
		t.Error("config-hash annotation missing")
	}
	pod := sts.Spec.Template.Spec
	if len(pod.InitContainers) != 1 || slices.Equal(pod.InitContainers[0].Command, []string{"postfix", "check"}) != true {
		t.Errorf("init container = %v", pod.InitContainers)
	}
	main := pod.Containers[0]
	if main.Image != images.Postfix {
		t.Errorf("postfix image = %q", main.Image)
	}
	for _, want := range []string{"main.cf", "master.cf"} {
		if !hasMount(main.VolumeMounts, configVolumeName, "/etc/postfix/"+want) {
			t.Errorf("postfix main container misses subPath mount %s", want)
		}
	}
	if !hasMount(main.VolumeMounts, postfixQueueVolume, "/var/spool/postfix") {
		t.Error("postfix main container misses the queue mount")
	}
	if main.ReadinessProbe == nil || main.ReadinessProbe.TCPSocket == nil || main.ReadinessProbe.TCPSocket.Port.IntVal != render.PortPostfixInbound {
		t.Error("postfix readiness probe must target port 25")
	}
	assertPostfixSidecars(t, pod)
}

// assertPostfixSidecars checks the queue-metrics and cert-reload
// sidecars of the Postfix pod (split out for cyclomatic complexity).
func assertPostfixSidecars(t *testing.T, pod corev1.PodSpec) {
	t.Helper()
	var metrics, reload *corev1.Container
	for i := range pod.Containers {
		switch pod.Containers[i].Name {
		case queueMetricsContainerName:
			metrics = &pod.Containers[i]
		case certReloadContainerName:
			reload = &pod.Containers[i]
		}
	}
	if metrics == nil {
		t.Fatal("queue-metrics sidecar missing")
	}
	if metrics.SecurityContext == nil || metrics.SecurityContext.RunAsUser == nil || *metrics.SecurityContext.RunAsUser != postfixUserUID {
		t.Error("queue-metrics must run as the postfix user (ADR 0005)")
	}
	if len(metrics.Env) != 1 || metrics.Env[0].Name != "CNMS_QUEUE_METRICS_PORT" || metrics.Env[0].Value != "9150" {
		t.Errorf("queue-metrics env = %v", metrics.Env)
	}
	if reload == nil {
		t.Fatal("cert-reload sidecar missing")
	}
}

func TestDovecotStatefulSet(t *testing.T) {
	// Auth-only (mailboxes off, API default).
	objs := build(t, nil)
	sts := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-dovecot"), testName+"-dovecot")
	if *sts.Spec.Replicas != 1 {
		t.Errorf("dovecot replicas = %d, want 1", *sts.Spec.Replicas)
	}
	if len(sts.Spec.VolumeClaimTemplates) != 0 {
		t.Errorf("dovecot has mail volume without mailboxes: %v", sts.Spec.VolumeClaimTemplates)
	}
	if g := sts.Spec.Template.Spec.SecurityContext.FSGroup; g == nil || *g != dovecotVmailID {
		t.Errorf("fsGroup = %v, want %d", g, dovecotVmailID)
	}
	main := sts.Spec.Template.Spec.Containers[0]
	if main.SecurityContext == nil || main.SecurityContext.Capabilities == nil ||
		!slices.Contains(main.SecurityContext.Capabilities.Add, corev1.Capability("NET_BIND_SERVICE")) {
		t.Error("dovecot main container needs NET_BIND_SERVICE (ports 24/143)")
	}
	svc := mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-dovecot"), testName+"-dovecot")
	if hasPort(svc, "imap") || hasPort(svc, "imaps") {
		t.Error("dovecot service exposes IMAP without mailboxes")
	}

	// With mailboxes.
	objs = build(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	})
	sts = mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-dovecot"), testName+"-dovecot")
	if len(sts.Spec.VolumeClaimTemplates) != 1 || sts.Spec.VolumeClaimTemplates[0].Name != dovecotMailVolume {
		t.Fatalf("volumeClaimTemplates = %v", sts.Spec.VolumeClaimTemplates)
	}
	if q := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]; q.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Errorf("mail storage = %v, want 10Gi (default)", q)
	}
	main = sts.Spec.Template.Spec.Containers[0]
	if !hasMount(main.VolumeMounts, dovecotMailVolume, "/srv/mail") {
		t.Error("dovecot main container misses the mail mount")
	}
	svc = mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-dovecot"), testName+"-dovecot")
	if !hasPort(svc, "imap") || !hasPort(svc, "imaps") {
		t.Error("dovecot service must expose IMAP with mailboxes")
	}
}

func TestRspamdDeployment(t *testing.T) {
	objs := build(t, nil)
	dep := mustFind[*appsv1.Deployment](t, find(t, objs, kindDeployment, testName+"-rspamd"), testName+"-rspamd")
	if *dep.Spec.Replicas != 2 {
		t.Errorf("rspamd replicas = %d, want 2 (default)", *dep.Spec.Replicas)
	}
	ru := dep.Spec.Strategy.RollingUpdate
	if ru == nil || ru.MaxUnavailable.IntVal != 0 || ru.MaxSurge.IntVal != 1 {
		t.Errorf("rspamd strategy = %+v, want maxUnavailable 0 with surge", dep.Spec.Strategy)
	}
	main := dep.Spec.Template.Spec.Containers[0]
	if !hasMount(main.VolumeMounts, configVolumeName, "/etc/rspamd/local.d") {
		t.Error("rspamd misses the local.d mount")
	}
	if !hasMount(main.VolumeMounts, "state", rspamdStateDir) {
		t.Error("rspamd misses the state emptyDir mount")
	}
	if main.ReadinessProbe == nil || main.ReadinessProbe.TCPSocket.Port.IntVal != render.PortRspamdController {
		t.Error("rspamd readiness probe must target the controller port")
	}
}

func TestValkeyDeployment(t *testing.T) {
	objs := build(t, nil)
	dep := mustFind[*appsv1.Deployment](t, find(t, objs, kindDeployment, testName+"-valkey"), testName+"-valkey")
	if *dep.Spec.Replicas != 1 {
		t.Errorf("valkey replicas = %d, want 1", *dep.Spec.Replicas)
	}
	var exporter *corev1.Container
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == "redis-exporter" {
			exporter = &dep.Spec.Template.Spec.Containers[i]
		}
	}
	if exporter == nil {
		t.Fatal("redis-exporter sidecar missing")
	}
	wantArg := "--redis.address=redis://127.0.0.1:" + fmt.Sprintf("%d", render.PortValkey)
	if !slices.Contains(exporter.Args, wantArg) {
		t.Errorf("exporter args = %v, want %q", exporter.Args, wantArg)
	}

	// Persistence on: PVC exists and the pod references it.
	objs = build(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Valkey = &cnmsv1alpha1.Valkey{Persistence: new(true), Storage: new(resource.MustParse("2Gi"))}
	})
	pvc := mustFind[*corev1.PersistentVolumeClaim](t, find(t, objs, kindPersistentVolumeClaim, testName+"-valkey-valkey-data"), "valkey pvc")
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Errorf("valkey pvc = %v, want 2Gi", q)
	}
	dep = mustFind[*appsv1.Deployment](t, find(t, objs, kindDeployment, testName+"-valkey"), testName+"-valkey")
	var foundPVC bool
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == testName+"-valkey-valkey-data" {
			foundPVC = true
		}
	}
	if !foundPVC {
		t.Error("valkey pod does not reference the PVC")
	}
}

func TestInboundService(t *testing.T) {
	// Defaults: LoadBalancer with externalTrafficPolicy Local.
	objs := build(t, nil)
	svc := mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-postfix"), testName+"-postfix")
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Errorf("service type = %q, want LoadBalancer (default)", svc.Spec.Type)
	}
	if svc.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal {
		t.Errorf("externalTrafficPolicy = %q, want Local (default)", svc.Spec.ExternalTrafficPolicy)
	}
	if !hasPort(svc, "smtp") || !hasPort(svc, "submission") {
		t.Error("inbound service must expose 25 and 587")
	}

	// ClusterIP without submission.
	objs = build(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Inbound = &cnmsv1alpha1.Inbound{ServiceType: new("ClusterIP")}
		s.Spec.Submission = &cnmsv1alpha1.Submission{Enabled: new(false)}
	})
	svc = mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-postfix"), testName+"-postfix")
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("service type = %q, want ClusterIP", svc.Spec.Type)
	}
	if hasPort(svc, "submission") {
		t.Error("inbound service exposes 587 with submission disabled")
	}
}

func TestNetworkPolicies(t *testing.T) {
	objs := build(t, nil)
	ingress := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-ingress"), testName+"-ingress")
	if len(ingress.Spec.Ingress) != 3 {
		t.Fatalf("ingress rules = %d, want 3", len(ingress.Spec.Ingress))
	}
	// Rule 1: inbound ports from anywhere.
	if !portSet(ingress.Spec.Ingress[0].Ports, []int32{render.PortPostfixInbound, render.PortPostfixSubmission}) {
		t.Errorf("ingress rule 0 ports = %v", ingress.Spec.Ingress[0].Ports)
	}
	// Rule 2: component ports from the same server only.
	if len(ingress.Spec.Ingress[1].From) != 1 || ingress.Spec.Ingress[1].From[0].PodSelector == nil {
		t.Error("ingress rule 1 must come from the same server")
	}
	// Rule 3: metrics ports from the cluster.
	if len(ingress.Spec.Ingress[2].From) != 2 {
		t.Errorf("ingress rule 2 from = %v, want IPv4+IPv6 any", ingress.Spec.Ingress[2].From)
	}
	if !portSet(ingress.Spec.Ingress[2].Ports, []int32{render.PortPostfixMetrics, render.PortRspamdController, render.PortValkeyMetrics}) {
		t.Errorf("ingress rule 2 ports = %v", ingress.Spec.Ingress[2].Ports)
	}

	egress := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-egress"), testName+"-egress")
	if len(egress.Spec.Egress) != 2 {
		t.Fatalf("egress rules = %d, want 2", len(egress.Spec.Egress))
	}
	// DNS to anywhere; port 25 egress must NOT be in the general egress
	// rule (it is granted only to the Postfix pods).
	if portPresent(egress.Spec.Egress[0].Ports, render.PortPostfixInbound) {
		t.Error("general egress allows port 25; only the Postfix pods may send")
	}

	smtp := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-egress-smtp"), testName+"-egress-smtp")
	comp := smtp.Spec.PodSelector.MatchLabels[componentLabel]
	if comp != string(render.ComponentPostfix) {
		t.Errorf("egress-smtp podSelector component = %q", comp)
	}

	noPort25 := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, "cnms-no-port25-egress"), "cnms-no-port25-egress")
	reqs := noPort25.Spec.PodSelector.MatchExpressions
	if len(reqs) != 1 || reqs[0].Key != ManagedByLabel || reqs[0].Operator != "NotIn" {
		t.Errorf("no-port25 podSelector = %+v", noPort25.Spec.PodSelector)
	}
	ports := noPort25.Spec.Egress[0].Ports
	if portPresent(ports, render.PortPostfixInbound) {
		t.Error("no-port25 egress must not allow TCP 25")
	}
	var tcpRanges []string
	for _, p := range ports {
		if p.Protocol == nil || *p.Protocol == corev1.ProtocolTCP {
			r := p.Port.String()
			if p.EndPort != nil {
				r += fmt.Sprintf("-%d", *p.EndPort)
			}
			tcpRanges = append(tcpRanges, r)
		}
	}
	if !slices.Contains(tcpRanges, "1-24") || !slices.Contains(tcpRanges, "26-65535") {
		t.Errorf("no-port25 TCP ranges = %v, want the complement of 25", tcpRanges)
	}
}

func TestPodDisruptionBudget(t *testing.T) {
	// Default minAvailable 1.
	objs := build(t, nil)
	pdb := mustFind[*policyv1.PodDisruptionBudget](t, find(t, objs, kindPodDisruptionBudget, testName+"-postfix"), testName+"-postfix")
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntVal != 1 {
		t.Errorf("minAvailable = %v, want 1 (default)", pdb.Spec.MinAvailable)
	}

	// Override.
	objs = build(t, func(s *cnmsv1alpha1.MailServer) {
		minAv := intstr.FromString("2")
		s.Spec.Availability = &cnmsv1alpha1.Availability{PostfixMinAvailable: &minAv}
	})
	pdb = mustFind[*policyv1.PodDisruptionBudget](t, find(t, objs, kindPodDisruptionBudget, testName+"-postfix"), testName+"-postfix")
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.StrVal != "2" {
		t.Errorf("minAvailable = %v, want 2", pdb.Spec.MinAvailable)
	}
}

func TestCertificate(t *testing.T) {
	objs := build(t, func(s *cnmsv1alpha1.MailServer) {
		// A non-default TLS secret name.
		s.Spec.TLS.SecretName = new("my-tls")
	})
	cert := mustFind[*unstructured.Unstructured](t, find(t, objs, kindCertificate, testName), testName)
	spec := cert.Object["spec"].(map[string]any)
	// The primary domain is the first SORTED authorized domain, even
	// though the request carries them unsorted.
	if cn, _ := spec["commonName"].(string); cn != domainA {
		t.Errorf("commonName = %q, want %q", cn, domainA)
	}
	dns, _ := spec["dnsNames"].([]any)
	if len(dns) != 2 || dns[0] != domainA || dns[1] != domainB {
		t.Errorf("dnsNames = %v, want [%s %s]", dns, domainA, domainB)
	}
	if sn, _ := spec["secretName"].(string); sn != "my-tls" {
		t.Errorf("secretName = %q, want my-tls", sn)
	}
	// Default secret name.
	objs2 := build(t, nil)
	cert2 := mustFind[*unstructured.Unstructured](t, find(t, objs2, kindCertificate, testName), testName)
	spec2 := cert2.Object["spec"].(map[string]any)
	if sn, _ := spec2["secretName"].(string); sn != testName+"-tls" {
		t.Errorf("secretName = %q, want %q", sn, testName+"-tls")
	}
}

func TestServiceMonitors(t *testing.T) {
	server := testServer(t, nil)
	mons := ServiceMonitors(server)
	if len(mons) != 3 {
		t.Fatalf("monitors = %d, want 3", len(mons))
	}
	for _, want := range []struct{ name, port string }{
		{testName + "-postfix-metrics", "postfix-metrics"},
		{testName + "-rspamd", "rspamd-metrics"},
		{testName + "-valkey-metrics", "valkey-metrics"},
	} {
		var found bool
		for _, m := range mons {
			u := m.(*unstructured.Unstructured)
			if u.GetName() != want.name {
				continue
			}
			found = true
			eps := u.Object["spec"].(map[string]any)["endpoints"].([]any)
			port := eps[0].(map[string]any)["port"]
			if port != want.port {
				t.Errorf("%s endpoint port = %v, want %s", want.name, port, want.port)
			}
			if u.GetAPIVersion() != "monitoring.coreos.com/v1" || u.GetKind() != "ServiceMonitor" {
				t.Errorf("%s gvk = %s/%s", want.name, u.GetAPIVersion(), u.GetKind())
			}
		}
		if !found {
			t.Errorf("monitor %q missing", want.name)
		}
	}
}

func TestConfigHashIsDeterministicAndConfigBound(t *testing.T) {
	objs1 := build(t, nil)
	objs2 := build(t, nil)
	hash1 := mustFind[*appsv1.StatefulSet](t, find(t, objs1, kindStatefulSet, testName+"-postfix"), "sts").Spec.Template.Annotations[ConfigHashAnnotation]
	hash2 := mustFind[*appsv1.StatefulSet](t, find(t, objs2, kindStatefulSet, testName+"-postfix"), "sts").Spec.Template.Annotations[ConfigHashAnnotation]
	if hash1 != hash2 {
		t.Error("config hash must be deterministic across builds")
	}

	// A different MailServer (different name => different render) must
	// produce a different hash.
	objs3 := build(t, func(s *cnmsv1alpha1.MailServer) { s.Name = "other" })
	hash3 := mustFind[*appsv1.StatefulSet](t, find(t, objs3, kindStatefulSet, "other-postfix"), "sts").Spec.Template.Annotations[ConfigHashAnnotation]
	if hash1 == hash3 {
		t.Error("different MailServer must yield a different config hash")
	}
}

// test helpers.

func hasMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, m := range mounts {
		if m.Name == name && m.MountPath == path {
			return true
		}
	}
	return false
}

func hasPort(svc *corev1.Service, name string) bool {
	for _, p := range svc.Spec.Ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

func portSet(ports []networkingv1.NetworkPolicyPort, want []int32) bool {
	if len(ports) != len(want) {
		return false
	}
	got := make([]int32, 0, len(ports))
	for _, p := range ports {
		got = append(got, p.Port.IntVal)
	}
	slices.Sort(got)
	w := slices.Clone(want)
	slices.Sort(w)
	return slices.Equal(got, w)
}

func portPresent(ports []networkingv1.NetworkPolicyPort, want int32) bool {
	for _, p := range ports {
		if p.Port.Type == intstr.Int && p.Port.IntVal == want {
			return true
		}
	}
	return false
}
