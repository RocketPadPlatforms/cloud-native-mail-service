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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
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

	// ServedDomainsAnnotation is set on the workload objects (not the
	// pod templates, so it never rolls pods) and lists, comma
	// separated and sorted, the domains the revision applied with it
	// serves. The controller compares it with the authorized set to
	// find out whether a revoked domain is still being served.
	ServedDomainsAnnotation = "cnms.rpp.gmbh/served-domains"

	// ManagedByLabel marks CNMS-managed resources. It is a label for
	// listing and cleanup only: tenants can set it on their own pods,
	// so no security decision may rest on it (ADR 0006).
	ManagedByLabel = "cnms.rpp.gmbh/managed-by"

	// appLabel marks the app family of the rendered resources.
	appLabel = "app.kubernetes.io/name"
	// InstanceLabelKey carries the MailServer name.
	InstanceLabelKey = "app.kubernetes.io/instance"
	// componentLabel carries the data-plane component.
	componentLabel = "app.kubernetes.io/component"
	// managedByAppLabel is the standard "managed by" app label.
	managedByAppLabel = "app.kubernetes.io/managed-by"

	// appValue is the app family value on all rendered resources.
	appValue = "cloud-native-mail-service"
	// ManagedByValue is the value of ManagedByLabel and of the
	// managed-by app label of CNMS-managed resources.
	ManagedByValue = "cnms"

	// K8upBackupAnnotation includes ("true") or excludes ("false") a
	// PersistentVolumeClaim from k8up file-level backups. Verified
	// against the k8up documentation (Annotations Reference and Backup
	// how-to, 2.16): `k8up.io/backup` takes the strings 'true' and
	// 'false' on a Pod or PVC; without it k8up backs up every PVC
	// unless the operator runs with BACKUP_SKIP_WITHOUT_ANNOTATION.
	K8upBackupAnnotation = "k8up.io/backup"

	// ServerLabel carries the MailServer name on all its objects.
	ServerLabel = "cnms.rpp.gmbh/mailserver"

	// TLSCertFile and TLSKeyFile are the cert-manager standard keys.
	TLSCertFile = "tls.crt"
	TLSKeyFile  = "tls.key"

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
	waitTLSContainerName      = "wait-tls"
	postfixContainerName      = "postfix"
	dovecotContainerName      = "dovecot"
	tmpDir                    = "/tmp"
	rspamdMetricsContainer    = "rspamd-metrics"
	tmpVolumeName             = "tmp"
	configVolumeName          = "config"
	usersVolumeName           = "users"
	directoryVolumeName       = "directory"
	dkimVolumeName            = "dkim"
	tlsVolumeName             = "tls"
	stateVolumeName           = "state"
	runVolumeName             = "run"
	// valkeyPortName names the Valkey service and container port.
	valkeyPortName = "valkey"

	// postfixTLSDir is the Postfix certificate directory (matches the
	// rendered main.cf, ADR 0004).
	postfixTLSDir = "/etc/postfix/tls"
	// postfixDataVolume and postfixDataDir are the Postfix data_directory
	// (master lock, TLS PRNG state) on an emptyDir: the root filesystem
	// is read-only.
	postfixDataVolume = "postfix-data"
	postfixDataDir    = "/var/lib/postfix"
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
	// Rspamd user and group (the image runs as _rspamd, uid/gid
	// 11333): the group is the pod fsGroup for the state emptyDir.
	rspamdUserID  int64 = 11333
	rspamdGroupID int64 = 11333
	// Valkey user and group (the image's valkey account, uid/gid 999):
	// the container runs as this user directly and the pod fsGroup
	// makes the data volume writable for it (the image entrypoint
	// would chown it as root).
	valkeyID int64 = 999
	// nobodyID is the unprivileged user of the Rspamd metrics sidecar.
	nobodyID int64 = 65534
)

// Validation is the config validation Job of one component for one
// rendered configuration.
type Validation struct {
	Component render.Component
	// Workload is the name of the workload the configuration is for.
	Workload string
	// Hash is the config hash (the pod template annotation value).
	Hash string
	Job  *batchv1.Job
}

