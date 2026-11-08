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
	"bytes"
	"fmt"
	"os/exec"
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
	// testHostname is the authorized hostname of the test server: not a
	// domain it serves, so a mix-up of the two shows.
	testHostname = "mx.a.example"
)

func testServer(t *testing.T, mutate func(*cnmsv1alpha1.MailServer)) *cnmsv1alpha1.MailServer {
	t.Helper()
	server := &cnmsv1alpha1.MailServer{}
	server.Name = testName
	server.Namespace = testNamespace
	server.Spec.DirectoryRef = &cnmsv1alpha1.NameReference{Name: "dir"}
	server.Spec.Outbound.Direct = &cnmsv1alpha1.DirectOutbound{}
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
func buildPlane(t *testing.T, mutate func(*cnmsv1alpha1.MailServer), users []render.Account) *DataPlane {
	t.Helper()
	return buildPlaneFor(t, mutate, users, []string{domainB, domainA}) // unsorted on purpose
}

// buildPlaneFor is buildPlane with an explicit authorized domain set.
func buildPlaneFor(t *testing.T, mutate func(*cnmsv1alpha1.MailServer), users []render.Account, domains []string) *DataPlane {
	t.Helper()
	return buildPlaneHost(t, mutate, users, domains, testHostname)
}

// buildPlaneHost is buildPlaneFor with an explicit authorized hostname
// ("" while it is not authorized).
func buildPlaneHost(t *testing.T, mutate func(*cnmsv1alpha1.MailServer), users []render.Account, domains []string, hostname string) *DataPlane {
	t.Helper()
	server := testServer(t, mutate)
	req := &render.RenderRequest{
		Server:            server,
		AuthorizedDomains: domains,
		Hostname:          hostname,
		Accounts:          users,
		HashKey:           testHashKey,
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
		{kindNetworkPolicy, n + "-egress-rspamd"},
		{kindPodDisruptionBudget, n + "-postfix"},
		{kindPodDisruptionBudget, n + "-rspamd"},
		{kindCertificate, n},
	}
	for _, wk := range want {
		if find(t, objs, wk.kind, wk.name) == nil {
			t.Errorf("missing %s %q", wk.kind, wk.name)
		}
	}
	// Four hash-named ConfigMaps; the Dovecot users Secret is the live one,
	// outside the object set of the revision.
	configs := 0
	for _, o := range objs {
		if k := kindOf(o); k == kindConfigMap || k == kindSecret {
			configs++
			if !configNameRE.MatchString(o.GetName()) {
				t.Errorf("%s %q is not named <server>-<component>-<hash8>", k, o.GetName())
			}
		}
	}
	if configs != 4 {
		t.Errorf("config objects = %d, want 4", configs)
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
	for _, want := range []string{"main.cf", "master.cf", "vmailbox", "valias", "sender_login", "recipient_defer", "null_sender"} {
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
	// The non-root image cannot bind below 1024: the containers listen
	// on unprivileged ports and the Service maps the well-known ones.
	for _, p := range main.Ports {
		if p.ContainerPort < 1024 {
			t.Errorf("dovecot port %s listens on privileged port %d", p.Name, p.ContainerPort)
		}
	}
	// Auth-only mode has no LMTP listener: the Service, the container
	// ports and the probes only know the auth port.
	svc0 := mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-dovecot"), testName+"-dovecot")
	if hasPort(svc0, "lmtp") {
		t.Errorf("auth-only dovecot service exposes LMTP: %v", svc0.Spec.Ports)
	}
	for _, p := range main.Ports {
		if p.Name == "lmtp" {
			t.Errorf("auth-only dovecot container exposes LMTP: %v", main.Ports)
		}
	}
	if main.ReadinessProbe.TCPSocket.Port.IntVal != render.PortDovecotAuth ||
		main.LivenessProbe.TCPSocket.Port.IntVal != render.PortDovecotAuth {
		t.Error("auth-only dovecot probes must target the auth port")
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
}

func TestDovecotStatefulSetWithMailboxes(t *testing.T) {
	objs := build(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	})
	sts := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-dovecot"), testName+"-dovecot")
	if len(sts.Spec.VolumeClaimTemplates) != 1 || sts.Spec.VolumeClaimTemplates[0].Name != dovecotMailVolume {
		t.Fatalf("volumeClaimTemplates = %v", sts.Spec.VolumeClaimTemplates)
	}
	if q := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]; q.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Errorf("mail storage = %v, want 10Gi (default)", q)
	}
	main := sts.Spec.Template.Spec.Containers[0]
	if !hasMount(main.VolumeMounts, dovecotMailVolume, "/srv/mail") {
		t.Error("dovecot main container misses the mail mount")
	}
	svc := mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-dovecot"), testName+"-dovecot")
	if !hasPort(svc, "imap") || !hasPort(svc, "imaps") {
		t.Error("dovecot service must expose IMAP with mailboxes")
	}
	if !servicePortMaps(svc, "lmtp", render.PortDovecotLMTP, render.PortDovecotLMTPListen) {
		t.Errorf("dovecot service must map lmtp %d to %d: %v", render.PortDovecotLMTP, render.PortDovecotLMTPListen, svc.Spec.Ports)
	}
	if main.ReadinessProbe.TCPSocket.Port.IntVal != render.PortDovecotLMTPListen {
		t.Error("dovecot probes must target the LMTP listen port with mailboxes")
	}
	if !servicePortMaps(svc, "imap", render.PortDovecotIMAP, render.PortDovecotIMAPListen) ||
		!servicePortMaps(svc, "imaps", render.PortDovecotIMAPS, render.PortDovecotIMAPSListen) {
		t.Errorf("dovecot service must map the IMAP ports to the listen ports: %v", svc.Spec.Ports)
	}
	for _, p := range sts.Spec.Template.Spec.Containers[0].Ports {
		if p.ContainerPort < 1024 {
			t.Errorf("dovecot port %s listens on privileged port %d", p.Name, p.ContainerPort)
		}
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
	if main.LivenessProbe == nil || main.LivenessProbe.TCPSocket.Port.IntVal != render.PortRspamdController {
		t.Error("rspamd liveness probe must target the controller port")
	}
	assertRspamdMetricsSidecar(t, dep, objs)
}

// assertRspamdMetricsSidecar checks the loopback metrics sidecar and
// that the controller port is not exposed by any Service (ADR 0005).
func assertRspamdMetricsSidecar(t *testing.T, dep *appsv1.Deployment, objs []client.Object) {
	t.Helper()
	pod := dep.Spec.Template.Spec
	var side *corev1.Container
	for i := range pod.Containers {
		if pod.Containers[i].Name == rspamdMetricsContainer {
			side = &pod.Containers[i]
		}
	}
	if side == nil {
		t.Fatal("rspamd-metrics sidecar missing")
	}
	if side.Image != images.Postfix {
		t.Errorf("sidecar image = %s, want the Postfix image (busybox)", side.Image)
	}
	sc := side.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != nobodyID || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("sidecar securityContext = %+v", sc)
	}
	if len(side.Ports) != 1 || side.Ports[0].ContainerPort != render.PortRspamdMetrics || side.Ports[0].Name != rspamdMetricsContainer {
		t.Errorf("sidecar ports = %v", side.Ports)
	}
	if !hasMount(side.VolumeMounts, tmpVolumeName, tmpDir) {
		t.Error("sidecar misses the /tmp scratch mount")
	}
	script := side.Command[2]
	for _, want := range []string{"http://127.0.0.1:11334/metrics", "0.0.0.0:9151", "cnms_rspamd_metrics_up"} {
		if !strings.Contains(script, want) {
			t.Errorf("sidecar script misses %q", want)
		}
	}
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Errorf("sidecar script is not valid sh: %v\n%s", err, out)
	}
	svc := mustFind[*corev1.Service](t, find(t, objs, kindService, testName+"-rspamd"), testName+"-rspamd")
	for _, p := range svc.Spec.Ports {
		if p.Port == render.PortRspamdController || p.TargetPort.IntVal == render.PortRspamdController {
			t.Errorf("rspamd Service exposes the controller port: %v", p)
		}
		if p.Name == rspamdMetricsContainer && p.TargetPort.IntVal != render.PortRspamdMetrics {
			t.Errorf("rspamd-metrics Service port targets %v", p.TargetPort)
		}
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
	// Rule 3: metrics ports from the cluster. Cilium CIDR rules do not
	// match cluster endpoints, so an any-pod peer comes with the
	// IPv4 and IPv6 ipBlocks.
	from := ingress.Spec.Ingress[2].From
	if len(from) != 3 || from[0].NamespaceSelector == nil || len(from[0].NamespaceSelector.MatchLabels) != 0 ||
		from[0].PodSelector != nil || from[1].IPBlock == nil || from[2].IPBlock == nil {
		t.Errorf("ingress rule 2 from = %v, want any pod + IPv4 + IPv6 any", from)
	}
	if !portSet(ingress.Spec.Ingress[2].Ports, []int32{render.PortPostfixMetrics, render.PortRspamdMetrics, render.PortValkeyMetrics}) {
		t.Errorf("ingress rule 2 ports = %v", ingress.Spec.Ingress[2].Ports)
	}
	for _, rule := range ingress.Spec.Ingress {
		if portPresent(rule.Ports, render.PortRspamdController) {
			t.Error("ingress allows the Rspamd controller port")
		}
	}

	egress := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-egress"), testName+"-egress")
	if len(egress.Spec.Egress) != 2 {
		t.Fatalf("egress rules = %d, want 2", len(egress.Spec.Egress))
	}
	// DNS: the kube-dns pods (namespace AND pod selector in ONE peer)
	// plus the ipBlocks for resolvers outside the cluster. Port 25
	// egress must NOT be in the general egress rule (it is granted
	// only to the Postfix pods).
	dns := egress.Spec.Egress[0].To
	if len(dns) != 3 || dns[1].IPBlock == nil || dns[2].IPBlock == nil {
		t.Fatalf("dns peers = %v, want kube-dns + IPv4 + IPv6", dns)
	}
	if dns[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" ||
		dns[0].PodSelector == nil || dns[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Errorf("kube-dns peer = %+v", dns[0])
	}
	if portPresent(egress.Spec.Egress[0].Ports, render.PortPostfixInbound) {
		t.Error("general egress allows port 25; only the Postfix pods may send")
	}

	smtp := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-egress-smtp"), testName+"-egress-smtp")
	comp := smtp.Spec.PodSelector.MatchLabels[componentLabel]
	if comp != string(render.ComponentPostfix) {
		t.Errorf("egress-smtp podSelector component = %q", comp)
	}
}

// The public Rspamd services (fuzzy storage, maps) are allowed for the
// Rspamd pods only, to public addresses only.
func TestRspamdEgressPolicy(t *testing.T) {
	objs := build(t, nil)
	rspamdNP := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-egress-rspamd"), "egress-rspamd")
	if rspamdNP.Spec.PodSelector.MatchLabels[componentLabel] != string(render.ComponentRspamd) {
		t.Errorf("egress-rspamd podSelector = %+v", rspamdNP.Spec.PodSelector)
	}
	rule := rspamdNP.Spec.Egress[0]
	if len(rule.Ports) != 2 || rule.Ports[0].Port.IntVal != 443 || *rule.Ports[0].Protocol != corev1.ProtocolTCP ||
		rule.Ports[1].Port.IntVal != render.PortRspamdFuzzy || *rule.Ports[1].Protocol != corev1.ProtocolUDP {
		t.Errorf("egress-rspamd ports = %+v, want TCP 443 and UDP 11335", rule.Ports)
	}
	if len(rule.To) != 2 || rule.To[0].IPBlock.CIDR != "0.0.0.0/0" || rule.To[1].IPBlock.CIDR != "::/0" {
		t.Fatalf("egress-rspamd peers = %+v", rule.To)
	}
	// Private, shared (CGNAT), loopback and link-local (cloud metadata)
	// IPv4 ranges; unique-local and link-local IPv6.
	for _, cidr := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	} {
		if !slices.Contains(rule.To[0].IPBlock.Except, cidr) {
			t.Errorf("egress-rspamd IPv4 except %v misses %s", rule.To[0].IPBlock.Except, cidr)
		}
	}
	for _, cidr := range []string{"fc00::/7", "fe80::/10"} {
		if !slices.Contains(rule.To[1].IPBlock.Except, cidr) {
			t.Errorf("egress-rspamd IPv6 except %v misses %s", rule.To[1].IPBlock.Except, cidr)
		}
	}
}

// Only the server's own pods are selected: a policy that selects other
// pods would be additive to their policies (ADR 0006).
func TestNetworkPoliciesSelectOnlyOwnPods(t *testing.T) {
	objs := build(t, nil)
	for _, o := range objs {
		if np, ok := o.(*networkingv1.NetworkPolicy); ok {
			if np.Spec.PodSelector.MatchLabels[ServerLabel] != testName {
				t.Errorf("NetworkPolicy %s selects pods of other servers or namespaces: %+v", np.Name, np.Spec.PodSelector)
			}
		}
	}
}

// IMAP is only reachable when the Dovecot mailboxes exist.
func TestIngressOpensIMAPOnlyWithMailboxes(t *testing.T) {
	imap := func(objs []client.Object) bool {
		np := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, testName+"-ingress"), "ingress")
		return portPresent(np.Spec.Ingress[0].Ports, render.PortDovecotIMAPListen) &&
			portPresent(np.Spec.Ingress[0].Ports, render.PortDovecotIMAPSListen)
	}
	if imap(build(t, nil)) {
		t.Error("IMAP ports are open without mailboxes")
	}
	if !imap(build(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	})) {
		t.Error("IMAP ports are closed with mailboxes enabled")
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
	// The certificate carries the hostname and nothing else: not the
	// domains of the server, and not the in-cluster name, which public
	// ACME issuers reject (ADR 0014).
	if cn, _ := spec["commonName"].(string); cn != testHostname {
		t.Errorf("commonName = %q, want %q", cn, testHostname)
	}
	dns, _ := spec["dnsNames"].([]any)
	if len(dns) != 1 || dns[0] != testHostname {
		t.Errorf("dnsNames = %v, want [%s]", dns, testHostname)
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

// With every domain revoked the plane still builds, and the Certificate
// keeps the authorized hostname: revoking the last domain does not
// revoke the name of the server, and the certificate is not reissued.
func TestBuildWithoutAuthorizedDomains(t *testing.T) {
	dp := buildPlaneFor(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	}, nil, nil)
	cert := mustFind[*unstructured.Unstructured](t,
		find(t, dp.Base, kindCertificate, testName), testName)
	spec := cert.Object["spec"].(map[string]any)
	if cn, _ := spec["commonName"].(string); cn != testHostname {
		t.Errorf("commonName = %q, want %q", cn, testHostname)
	}
	if dns, _ := spec["dnsNames"].([]any); len(dns) != 1 || dns[0] != testHostname {
		t.Errorf("dnsNames = %v, want [%s]", dns, testHostname)
	}
	postfixCM := mustFind[*corev1.ConfigMap](t,
		find(t, dp.Configs, kindConfigMap, configMapNameOf(t, dp, render.ComponentPostfix)), "postfix")
	if strings.Contains(postfixCM.Data["main.cf"], "virtual_mailbox_domains") {
		t.Error("postfix main.cf still lists virtual domains")
	}
}

// While the hostname is not authorized the Certificate keeps only the
// in-cluster server name and has no common name; the domains stay.
func TestBuildWithoutAuthorizedHostname(t *testing.T) {
	dp := buildPlaneHost(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	}, nil, []string{domainB, domainA}, "")
	cert := mustFind[*unstructured.Unstructured](t,
		find(t, dp.Base, kindCertificate, testName), testName)
	spec := cert.Object["spec"].(map[string]any)
	if _, has := spec["commonName"]; has {
		t.Errorf("commonName = %v, want none", spec["commonName"])
	}
	want := "mail-postfix.tenant1.svc.cluster.local"
	if dns, _ := spec["dnsNames"].([]any); len(dns) != 1 || dns[0] != want {
		t.Errorf("dnsNames = %v, want [%s]", dns, want)
	}
	postfixCM := mustFind[*corev1.ConfigMap](t,
		find(t, dp.Configs, kindConfigMap, configMapNameOf(t, dp, render.ComponentPostfix)), "postfix")
	mainCF := postfixCM.Data["main.cf"]
	if !strings.Contains(mainCF, "virtual_mailbox_domains = "+domainA+", "+domainB) {
		t.Error("postfix main.cf does not serve the domains")
	}
	if !strings.Contains(mainCF, "myhostname = "+want+"\n") {
		t.Errorf("postfix main.cf myhostname is not %s", want)
	}
}

