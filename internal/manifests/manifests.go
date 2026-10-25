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

// Package manifests builds the Kubernetes objects of a MailServer
// data plane from the rendered engine configuration (internal/render
// results) and the image pins (internal/images). The builder is a
// pure function: no cluster access, deterministic output, so a
// manifest is unit-testable without a cluster.
//
// The MailServer controller creates the objects with owner
// references back to the MailServer, so deleting the MailServer
// garbage-collects the whole data plane.
package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Pod-template and object conventions of the rendered data plane.
const (
	// ConfigHashAnnotation carries the SHA-256 of the rendered
	// component configuration on every pod template. A config change
	// changes the hash and rolls the pods; certificate changes are
	// excluded from the hash and trigger a reload (not a rollout) via
	// the cert-reload sidecar (D5).
	ConfigHashAnnotation = "cnms.rpp.gmbh/config-hash"

	// ManagedByLabel marks CNMS-managed resources; ADR 0006 excludes
	// labeled pods from the namespace port-25 egress deny.
	ManagedByLabel = "cnms.rpp.gmbh/managed-by"

	// appLabel marks the app family of the rendered resources.
	appLabel = "app.kubernetes.io/name"
	// instanceLabel carries the MailServer name.
	instanceLabel = "app.kubernetes.io/instance"
	// componentLabel carries the data-plane component.
	componentLabel = "app.kubernetes.io/component"
	// managedByAppLabel is the standard "managed by" app label.
	managedByAppLabel = "app.kubernetes.io/managed-by"

	// appValue is the app family value on all rendered resources.
	appValue = "cloud-native-mail-service"
	// managedByValue marks a resource as CNMS-managed.
	managedByValue = "cnms"

	// serverLabel carries the MailServer name on pods and policies.
	serverLabel = "cnms.rpp.gmbh/mailserver"

	// tlsCertFile and tlsKeyFile are the cert-manager standard keys.
	tlsCertFile = "tls.crt"
	tlsKeyFile  = "tls.key"

	// postfixQueueVolume is the StatefulSet volumeClaimTemplate name
	// of the per-replica queue volume.
	postfixQueueVolume = "postfix-queue"
	// dovecotMailVolume is the StatefulSet volumeClaimTemplate name
	// of the Dovecot mail storage volume.
	dovecotMailVolume = "mail"
	// valkeyDataVolume is the name of the Valkey data volume (PVC or
	// emptyDir, per spec.valkey.persistence).
	valkeyDataVolume = "valkey-data"

	// dovecotRunDir is the Dovecot runtime directory (master state,
	// sockets) on an emptyDir shared with the cert-reload sidecar.
	dovecotRunDir = "/var/run/dovecot"

	// Component container and volume names.
	validateContainerName     = "validate"
	queueMetricsContainerName = "queue-metrics"
	certReloadContainerName   = "cert-reload"
	configVolumeName          = "config"
	tlsVolumeName             = "tls"
	stateVolumeName           = "state"
	runVolumeName             = "run"
	// valkeyPortName names the Valkey service and container port.
	valkeyPortName = "valkey"

	// postfixTLSDir is the Postfix certificate directory (matches the
	// rendered main.cf, ADR 0004).
	postfixTLSDir = "/etc/postfix/tls"
	// rspamdStateDir is the Rspamd on-disk module state directory
	// (ADR 0004: emptyDir; shared state lives in Valkey).
	rspamdStateDir = "/var/lib/rspamd"

	// Postfix queue group UID (images/postfix Dockerfile): the pod
	// fsGroup so the postfix daemons can write the queue volume.
	postfixQueueGroup int64 = 5001
	// Postfix user UID (queue-metrics sidecar runs as the postfix
	// user per ADR 0005).
	postfixUserUID int64 = 5001
	// Dovecot vmail UID/GID (the dovecot image runs rootless as
	// vmail): the pod fsGroup for /var/run/dovecot and the mail
	// volume.
	dovecotVmailID int64 = 1000
	// Rspamd user GID (the image runs as _rspamd, uid/gid 11333):
	// the pod fsGroup for the state emptyDir.
	rspamdGroupID int64 = 11333
)

