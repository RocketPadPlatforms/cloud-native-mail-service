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
	"regexp"
	"slices"
	"strings"
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

// build renders all four components and returns every object of the
// data plane except the validation Jobs.
func build(t *testing.T, mutate func(*cnmsv1alpha1.MailServer)) []client.Object {
	t.Helper()
	dp := buildPlane(t, mutate, nil)
	objs := slices.Concat(dp.Configs, dp.Base, dp.Workloads)
	return objs
}

// buildPlane renders all four components and builds the staged
// object set.
func buildPlane(t *testing.T, mutate func(*cnmsv1alpha1.MailServer), users []render.StaticUser) *DataPlane {
	t.Helper()
	server := testServer(t, mutate)
	req := &render.RenderRequest{
		Server:            server,
		AuthorizedDomains: []string{domainB, domainA}, // unsorted on purpose
		StaticUsers:       users,
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
	dp, err := Build(req, renders)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return dp
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
	kindSecret                = "Secret"
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
	case *corev1.Secret:
		return kindSecret
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
	// Four hash-named ConfigMaps and the Dovecot users Secret.
	configs := 0
	for _, o := range objs {
		if k := kindOf(o); k == kindConfigMap || k == kindSecret {
			configs++
			if !configNameRE.MatchString(o.GetName()) {
				t.Errorf("%s %q is not named <server>-<component>-<hash8>", k, o.GetName())
			}
		}
	}
	if configs != 5 {
		t.Errorf("config objects = %d, want 5", configs)
	}
	if len(objs) != len(want)+configs {
		t.Errorf("object count = %d, want %d", len(objs), len(want)+configs)
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
	if len(pod.InitContainers) != 0 {
		t.Errorf("validation runs in a Job, not an init container: %v", pod.InitContainers)
	}
	assertConfigVolumeExists(t, objs, pod, "postfix")
	if pod.ShareProcessNamespace == nil || !*pod.ShareProcessNamespace {
		t.Error("postfix pod must share the process namespace for the cert-reload sidecar")
	}
	main := pod.Containers[0]
	if main.Image != images.Postfix {
		t.Errorf("postfix image = %q", main.Image)
	}
	for _, want := range []string{"main.cf", "master.cf", "vmailbox"} {
		if !hasMount(main.VolumeMounts, configVolumeName, "/etc/postfix/"+want) {
			t.Errorf("postfix main container misses subPath mount %s", want)
		}
	}
	if !hasMount(main.VolumeMounts, postfixQueueVolume, "/var/spool/postfix") {
		t.Error("postfix main container misses the queue mount")
	}
	if !hasMount(main.VolumeMounts, tlsVolumeName, postfixTLSDir) {
		t.Error("postfix main container misses the tls mount (smtpd loads the certificate)")
	}
	if main.ReadinessProbe == nil || main.ReadinessProbe.TCPSocket == nil || main.ReadinessProbe.TCPSocket.Port.IntVal != render.PortPostfixInbound {
		t.Error("postfix readiness probe must target port 25")
	}
	if main.StartupProbe == nil || main.StartupProbe.TCPSocket == nil || main.StartupProbe.TCPSocket.Port.IntVal != render.PortPostfixInbound {
		t.Error("postfix needs a startup probe on port 25")
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
	for _, m := range metrics.VolumeMounts {
		if m.Name == postfixQueueVolume && !m.ReadOnly {
			t.Error("queue-metrics must mount the queue volume read-only")
		}
	}
	if !hasMount(metrics.VolumeMounts, postfixQueueVolume, "/var/spool/postfix") {
		t.Error("queue-metrics misses the queue mount")
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
	if p := sts.Spec.Template.Spec.ShareProcessNamespace; p == nil || !*p {
		t.Error("dovecot pod must share the process namespace for the cert-reload sidecar")
	}
	main := sts.Spec.Template.Spec.Containers[0]
	if main.SecurityContext == nil || main.SecurityContext.Capabilities == nil ||
		!slices.Contains(main.SecurityContext.Capabilities.Add, corev1.Capability("NET_BIND_SERVICE")) {
		t.Error("dovecot main container needs NET_BIND_SERVICE (ports 24/143)")
	}
	for i := range sts.Spec.Template.Spec.Containers {
		c := &sts.Spec.Template.Spec.Containers[i]
		if c.Name != certReloadContainerName {
			continue
		}
		// The Dovecot image lacks coreutils; the sidecar runs the
		// Postfix image as vmail and reloads via SIGHUP.
		if c.Image != images.Postfix {
			t.Errorf("dovecot cert-reload image = %s, want the Postfix image", c.Image)
		}
		if c.SecurityContext == nil || c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != dovecotVmailID {
			t.Error("dovecot cert-reload must run as vmail to signal the master")
		}
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
	wantArg := "--redis.addr=redis://127.0.0.1:" + fmt.Sprintf("%d", render.PortValkey)
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

// configNameRE matches "<server>-<component>-<hash8>".
var configNameRE = regexp.MustCompile(`^` + testName + `-(postfix|rspamd|dovecot|valkey)-[0-9a-f]{8}$`)

// assertConfigVolumeExists checks that the config volume of a pod
// references an immutable ConfigMap of the object set whose name has
// the component prefix and the hash suffix.
func assertConfigVolumeExists(t *testing.T, objs []client.Object, pod corev1.PodSpec, component string) {
	t.Helper()
	for _, v := range pod.Volumes {
		if v.Name != configVolumeName {
			continue
		}
		if v.ConfigMap == nil {
			t.Fatalf("%s config volume is not a ConfigMap", component)
		}
		name := v.ConfigMap.Name
		if !strings.HasPrefix(name, testName+"-"+component+"-") || !configNameRE.MatchString(name) {
			t.Errorf("%s config volume %q is not hash-named", component, name)
		}
		cm := mustFind[*corev1.ConfigMap](t, find(t, objs, kindConfigMap, name), name)
		if cm.Immutable == nil || !*cm.Immutable {
			t.Errorf("ConfigMap %s must be immutable", name)
		}
		return
	}
	t.Fatalf("%s pod has no config volume", component)
}

func TestConfigObjectsAreContentNamed(t *testing.T) {
	name := func(mutate func(*cnmsv1alpha1.MailServer)) string {
		objs := build(t, mutate)
		sts := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-dovecot"), "sts")
		for _, v := range sts.Spec.Template.Spec.Volumes {
			if v.ConfigMap != nil {
				return v.ConfigMap.Name
			}
		}
		t.Fatal("no ConfigMap volume")
		return ""
	}
	a, b := name(nil), name(nil)
	if a != b {
		t.Errorf("same config must give the same name: %s vs %s", a, b)
	}
	c := name(func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	})
	if a == c {
		t.Error("a config change must give a new ConfigMap name")
	}
}

func TestDovecotUsersLiveInAnImmutableSecret(t *testing.T) {
	user := render.StaticUser{Username: "u", PasswordHash: "{SHA512-CRYPT}x", Mailbox: "u@" + domainA}
	dp := buildPlane(t, nil, []render.StaticUser{user})
	var sec *corev1.Secret
	for _, o := range dp.Configs {
		if s, ok := o.(*corev1.Secret); ok {
			sec = s
		}
		if cm, ok := o.(*corev1.ConfigMap); ok {
			if _, leaked := cm.Data["users"]; leaked {
				t.Errorf("ConfigMap %s carries the users file", cm.Name)
			}
		}
	}
	if sec == nil {
		t.Fatal("no users Secret")
	}
	if sec.Immutable == nil || !*sec.Immutable || !configNameRE.MatchString(sec.Name) {
		t.Errorf("users Secret %q must be immutable and hash-named", sec.Name)
	}
	if !strings.Contains(string(sec.Data["users"]), "{SHA512-CRYPT}x") {
		t.Error("users Secret misses the users file")
	}

	sts := mustFind[*appsv1.StatefulSet](t, find(t, dp.Workloads, kindStatefulSet, testName+"-dovecot"), "sts")
	pod := sts.Spec.Template.Spec
	var usersVol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == usersVolumeName {
			usersVol = &pod.Volumes[i]
		}
	}
	if usersVol == nil || usersVol.Secret == nil || usersVol.Secret.SecretName != sec.Name {
		t.Fatalf("dovecot pod must mount the users Secret %q: %v", sec.Name, usersVol)
	}
	if !hasMount(pod.Containers[0].VolumeMounts, usersVolumeName, render.DovecotUsersDir) {
		t.Error("dovecot container misses the users mount")
	}
	if m := usersVol.Secret.DefaultMode; m == nil || *m != 0o440 {
		t.Errorf("users Secret mode = %v, want 0440", m)
	}
	// The cert-reload sidecar gets the certificate and the runtime
	// directory only, not the password hashes.
	for _, c := range pod.Containers[1:] {
		if c.Name != certReloadContainerName {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.Name != tlsVolumeName && m.Name != runVolumeName {
				t.Errorf("cert-reload mounts %q; only tls and run are needed", m.Name)
			}
		}
	}

	// A user change changes the Secret name and the pod-template hash,
	// but not the ConfigMap name.
	other := buildPlane(t, nil, []render.StaticUser{{Username: "v", PasswordHash: "{SHA512-CRYPT}y", Mailbox: "v@" + domainA}})
	osts := mustFind[*appsv1.StatefulSet](t, find(t, other.Workloads, kindStatefulSet, testName+"-dovecot"), "sts")
	if osts.Spec.Template.Annotations[ConfigHashAnnotation] == sts.Spec.Template.Annotations[ConfigHashAnnotation] {
		t.Error("a user change must change the pod-template hash")
	}
	var otherSecret *corev1.Secret
	for _, o := range other.Configs {
		if s, ok := o.(*corev1.Secret); ok {
			otherSecret = s
		}
	}
	if otherSecret.Name == sec.Name {
		t.Error("a user change must give a new Secret name")
	}
}

func TestValidationJobs(t *testing.T) {
	dp := buildPlane(t, nil, nil)
	if len(dp.Validations) != 4 {
		t.Fatalf("validations = %d, want one per component", len(dp.Validations))
	}
	for _, v := range dp.Validations {
		job := v.Job
		suffix := validationNameInfix + hashSuffix(v.Hash)
		if want := testName + "-" + string(v.Component) + suffix; job.Name != want {
			t.Errorf("job name = %q, want %q", job.Name, want)
		}
		if job.Annotations[ConfigHashAnnotation] != v.Hash {
			t.Errorf("%s job misses the config hash annotation", v.Component)
		}
		if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
			t.Errorf("%s job must not retry", v.Component)
		}
		if job.Spec.TTLSecondsAfterFinished == nil || job.Spec.ActiveDeadlineSeconds == nil {
			t.Errorf("%s job needs a TTL and a deadline", v.Component)
		}
		pod := job.Spec.Template.Spec
		if pod.RestartPolicy != corev1.RestartPolicyNever || len(pod.Containers) != 1 {
			t.Errorf("%s job pod = %+v", v.Component, pod)
		}
		if pod.Containers[0].Image != componentImage(v.Component) || len(pod.Containers[0].Command) == 0 {
			t.Errorf("%s job runs %q %v", v.Component, pod.Containers[0].Image, pod.Containers[0].Command)
		}
		if _, ok := job.Spec.Template.Labels[componentLabel]; ok {
			t.Errorf("%s validation pods must not match Services or the PDB", v.Component)
		}
		// The Job mounts the config of the workload's pod template.
		var cm string
		for _, vol := range pod.Volumes {
			if vol.ConfigMap != nil {
				cm = vol.ConfigMap.Name
			}
		}
		if !configNameRE.MatchString(cm) {
			t.Errorf("%s job mounts %q", v.Component, cm)
		}
		for _, vol := range pod.Volumes {
			if vol.Secret != nil {
				t.Errorf("%s job mounts Secret %q; no validator reads one", v.Component, vol.Secret.SecretName)
			}
		}
	}
	// Postfix: no PVC, an emptyDir queue.
	for _, v := range dp.Validations {
		if v.Component != render.ComponentPostfix {
			continue
		}
		for _, vol := range v.Job.Spec.Template.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil {
				t.Error("the postfix validation must not need the queue PVC")
			}
		}
	}
}

func TestConfigHashCoversImage(t *testing.T) {
	r := &render.RenderResult{ConfigData: map[string]string{"f": "x"}}
	first := configHash(render.ComponentPostfix, r)
	if again := configHash(render.ComponentPostfix, r); first != again {
		t.Error("the same component and render must give the same hash")
	}
	// The same files under another engine image: the validation must
	// run again.
	if first == configHash(render.ComponentRspamd, r) {
		t.Error("a different image must give a different hash")
	}
}

// longestName is the length of the longest object name generated for
// a MailServer with the given name (every optional object enabled).
func longestName(t *testing.T, serverName string) int {
	t.Helper()
	dp := buildPlane(t, func(s *cnmsv1alpha1.MailServer) {
		s.Name = serverName
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		s.Spec.Valkey = &cnmsv1alpha1.Valkey{Persistence: new(true)}
	}, []render.StaticUser{{Username: "u", PasswordHash: "{SHA512-CRYPT}x", Mailbox: "u@" + domainA}})
	longest := 0
	for _, o := range slices.Concat(dp.Configs, dp.Base, dp.Workloads) {
		longest = max(longest, len(o.GetName()))
	}
	for _, v := range dp.Validations {
		longest = max(longest, len(v.Job.Name))
	}
	// ServiceMonitor names (suffix "-metrics").
	for _, o := range ServiceMonitors(testServer(t, func(s *cnmsv1alpha1.MailServer) { s.Name = serverName })) {
		longest = max(longest, len(o.GetName()))
	}
	return longest
}

// maxServerNameLength is the longest MailServer name that keeps every
// generated object name within the 63-character DNS label limit. The
// CEL rule on MailServer metadata.name uses the same number.
const maxServerNameLength = 44

func TestGeneratedNamesFitLimit(t *testing.T) {
	if n := longestName(t, strings.Repeat("a", maxServerNameLength)); n > 63 {
		t.Errorf("a %d-character server name generates a %d-character object name", maxServerNameLength, n)
	}
	// The limit is tight: one more character overflows.
	if n := longestName(t, strings.Repeat("a", maxServerNameLength+1)); n <= 63 {
		t.Errorf("maxServerNameLength is too low: %d characters still fit (%d)", maxServerNameLength+1, n)
	}
}

func TestCertReloadLoopBaselinesBeforeLoop(t *testing.T) {
	script := certReloadLoop("/tls/tls.crt", []string{"postfix", "reload"})[2]
	baseline := strings.Index(script, "last=$(sum)")
	loop := strings.Index(script, "while :")
	if baseline < 0 || loop < 0 || baseline > loop {
		t.Errorf("the baseline hash must be taken before the loop:\n%s", script)
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