// Every workload records the domains it serves, outside the pod
// template so the annotation never rolls pods. The hostname is not a
// domain and is not recorded.
func TestBuildRecordsServedDomains(t *testing.T) {
	for _, tc := range []struct {
		name     string
		domains  []string
		hostname string
		want     string
	}{
		{"domains and hostname", []string{domainB, domainA}, testHostname, domainA + "," + domainB},
		{"hostname only", nil, testHostname, ""},
		{"domains only", []string{domainB, domainA}, "", domainA + "," + domainB},
		{"neither", nil, "", ""},
		{"hostname that is a domain", []string{domainA}, domainA, domainA},
	} {
		dp := buildPlaneHost(t, nil, nil, tc.domains, tc.hostname)
		if len(dp.Workloads) != 4 {
			t.Fatalf("workloads = %d, want 4", len(dp.Workloads))
		}
		for _, w := range dp.Workloads {
			got, ok := w.GetAnnotations()[ServedDomainsAnnotation]
			if !ok || got != tc.want {
				t.Errorf("%s, %s: %s = %q (set %t), want %q", tc.name, w.GetName(), ServedDomainsAnnotation, got, ok, tc.want)
			}
		}
	}
}

func configMapNameOf(t *testing.T, dp *DataPlane, c render.Component) string {
	t.Helper()
	for _, o := range dp.Configs {
		if cm, ok := o.(*corev1.ConfigMap); ok && cm.Labels[componentLabel] == string(c) {
			return cm.Name
		}
	}
	t.Fatalf("no ConfigMap for %s", c)
	return ""
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

func TestDovecotUsersLiveInAMutableSecret(t *testing.T) {
	user := render.Account{Name: "u", PasswordHash: testCryptHash, Address: "u@" + domainA}
	dp := buildPlane(t, nil, []render.Account{user})
	for _, o := range dp.Configs {
		if _, isSecret := o.(*corev1.Secret); isSecret {
			t.Errorf("Secret %s is hash-named: the users are not in the revision", o.GetName())
		}
		if cm, ok := o.(*corev1.ConfigMap); ok {
			if _, leaked := cm.Data["passdb"]; leaked {
				t.Errorf("ConfigMap %s carries the users file", cm.Name)
			}
		}
	}
	sec := dp.LiveUsers
	if sec == nil {
		t.Fatal("no live users Secret")
	}
	// Stable name, mutable, no content hash (ADR 0015, section 4.7).
	if sec.Name != testName+"-dovecot-users" || sec.Immutable != nil {
		t.Errorf("live users Secret %q (immutable %v) must have a stable name and be mutable", sec.Name, sec.Immutable)
	}
	if !strings.Contains(string(sec.Data["passdb"]), "{SHA512-CRYPT}x") || len(sec.Data["userdb"]) == 0 {
		t.Error("the live users Secret misses the passdb or the userdb file")
	}

	sts := mustFind[*appsv1.StatefulSet](t, find(t, dp.Workloads, kindStatefulSet, testName+"-dovecot"), "sts")
	pod := sts.Spec.Template.Spec
	usersVol := volumeByName(pod.Volumes, usersVolumeName)
	if usersVol == nil || usersVol.Secret == nil || usersVol.Secret.SecretName != sec.Name {
		t.Fatalf("dovecot pod must mount the users Secret %q: %v", sec.Name, usersVol)
	}
	// A whole directory: the kubelet never updates a subPath mount.
	m := mountByName(pod.Containers[0].VolumeMounts, usersVolumeName)
	if m == nil || m.MountPath != render.DovecotUsersDir || m.SubPath != "" || !m.ReadOnly {
		t.Errorf("users mount = %+v, want the read-only directory %s with no subPath", m, render.DovecotUsersDir)
	}
	if mode := usersVol.Secret.DefaultMode; mode == nil || *mode != 0o440 {
		t.Errorf("users Secret mode = %v, want 0440", mode)
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

	// A user change changes neither the pod-template hash nor any
	// object name: it is no revision (no Job, no rollout, no restart).
	other := buildPlane(t, nil, []render.Account{{Name: "v", PasswordHash: "{SHA512-CRYPT}y", Address: "v@" + domainA}})
	osts := mustFind[*appsv1.StatefulSet](t, find(t, other.Workloads, kindStatefulSet, testName+"-dovecot"), "sts")
	if osts.Spec.Template.Annotations[ConfigHashAnnotation] != sts.Spec.Template.Annotations[ConfigHashAnnotation] {
		t.Error("a user change must not change the pod-template hash")
	}
	if other.LiveUsers.Name != sec.Name || string(other.LiveUsers.Data["passdb"]) == string(sec.Data["passdb"]) {
		t.Error("a user change must change the content of the live Secret and keep its name")
	}
	// The Postfix maps do change (a revision, behind the validation Job); the
	// Dovecot, Rspamd and Valkey configuration does not.
	for i := range dp.Validations {
		if c := dp.Validations[i].Component; c != render.ComponentPostfix && dp.Validations[i].Hash != other.Validations[i].Hash {
			t.Errorf("%s: a user change gives a new validation Job", c)
		}
	}
	// No password hash is in the config hash, so no hash key is needed for
	// the users alone (TestBuildNeedsAHashKey).
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
	}, []render.Account{{Name: "u", PasswordHash: testCryptHash, Address: "u@" + domainA}})
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
	script := certReloadLoop("/tls/tls.crt", []string{postfixContainerName, "reload"})[2]
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

// Auth-only mode has no LMTP listener, so no policy allows it.
func TestAuthOnlyPolicyHasNoLMTPPort(t *testing.T) {
	objs := build(t, nil)
	for _, name := range []string{testName + "-ingress", testName + "-egress"} {
		np := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, name), name)
		var ports []networkingv1.NetworkPolicyPort
		if len(np.Spec.Ingress) > 1 {
			ports = np.Spec.Ingress[1].Ports
		} else {
			ports = np.Spec.Egress[1].Ports
		}
		if portPresent(ports, render.PortDovecotLMTPListen) || !portPresent(ports, render.PortDovecotAuth) {
			t.Errorf("%s component ports must hold the auth port and no LMTP port: %v", name, ports)
		}
	}
}