// Build produces every object of the data plane of one MailServer.
// req must carry at least one authorized domain; renders must
// contain a result for every data-plane component. The returned
// list is deterministic: Services, the Valkey PVC and the
// Certificate come before the workloads that reference them.
func Build(req *render.RenderRequest, renders map[render.Component]*render.RenderResult) ([]client.Object, error) {
	server := req.Server
	if server == nil {
		return nil, errors.New("render request without MailServer")
	}
	if len(req.AuthorizedDomains) == 0 {
		return nil, errors.New("no authorized domains")
	}
	var missing []render.Component
	for _, c := range render.AllComponents() {
		if renders[c] == nil {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing render results: %v", missing)
	}

	var objs []client.Object
	// ConfigMaps.
	for _, c := range render.AllComponents() {
		objs = append(objs, configMap(server, c, renders[c]))
	}
	// Services (incl. headless) before the workloads that use them.
	objs = append(objs, services(server)...)
	if valkeyPersistence(server) {
		objs = append(objs, valkeyPersistentVolumeClaim(server))
	}
	// Certificate (cert-manager; the certificate Secret is the
	// source for the in-place-reloaded TLS mounts).
	objs = append(objs, certificate(req))
	// Workloads.
	objs = append(objs,
		postfixStatefulSet(server, renders[render.ComponentPostfix]),
		dovecotStatefulSet(server, renders[render.ComponentDovecot]),
		rspamdDeployment(server, renders[render.ComponentRspamd]),
		valkeyDeployment(server, renders[render.ComponentValkey]))
	// Network policies and the disruption budget (ADR 0006).
	objs = append(objs, networkPolicies(server)...)
	objs = append(objs, postfixPodDisruptionBudget(server))
	return objs, nil
}

// ServiceMonitors returns the monitoring.coreos.com/v1
// ServiceMonitors (as unstructured, so the operator works without
// the Prometheus Operator CRDs, principle 10) for the metrics
// endpoints of the MailServer. The controller creates them only
// when the CRDs are discovered available and the observability
// setting allows (ADR 0005).
func ServiceMonitors(server *cnmsv1alpha1.MailServer) []client.Object {
	mk := func(name string, c render.Component, port string) *unstructured.Unstructured {
		return unstructuredObj("monitoring.coreos.com/v1", "ServiceMonitor",
			name, server.Namespace, objectLabels(server, c), map[string]any{
				"namespaceSelector": map[string]any{
					"matchNames": []any{server.Namespace},
				},
				"selector": map[string]any{
					"matchLabels": map[string]any{
						appLabel:       appValue,
						serverLabel:    server.Name,
						componentLabel: string(c),
					},
				},
				"endpoints": []any{
					map[string]any{"port": port, "interval": "30s"},
				},
			})
	}
	return []client.Object{
		mk(render.ServiceName(server.Name, render.ComponentPostfix)+"-metrics",
			render.ComponentPostfix, "cnms-postfix-metrics"),
		mk(render.ServiceName(server.Name, render.ComponentRspamd),
			render.ComponentRspamd, "cnms-rspamd-metrics"),
		mk(render.ServiceName(server.Name, render.ComponentValkey)+"-metrics",
			render.ComponentValkey, "cnms-valkey-metrics"),
	}
}

// unstructuredObj builds a minimal unstructured object (metadata +
// spec) for API groups the operator integrates with optionally
// (cert-manager, Prometheus Operator).
func unstructuredObj(apiVersion, kind, name, namespace string, l map[string]string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace, "labels": l},
		"spec":       spec,
	}}
}

// configMap places the rendered configuration of a component in a
// ConfigMap named "<server>-<component>".
func configMap(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            render.ServiceName(server.Name, c),
			Namespace:       server.Namespace,
			Labels:          objectLabels(server, c),
			OwnerReferences: nil, // set by the controller
		},
		Data: r.ConfigData,
	}
}

// objectLabels returns the label set for a component object.
func objectLabels(server *cnmsv1alpha1.MailServer, c render.Component) map[string]string {
	l := map[string]string{
		appLabel:          appValue,
		instanceLabel:     server.Name,
		managedByAppLabel: managedByValue,
		ManagedByLabel:    managedByValue,
		serverLabel:       server.Name,
	}
	if c != "" {
		l[componentLabel] = string(c)
	}
	return l
}