// DataPlane is the object set of one MailServer, split by rollout
// stage (ADR 0009): the immutable configuration is created first,
// the Jobs validate it, and the workloads that reference it are
// applied only after validation passed.
type DataPlane struct {
	// Configs are the immutable, hash-named ConfigMaps and Secrets.
	Configs []client.Object
	// Validations are the validation Jobs, one per component.
	Validations []Validation
	// Base are the objects that do not depend on a validated
	// configuration: Services, the Valkey PVC, the Certificate,
	// NetworkPolicies and the PodDisruptionBudget.
	Base []client.Object
	// Workloads are the StatefulSets and Deployments.
	Workloads []client.Object
	// LiveUsers is the Secret with the passdb and userdb files of Dovecot
	// (ADR 0015, section 4.7). It has a stable name and is updated in place,
	// outside the config hash: the pods mount the object by name, so a
	// change of its content is no new revision and no rollout. The
	// controller writes it before the workloads and outside the validation
	// gate.
	LiveUsers *corev1.Secret
}

// Build produces every object of the data plane of one MailServer.
// req may carry no authorized domain (all revoked): the configuration
// is then fail closed (see render.RenderRequest) and the Certificate
// keeps only the in-cluster server name. renders must contain a result
// for every data-plane component. The output is deterministic.
func Build(req *render.RenderRequest, renders map[render.Component]*render.RenderResult) (*DataPlane, error) {
	server := req.Server
	if server == nil {
		return nil, errors.New("render request without MailServer")
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

	// The secret files are named and hashed under the key of the server,
	// never by a plain hash of their content.
	for c, r := range renders {
		if HasSecretContent(r) && len(req.HashKey) == 0 {
			return nil, fmt.Errorf("the %s secret files need a hash key", c)
		}
		r.SecretDigest = secretDigest(req.HashKey, r.SecretData)
	}

	dp := &DataPlane{LiveUsers: LiveUsersSecret(server, renders[render.ComponentDovecot].LiveData)}
	for _, c := range render.AllComponents() {
		dp.Configs = append(dp.Configs, configMap(server, c, renders[c]))
		if s := secret(server, c, renders[c]); s != nil {
			dp.Configs = append(dp.Configs, s)
		}
		dp.Validations = append(dp.Validations, validation(server, c, renders[c]))
	}
	// Services (incl. headless) before the workloads that use them.
	dp.Base = append(dp.Base, services(server)...)
	if valkeyPersistence(server) {
		dp.Base = append(dp.Base, valkeyPersistentVolumeClaim(server))
	}
	// Certificate (cert-manager; the certificate Secret is the
	// source for the in-place-reloaded TLS mounts).
	dp.Base = append(dp.Base, certificate(req))
	// Network policies and the disruption budget (ADR 0006).
	dp.Base = append(dp.Base, networkPolicies(server)...)
	dp.Base = append(dp.Base, postfixPodDisruptionBudget(server), rspamdPodDisruptionBudget(server))
	dp.Workloads = []client.Object{
		postfixStatefulSet(server, renders[render.ComponentPostfix]),
		dovecotStatefulSet(server, renders[render.ComponentDovecot]),
		rspamdDeployment(server, renders[render.ComponentRspamd], dkimVolume(req)),
		valkeyDeployment(server, renders[render.ComponentValkey]),
	}
	domains := slices.Clone(req.AuthorizedDomains)
	slices.Sort(domains)
	// The hostname is not recorded: a renamed or revoked hostname never
	// makes the data plane fail closed (ADR 0014).
	served := map[string]string{ServedDomainsAnnotation: strings.Join(slices.Compact(domains), ",")}
	for _, w := range dp.Workloads {
		w.SetAnnotations(maps.Clone(served))
	}
	return dp, nil
}

// Certificate renders the Certificate of Build alone. It needs only the
// server and the hostname of the request, so the controller can
// re-apply it while the rest of the render is blocked.
func Certificate(req *render.RenderRequest) *unstructured.Unstructured {
	return certificate(req)
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
						ServerLabel:    server.Name,
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
			render.ComponentPostfix, "postfix-metrics"),
		mk(render.ServiceName(server.Name, render.ComponentRspamd),
			render.ComponentRspamd, rspamdMetricsContainer),
		mk(render.ServiceName(server.Name, render.ComponentValkey)+"-metrics",
			render.ComponentValkey, "valkey-metrics"),
	}
}