// servicePortMaps reports whether the named Service port maps port to
// the target port.
func servicePortMaps(svc *corev1.Service, name string, port, target int32) bool {
	for _, p := range svc.Spec.Ports {
		if p.Name == name {
			return p.Port == port && p.TargetPort.IntVal == target
		}
	}
	return false
}

// A NetworkPolicy matches the port behind the Service, so the
// same-server allow lists the Dovecot listen port, not the Service
// port.
func TestComponentPolicyUsesDovecotListenPorts(t *testing.T) {
	objs := build(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	})
	for _, name := range []string{testName + "-ingress", testName + "-egress"} {
		np := mustFind[*networkingv1.NetworkPolicy](t, find(t, objs, kindNetworkPolicy, name), name)
		var ports []networkingv1.NetworkPolicyPort
		if len(np.Spec.Ingress) > 1 {
			ports = np.Spec.Ingress[1].Ports
		} else {
			ports = np.Spec.Egress[1].Ports
		}
		if !portPresent(ports, render.PortDovecotLMTPListen) || portPresent(ports, render.PortDovecotLMTP) {
			t.Errorf("%s component ports must hold the LMTP listen port only: %v", name, ports)
		}
	}
}

// Dovecot cannot start without its certificate files: an init
// container waits for them, so the pod does not crash-loop before
// cert-manager has issued the Secret. The Secret volume stays optional
// so the pod can start at all.
func TestDovecotWaitsForTheCertificate(t *testing.T) {
	for _, mailboxes := range []bool{false, true} {
		objs := build(t, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(mailboxes)}
		})
		sts := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-dovecot"), "sts")
		pod := sts.Spec.Template.Spec
		if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != waitTLSContainerName {
			t.Fatalf("mailboxes=%v: init containers = %v, want %s", mailboxes, pod.InitContainers, waitTLSContainerName)
		}
		init := pod.InitContainers[0]
		script := strings.Join(init.Command, " ")
		for _, f := range []string{dovecotSSLDir() + "/" + TLSCertFile, dovecotSSLDir() + "/" + TLSKeyFile} {
			if !strings.Contains(script, f) {
				t.Errorf("wait script does not check %s: %s", f, script)
			}
		}
		if !hasMount(init.VolumeMounts, tlsVolumeName, dovecotSSLDir()) {
			t.Error("wait-tls misses the certificate mount")
		}
		for _, v := range pod.Volumes {
			if v.Name == tlsVolumeName && (v.Secret == nil || v.Secret.Optional == nil || !*v.Secret.Optional) {
				t.Error("the certificate Secret volume must stay optional")
			}
		}
	}
}