// podSelector matches all pods of one MailServer.
func serverSelector(server *cnmsv1alpha1.MailServer) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{
			appLabel:    appValue,
			serverLabel: server.Name,
		},
	}
}

// componentSelector matches the pods of one component of one
// MailServer (also used as Service selector).
func componentSelector(server *cnmsv1alpha1.MailServer, c render.Component) *metav1.LabelSelector {
	sel := serverSelector(server)
	sel.MatchLabels[componentLabel] = string(c)
	return sel
}

// configHash is the deterministic SHA-256 of a rendered
// configuration (canonical key order). The certificate is not part
// of the render, so renewals do not change the hash (D5).
func configHash(r *render.RenderResult) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(r.ConfigData)) {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(r.ConfigData[k])
		b.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// podAnnotations adds the config-hash annotation to a pod template.
func podAnnotations(r *render.RenderResult) map[string]string {
	return map[string]string{ConfigHashAnnotation: configHash(r)}
}

// tlsSecretName is the name of the TLS certificate Secret
// (spec.tls.secretName or "<name>-tls").
func tlsSecretName(server *cnmsv1alpha1.MailServer) string {
	if server.Spec.TLS.SecretName != nil && *server.Spec.TLS.SecretName != "" {
		return *server.Spec.TLS.SecretName
	}
	return server.Name + "-tls"
}

// default helpers (mirror the kubebuilder defaults of the API).

func int32Value(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

func boolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func strValue(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

func quantityValue(p *resource.Quantity, def resource.Quantity) resource.Quantity {
	if p == nil || p.IsZero() {
		return def
	}
	return *p
}

// component helpers.

func submissionEnabled(server *cnmsv1alpha1.MailServer) bool {
	if server.Spec.Submission == nil {
		return true // API default
	}
	return boolValue(server.Spec.Submission.Enabled, true)
}

func mailboxesEnabled(server *cnmsv1alpha1.MailServer) bool {
	if server.Spec.Mailboxes == nil {
		return false // API default
	}
	return boolValue(server.Spec.Mailboxes.Enabled, false)
}

func valkeyPersistence(server *cnmsv1alpha1.MailServer) bool {
	if server.Spec.Valkey == nil {
		return false // API default
	}
	return boolValue(server.Spec.Valkey.Persistence, false)
}

// topologySpreadConstraints builds the constraints from
// spec.availability.topologySpread (or the API defaults) for all
// data-plane pods of the MailServer.
func topologySpreadConstraints(server *cnmsv1alpha1.MailServer) []corev1.TopologySpreadConstraint {
	var maxSkew int32 = 1
	var topologyKey = "kubernetes.io/hostname"
	if server.Spec.Availability != nil && server.Spec.Availability.TopologySpread != nil {
		ts := server.Spec.Availability.TopologySpread
		maxSkew = int32Value(ts.MaxSkew, 1)
		topologyKey = strValue(ts.TopologyKey, topologyKey)
	}
	return []corev1.TopologySpreadConstraint{{
		MaxSkew:           maxSkew,
		TopologyKey:       topologyKey,
		WhenUnsatisfiable: corev1.ScheduleAnyway,
		LabelSelector:     serverSelector(server),
	}}
}

// certReloadLoop is the command of the certificate-reload sidecar:
// it polls the hash of the mounted certificate and runs the engine
// reload command when it changes (renewal => reload, not rollout).
// The first observation is recorded without reloading, and a failed
// reload is retried on the next loop.
func certReloadLoop(certFile string, reload []string) []string {
	script := `set -eu
cert=` + certFile + `
last=""
while :; do
  sleep 30
  [ -e "$cert" ] || continue
  now=$(sha256sum "$cert" | cut -d' ' -f1)
  if [ "$now" != "$last" ]; then
    if ` + strings.Join(reload, " ") + `; then
      last=$now
    else
      echo "certificate reload failed; retrying"
    fi
  fi
done`
	return []string{"sh", "-c", script}
}

// portProbe builds a TCP socket probe for readiness/liveness.
func portProbe(port int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		},
	}
}