// unstructuredObj builds a minimal unstructured object (metadata +
// spec) for API groups the operator integrates with optionally
// (cert-manager, Prometheus Operator).
func unstructuredObj(apiVersion, kind, name, namespace string, l map[string]string, spec map[string]any) *unstructured.Unstructured {
	// Labels must be map[string]any inside an unstructured object:
	// DeepCopy panics on map[string]string values.
	labels := make(map[string]any, len(l))
	for k, v := range l {
		labels[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace, "labels": labels},
		"spec":       spec,
	}}
}

// configMap places the rendered configuration of a component in an
// immutable ConfigMap named by the content hash. A config change
// creates a new object; the pods of the old revision keep theirs.
func configMap(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName(server, c, r),
			Namespace: server.Namespace,
			Labels:    objectLabels(server, c),
			// The owner reference is set by the controller.
		},
		Immutable: new(true),
		Data:      r.ConfigData,
	}
}

// secret places the rendered secret files of a component (nil when
// there are none) in an immutable Secret named by the content hash.
func secret(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) *corev1.Secret {
	if len(r.SecretData) == 0 {
		return nil
	}
	data := make(map[string][]byte, len(r.SecretData))
	for k, v := range r.SecretData {
		data[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName(server, c, r),
			Namespace: server.Namespace,
			Labels:    objectLabels(server, c),
		},
		Immutable: new(true),
		Data:      data,
	}
}