// The topology spread counts the pods of the workload's own component
// only: its selector is the component selector, which the pod
// template labels satisfy, and it excludes the other components.
func TestTopologySpreadSelectsOwnComponent(t *testing.T) {
	for _, pc := range allPods(t, true) {
		if pc.validation {
			continue
		}
		if len(pc.pod.TopologySpreadConstraints) != 1 {
			t.Fatalf("%s: topology spread constraints = %v", pc.name, pc.pod.TopologySpreadConstraints)
		}
		sel := pc.pod.TopologySpreadConstraints[0].LabelSelector.MatchLabels
		component := strings.TrimPrefix(pc.name, testName+"-")
		if sel[componentLabel] != component || sel[ServerLabel] != testName {
			t.Errorf("%s: spread selector = %v, want the %s component of %s", pc.name, sel, component, testName)
		}
	}
}

// A persistent Valkey volume is ReadWriteOnce: a rolling update would
// start the new pod while the old one still holds the volume, so the
// Deployment recreates instead.
func TestValkeyRecreatesWithPersistence(t *testing.T) {
	strategy := func(persistence bool) appsv1.DeploymentStrategyType {
		objs := build(t, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Valkey = &cnmsv1alpha1.Valkey{Persistence: new(persistence)}
			if persistence {
				s.Spec.Valkey.Storage = new(resource.MustParse("1Gi"))
			}
		})
		dep := mustFind[*appsv1.Deployment](t, find(t, objs, kindDeployment, testName+"-valkey"), "valkey")
		return dep.Spec.Strategy.Type
	}
	if got := strategy(true); got != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("persistent Valkey strategy = %q, want Recreate", got)
	}
	if got := strategy(false); got == appsv1.RecreateDeploymentStrategyType {
		t.Errorf("ephemeral Valkey strategy = %q, want the default rolling update", got)
	}
}

// The Rspamd budget allows one eviction at a time and never blocks a
// drain, whatever the replica count: maxUnavailable 1 allows an
// eviction even with a single replica (minAvailable 1 would not).
func TestRspamdPodDisruptionBudget(t *testing.T) {
	for _, replicas := range []int32{1, 2, 5} {
		objs := build(t, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Rspamd = &cnmsv1alpha1.Rspamd{Replicas: &replicas}
		})
		pdb := mustFind[*policyv1.PodDisruptionBudget](t, find(t, objs, kindPodDisruptionBudget, testName+"-rspamd"), "rspamd pdb")
		if pdb.Spec.MaxUnavailable == nil || pdb.Spec.MaxUnavailable.IntVal != 1 || pdb.Spec.MinAvailable != nil {
			t.Errorf("replicas=%d: PDB = %+v, want maxUnavailable 1 only", replicas, pdb.Spec)
		}
		sel := pdb.Spec.Selector.MatchLabels
		if sel[componentLabel] != string(render.ComponentRspamd) || sel[ServerLabel] != testName {
			t.Errorf("PDB selector = %v, want the Rspamd pods of %s", sel, testName)
		}
	}
}

// k8up annotations (spec.backup.enabled, default on): the Postfix
// queue and the Valkey volume are excluded, the Dovecot mail volume
// is included; with backup disabled no annotation is set. The managed
// labels are on the claim templates too.
func TestK8upAnnotations(t *testing.T) {
	annotations := func(backup *cnmsv1alpha1.Backup) (queue, mail, data map[string]string) {
		objs := build(t, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Backup = backup
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
			s.Spec.Valkey = &cnmsv1alpha1.Valkey{Persistence: new(true), Storage: new(resource.MustParse("1Gi"))}
		})
		pf := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-postfix"), "postfix")
		dv := mustFind[*appsv1.StatefulSet](t, find(t, objs, kindStatefulSet, testName+"-dovecot"), "dovecot")
		pvc := mustFind[*corev1.PersistentVolumeClaim](t, find(t, objs, kindPersistentVolumeClaim, testName+"-valkey-"+valkeyDataVolume), "valkey pvc")
		for _, tpl := range []corev1.PersistentVolumeClaim{pf.Spec.VolumeClaimTemplates[0], dv.Spec.VolumeClaimTemplates[0]} {
			if tpl.Labels[ManagedByLabel] != ManagedByValue || tpl.Labels[ServerLabel] != testName {
				t.Errorf("claim template %s misses the managed labels: %v", tpl.Name, tpl.Labels)
			}
		}
		return pf.Spec.VolumeClaimTemplates[0].Annotations, dv.Spec.VolumeClaimTemplates[0].Annotations, pvc.Annotations
	}

	for name, backup := range map[string]*cnmsv1alpha1.Backup{
		"default": nil, "enabled": {Enabled: new(true)}, "unset": {},
	} {
		queue, mail, data := annotations(backup)
		if queue[K8upBackupAnnotation] != "false" || data[K8upBackupAnnotation] != "false" || mail[K8upBackupAnnotation] != "true" {
			t.Errorf("%s: queue=%v mail=%v valkey=%v, want queue and valkey excluded, mail included", name, queue, mail, data)
		}
	}
	queue, mail, data := annotations(&cnmsv1alpha1.Backup{Enabled: new(false)})
	if len(queue) != 0 || len(mail) != 0 || len(data) != 0 {
		t.Errorf("backup disabled: annotations queue=%v mail=%v valkey=%v, want none", queue, mail, data)
	}
}

// testHashKey keys the secret digests of the planes the tests build.
var testHashKey = bytes.Repeat([]byte{7}, 32)

// testCryptHash stands in for the password hash of a test account.
const testCryptHash = "{SHA512-CRYPT}x"

// Every file the Postfix renderer produces is mounted into the pod and
// the validation Job: a table that main.cf names but that is not mounted
// would stop Postfix at its start.
func TestEveryPostfixFileIsMounted(t *testing.T) {
	req := &render.RenderRequest{
		Server:            testServer(t, nil),
		AuthorizedDomains: []string{domainA},
		Hostname:          testHostname,
		HashKey:           testHashKey,
	}
	res, err := postfix.New().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	mounts := configSubPathMounts()
	for file := range res.ConfigData {
		if !hasMount(mounts, configVolumeName, "/etc/postfix/"+file) {
			t.Errorf("the rendered Postfix file %s is not mounted", file)
		}
	}
}