// objectLabels returns the label set for a component object.
func objectLabels(server *cnmsv1alpha1.MailServer, c render.Component) map[string]string {
	l := map[string]string{
		appLabel:          appValue,
		InstanceLabelKey:  server.Name,
		managedByAppLabel: ManagedByValue,
		ManagedByLabel:    ManagedByValue,
		ServerLabel:       server.Name,
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
			ServerLabel: server.Name,
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

// dataHash is the deterministic SHA-256 of rendered files (canonical
// key order).
func dataHash(data map[string]string) string {
	sum := sha256.Sum256(canonical(data))
	return hex.EncodeToString(sum[:])
}

// canonical is the files in key order, the encoding both hashes cover.
func canonical(data map[string]string) []byte {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(data)) {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(data[k])
		b.WriteByte(0)
	}
	return []byte(b.String())
}

// secretDigest is the digest of the secret files of a render: HMAC-SHA256
// under key, so the digest is useless for guessing a password offline.
// Without secret content (no files, or files of comments only, such as the
// users file of a server without accounts) there is nothing to protect and
// it is the plain hash, as before.
func secretDigest(key []byte, data map[string]string) string {
	if !hasContent(data) {
		return dataHash(data)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(canonical(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// hasContent reports whether a file holds anything but comments and blank
// lines: the users file of a server without accounts is only a header.
func hasContent(data map[string]string) bool {
	for _, v := range data {
		for line := range strings.Lines(v) {
			if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
				return true
			}
		}
	}
	return false
}

// HasSecretContent reports whether the secret files of a render hold
// anything, that is whether Build needs the hash key for it.
func HasSecretContent(r *render.RenderResult) bool {
	return hasContent(r.SecretData)
}

// digestOf is the digest Build recorded; a result that did not pass
// through Build has no secret files worth keying (the tests of the hash).
func digestOf(r *render.RenderResult) string {
	if r.SecretDigest != "" {
		return r.SecretDigest
	}
	return dataHash(r.SecretData)
}

// configHash is the hash of a whole rendered component: its engine
// image, its ConfigMap files and, if any, its Secret files. It is the
// pod template annotation and names the validation Job, so an image
// bump is validated again with the new image. The certificate is not
// part of the render, so renewals do not change the hash (D5).
func configHash(c render.Component, r *render.RenderResult) string {
	h := componentImage(c) + "\x00" + dataHash(r.ConfigData) + digestOf(r)
	if len(r.Env) > 0 {
		// The environment changes the pod template; an empty one leaves
		// the hash of earlier revisions as it was.
		h += "\x00env\x00" + dataHash(r.Env)
	}
	sum := sha256.Sum256([]byte(h))
	return hex.EncodeToString(sum[:])
}

// hashSuffix is the short hash in the names of immutable objects.
func hashSuffix(hash string) string {
	return hash[:8]
}

// configMapName is the name of the immutable ConfigMap of a rendered
// component: "<server>-<component>-<hash8>" over the ConfigData.
func configMapName(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) string {
	return render.ServiceName(server.Name, c) + "-" + hashSuffix(dataHash(r.ConfigData))
}

// secretName is the name of the immutable Secret of a rendered
// component: "<server>-<component>-<hash8>" over the SecretData.
func secretName(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) string {
	return render.ServiceName(server.Name, c) + "-" + hashSuffix(digestOf(r))
}

// podAnnotations adds the config-hash annotation to a pod template.
func podAnnotations(c render.Component, r *render.RenderResult) map[string]string {
	return map[string]string{ConfigHashAnnotation: configHash(c, r)}
}

// TLSSecretName is the name of the TLS certificate Secret
// (spec.tls.secretName or "<name>-tls").
func TLSSecretName(server *cnmsv1alpha1.MailServer) string {
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

// backupEnabled reports whether the managed volumes carry k8up
// annotations (spec.backup.enabled, default true).
func backupEnabled(server *cnmsv1alpha1.MailServer) bool {
	if server.Spec.Backup == nil {
		return true // API default
	}
	return boolValue(server.Spec.Backup.Enabled, true)
}

// backupAnnotations is the k8up annotation set of a volume: nil when
// the MailServer does not manage them, else include says whether the
// volume is backed up. Only PVCs are annotated: pods would need the
// annotation for application-aware backups (k8up.io/backupcommand),
// which CNMS does not use yet, and a pod annotation would roll the
// pods whenever spec.backup.enabled changes.
func backupAnnotations(server *cnmsv1alpha1.MailServer, include bool) map[string]string {
	if !backupEnabled(server) {
		return nil
	}
	return map[string]string{K8upBackupAnnotation: strconv.FormatBool(include)}
}

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
// spec.availability.topologySpread (or the API defaults) for the pods
// of one component. The selector counts only the pods of that
// component: with the whole MailServer, the skew would be measured
// across Postfix, Rspamd, Dovecot and Valkey together, which spreads
// the replicas of one component badly.
func topologySpreadConstraints(server *cnmsv1alpha1.MailServer, c render.Component) []corev1.TopologySpreadConstraint {
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
		LabelSelector:     componentSelector(server, c),
	}}
}

// certReloadLoop is the command of the certificate-reload sidecar:
// it polls the hash of the mounted certificate and runs the engine
// reload command when it changes (renewal => reload, not rollout).
// The hash at container start is the baseline, so the first pass does
// not reload (the engine just read that certificate); a certificate
// that appears later (optional Secret) counts as a change. A failed
// reload is retried on the next loop.
func certReloadLoop(certFile string, reload []string) []string {
	script := `set -eu
cert=` + certFile + `
sum() { if [ -e "$cert" ]; then sha256sum "$cert" | cut -d' ' -f1; fi; }
last=$(sum)
while :; do
  sleep 30
  now=$(sum)
  [ -n "$now" ] || continue
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

// waitForTLSFiles is the command of the Dovecot init container: it
// returns once the certificate and key files exist and are not empty.
// It waits without a limit (a missing certificate is not an error of
// the pod) and logs once a minute so `kubectl logs -c wait-tls` shows
// why the pod is in Init.
func waitForTLSFiles(dir string) []string {
	script := `set -eu
n=0
until [ -s ` + dir + "/" + TLSCertFile + ` ] && [ -s ` + dir + "/" + TLSKeyFile + ` ]; do
  [ $((n % 6)) -ne 0 ] || echo "Waiting for the TLS certificate Secret"
  n=$((n + 1))
  sleep 10
done
echo "TLS certificate found"`
	return []string{"sh", "-c", script}
}

// rspamdMetricsLoop is the command of the Rspamd metrics sidecar
// (ADR 0005). The controller worker only answers loopback clients
// without a password, so the sidecar copies GET /metrics every 15s
// over loopback and serves the copy on listenPort; no other
// controller endpoint becomes reachable. The body is prefixed with
// cnms_rspamd_metrics_up (0 when the last copy failed, in which case
// the last good body is kept) so stale data is detectable. The file is
// served as /metrics/index.txt because httpd sends no Content-Type for
// a file without an extension and Prometheus 3 rejects that (same as
// the Postfix queue-metrics sidecar). Only $dir/www is the document
// root: the httpd config and the working copies stay outside it, so
// /metrics/ is the only thing served. `busybox` is called by name: the
// image does not symlink the applets.
func rspamdMetricsLoop(listenPort, controllerPort int32) []string {
	script := fmt.Sprintf(`set -eu
dir=/tmp/rspamd-metrics
mkdir -p "$dir/www/metrics"
printf 'I:index.txt\n.txt:text/plain\n' > "$dir/httpd.conf"
scrape() {
  if busybox wget -q -T 5 -O "$dir/new" http://127.0.0.1:%[2]d/metrics; then
    up=1
    mv "$dir/new" "$dir/last"
  else
    up=0
    rm -f "$dir/new"
  fi
  {
    echo "# HELP cnms_rspamd_metrics_up Whether the last copy of the Rspamd metrics succeeded."
    echo "# TYPE cnms_rspamd_metrics_up gauge"
    echo "cnms_rspamd_metrics_up $up"
    if [ -f "$dir/last" ]; then cat "$dir/last"; fi
  } > "$dir/www/metrics/.index.tmp"
  mv "$dir/www/metrics/.index.tmp" "$dir/www/metrics/index.txt"
}
scrape
busybox httpd -f -p 0.0.0.0:%[1]d -h "$dir/www" -c "$dir/httpd.conf" &
srv=$!
trap 'exit 0' TERM INT
while :; do
  sleep 15 &
  wait $!
  kill -0 "$srv"
  scrape
done`, listenPort, controllerPort)
	return []string{"sh", "-c", script}
}

// startupPortProbe is a TCP startup probe that allows 150s (30 x 5s)
// for the first start; readiness and liveness begin after it passes.
func startupPortProbe(port int32) *corev1.Probe {
	p := portProbe(port)
	p.PeriodSeconds = 5
	p.FailureThreshold = 30
	return p
}

// portProbe builds a TCP socket probe for readiness/liveness.
func portProbe(port int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		},
	}
}

// HashKeyData is the data key of the hash key Secret.
const HashKeyData = "key"

// HashKeySecretName is the name of the Secret that holds the random key
// of the secret-file digests of a MailServer (see render.RenderRequest).
func HashKeySecretName(server *cnmsv1alpha1.MailServer) string {
	return server.Name + "-hash-key"
}

// HashKeySecret is the immutable Secret with the hash key. It is created
// once and never changed; losing it only changes the names and hashes of
// the next revision, which costs one rollout.
func HashKeySecret(server *cnmsv1alpha1.MailServer, key []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      HashKeySecretName(server),
			Namespace: server.Namespace,
			Labels:    objectLabels(server, ""),
		},
		Immutable: new(true),
		Data:      map[string][]byte{HashKeyData: key},
	}
}

// LiveUsersSecretName is the name of the live users Secret of Dovecot (ADR
// 0015, section 4.7): stable, so that the pods mount it by name and an
// update of its content needs no rollout.
func LiveUsersSecretName(server *cnmsv1alpha1.MailServer) string {
	return render.ServiceName(server.Name, render.ComponentDovecot) + "-users"
}

// LiveUsersSecret is the mutable Secret with the passdb and userdb files.
// It is not immutable and not named by its content; the owner reference is
// set by the controller, which updates it in place with the resourceVersion
// it read.
func LiveUsersSecret(server *cnmsv1alpha1.MailServer, files map[string]string) *corev1.Secret {
	data := make(map[string][]byte, len(files))
	for k, v := range files {
		data[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      LiveUsersSecretName(server),
			Namespace: server.Namespace,
			Labels:    objectLabels(server, render.ComponentDovecot),
		},
		Data: data,
	}
}
