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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstr "k8s.io/apimachinery/pkg/util/intstr"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Postfix termination grace (seconds): generous, but a grace period
// never drains a queue (deferred mail may stay queued for days; see
// the HA section of the project plan).
const postfixTerminationGrace int64 = 120

// postfixStatefulSet renders the Postfix workload: StatefulSet, one
// per-replica queue PVC, ordered one-at-a-time updates gated on
// readiness (k8s default for StatefulSets) plus the PDB.
func postfixStatefulSet(server *cnmsv1alpha1.MailServer, r *render.RenderResult) *appsv1.StatefulSet {
	name := render.ServiceName(server.Name, render.ComponentPostfix)
	replicas := int32(2)
	queueStorage := resource.MustParse("1Gi")
	if server.Spec.Postfix != nil {
		replicas = int32Value(server.Spec.Postfix.Replicas, replicas)
		queueStorage = quantityValue(server.Spec.Postfix.QueueStorage, queueStorage)
	}

	var submissionPorts []corev1.ContainerPort
	if submissionEnabled(server) {
		submissionPorts = append(submissionPorts, corev1.ContainerPort{
			Name: "submission", ContainerPort: render.PortPostfixSubmission,
		})
	}

	return &appsv1.StatefulSet{
		ObjectMeta: objectMeta(server, name, render.ComponentPostfix),
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: name + "-headless",
			Selector:    selectorMeta(componentSelector(server, render.ComponentPostfix)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentPostfix),
					Annotations: podAnnotations(r),
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: new(postfixTerminationGrace),
					SecurityContext: &corev1.PodSecurityContext{
						// The queue volume is group-owned by the
						// postfix group (postfixQueueGroup) so the
						// daemons can write it.
						FSGroup: new(postfixQueueGroup),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server),
					Volumes: []corev1.Volume{
						configVolume(name),
						tlsVolume(TLSSecretName(server)),
						{
							Name:         postfixQueueVolume,
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: postfixQueueVolume}},
						},
					},
					InitContainers: []corev1.Container{{
						// The rendered configuration gate: a bad
						// config never replaces a working one (D4).
						Name:         validateContainerName,
						Image:        images.Postfix,
						Command:      r.ValidateCommand,
						VolumeMounts: configSubPathMounts("main.cf", "master.cf"),
					}},
					Containers: []corev1.Container{{
						Name:  "postfix",
						Image: images.Postfix, // default CMD: the entrypoint
						Ports: append([]corev1.ContainerPort{{Name: "smtp", ContainerPort: render.PortPostfixInbound}}, submissionPorts...),
						VolumeMounts: append(configSubPathMounts("main.cf", "master.cf"),
							// smtpd loads the certificate from this
							// path (the sidecar's mount is a
							// different filesystem).
							tlsMount(),
							queueMount()),
						// The certificate is a plain (non-subPath)
						// mount: cert-manager updates the Secret in
						// place, and the cert-reload sidecar reacts
						// to it (renewal => reload, not rollout).
						// The root FS is a documented exception to
						// read-only (the master maintains
						// postfix-files and the data spool; the
						// queue is the only volume with mail).
						ReadinessProbe: portProbe(render.PortPostfixInbound),
						LivenessProbe:  portProbe(render.PortPostfixInbound),
					}, {
						// Queue metrics sidecar (ADR 0005): samples
						// `postqueue -j` as the postfix user and
						// serves the Prometheus text file on 9150.
						Name:  "queue-metrics",
						Image: images.Postfix,
						Command: []string{
							"/usr/local/bin/cnms-postfix-queue-metrics.sh",
						},
						Env: []corev1.EnvVar{{
							Name:  "CNMS_QUEUE_METRICS_PORT",
							Value: fmt.Sprintf("%d", render.PortPostfixMetrics),
						}},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:    new(postfixUserUID),
							RunAsGroup:   new(postfixQueueGroup),
							RunAsNonRoot: new(true),
						},
						Ports: []corev1.ContainerPort{{
							Name:          "postfix-metrics",
							ContainerPort: render.PortPostfixMetrics,
						}},
						// postqueue needs the config (postconf) and
						// the queue; the certificate is not used.
						VolumeMounts: configSubPathMounts("main.cf", "master.cf"),
					}, {
						// Certificate reload sidecar: polls the
						// mounted certificate and runs `postfix
						// reload` when it changes (D5).
						Name:    "cert-reload",
						Image:   images.Postfix,
						Command: certReloadLoop(postfixTLSDir+"/"+TLSCertFile, r.ReloadCommand),
						VolumeMounts: append(configSubPathMounts("main.cf", "master.cf"),
							tlsMount(),
							// `postfix reload` talks to the master
							// via the queue-directory sockets.
							queueMount()),
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: postfixQueueVolume},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: queueStorage,
						},
					},
				},
			}},
		},
	}
}

// dovecotStatefulSet renders the Dovecot workload: single replica
// (active/standby via rescheduling, project plan HA section), RWO
// mail volume only when mailboxes are enabled, fsGroup vmail.
func dovecotStatefulSet(server *cnmsv1alpha1.MailServer, r *render.RenderResult) *appsv1.StatefulSet {
	name := render.ServiceName(server.Name, render.ComponentDovecot)
	replicas := int32(1)
	mailboxes := mailboxesEnabled(server)
	storage := resource.MustParse("10Gi")
	if server.Spec.Mailboxes != nil {
		storage = quantityValue(server.Spec.Mailboxes.Storage, storage)
	}

	var imapPorts []corev1.ContainerPort
	var volumeClaimTemplates []corev1.PersistentVolumeClaim
	if mailboxes {
		imapPorts = []corev1.ContainerPort{
			{Name: "imap", ContainerPort: render.PortDovecotIMAP},
			{Name: "imaps", ContainerPort: render.PortDovecotIMAPS},
		}
		volumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: dovecotMailVolume},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storage,
					},
				},
			},
		}}
	}

	return &appsv1.StatefulSet{
		ObjectMeta: objectMeta(server, name, render.ComponentDovecot),
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: name + "-headless",
			Selector:    selectorMeta(componentSelector(server, render.ComponentDovecot)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentDovecot),
					Annotations: podAnnotations(r),
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						// The image runs rootless as vmail; fsGroup
						// group-owns the emptyDir runtime and the
						// mail volume (dovecotVmailID).
						FSGroup: new(dovecotVmailID),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server),
					Volumes: []corev1.Volume{
						{
							Name:         configVolumeName,
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}},
						},
						tlsVolume(TLSSecretName(server)),
						{
							Name:         "run",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
					InitContainers: []corev1.Container{{
						// doveconf -n is strict: unknown settings
						// are fatal, so a bad render never starts.
						Name:         validateContainerName,
						Image:        images.Dovecot,
						Command:      r.ValidateCommand,
						VolumeMounts: dovecotMounts(false),
					}},
					Containers: []corev1.Container{{
						Name:         "dovecot",
						Image:        images.Dovecot, // default CMD: tini + dovecot -F
						Ports:        append([]corev1.ContainerPort{{Name: "lmtp", ContainerPort: render.PortDovecotLMTP}, {Name: "auth", ContainerPort: render.PortDovecotAuth}}, imapPorts...),
						VolumeMounts: dovecotMounts(mailboxes),
						SecurityContext: &corev1.SecurityContext{
							// vmail (non-root) binds the LMTP (24)
							// and IMAP (143) ports below 1024.
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"NET_BIND_SERVICE"},
							},
						},
						// The LMTP listener (24) is present in both
						// variants (mail storage and auth-only).
						ReadinessProbe: portProbe(render.PortDovecotLMTP),
						LivenessProbe:  portProbe(render.PortDovecotLMTP),
					}, {
						// The loop needs coreutils (sleep,
						// sha256sum), which the minimal Dovecot
						// image lacks, so it runs in the Postfix
						// image and reloads by signaling the
						// Dovecot master (doveadm reload is the
						// same SIGHUP) via the pidfile in the
						// shared runtime directory.
						Name:            "cert-reload",
						Image:           images.Postfix,
						SecurityContext: &corev1.SecurityContext{RunAsUser: new(dovecotVmailID)},
						Command: certReloadLoop(dovecotSSLDir()+"/"+TLSCertFile,
							[]string{"kill", "-HUP", "$(cat " + dovecotRunDir + "/master.pid)"}),
						VolumeMounts: dovecotMounts(false),
					}},
				},
			},
			VolumeClaimTemplates: volumeClaimTemplates,
		},
	}
}

// rspamdDeployment renders the Rspamd workload: Deployment with
// maxUnavailable 0 and surge (project plan HA section).
func rspamdDeployment(server *cnmsv1alpha1.MailServer, r *render.RenderResult) *appsv1.Deployment {
	name := render.ServiceName(server.Name, render.ComponentRspamd)
	replicas := int32(2)
	if server.Spec.Rspamd != nil {
		replicas = int32Value(server.Spec.Rspamd.Replicas, replicas)
	}
	return &appsv1.Deployment{
		ObjectMeta: objectMeta(server, name, render.ComponentRspamd),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: selectorMeta(componentSelector(server, render.ComponentRspamd)),
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: new(intstr.FromInt32(0)),
					MaxSurge:       new(intstr.FromInt32(1)),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentRspamd),
					Annotations: podAnnotations(r),
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: new(rspamdGroupID),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server),
					Volumes: []corev1.Volume{
						configVolume(name),
						{
							// On-disk module state only (ADR 0004);
							// shared state lives in the per-MailServer
							// Valkey. Restarts reset it deliberately.
							Name:         "state",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
					InitContainers: []corev1.Container{{
						Name:         validateContainerName,
						Image:        images.Rspamd,
						Command:      r.ValidateCommand,
						VolumeMounts: localDMount(),
					}},
					Containers: []corev1.Container{{
						Name:           "rspamd",
						Image:          images.Rspamd, // default CMD: rspamd -f
						Ports:          []corev1.ContainerPort{{Name: "milter", ContainerPort: render.PortRspamdMilter}, {Name: "rspamd-metrics", ContainerPort: render.PortRspamdController}},
						VolumeMounts:   append(localDMount(), corev1.VolumeMount{Name: stateVolumeName, MountPath: rspamdStateDir}),
						ReadinessProbe: portProbe(render.PortRspamdController),
						LivenessProbe:  portProbe(render.PortRspamdController),
					}},
				},
			},
		},
	}
}

// valkeyDeployment renders the Valkey workload: one instance per
// MailServer (principle 6), persistence per spec.
func valkeyDeployment(server *cnmsv1alpha1.MailServer, r *render.RenderResult) *appsv1.Deployment {
	name := render.ServiceName(server.Name, render.ComponentValkey)
	replicas := int32(1)
	var dataVolume corev1.Volume
	if valkeyPersistence(server) {
		dataVolume = corev1.Volume{
			Name: valkeyDataVolume,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: name + "-" + valkeyDataVolume,
				},
			},
		}
	} else {
		dataVolume = corev1.Volume{
			Name:         valkeyDataVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}
	}
	return &appsv1.Deployment{
		ObjectMeta: objectMeta(server, name, render.ComponentValkey),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: selectorMeta(componentSelector(server, render.ComponentValkey)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentValkey),
					Annotations: podAnnotations(r),
				},
				Spec: corev1.PodSpec{
					TopologySpreadConstraints: topologySpreadConstraints(server),
					Volumes: []corev1.Volume{
						configVolume(name),
						dataVolume,
					},
					InitContainers: []corev1.Container{{
						// 9.1.2 has no config-check flag, so the
						// gate briefly starts the server and pings
						// it (render.ValidateCommand).
						Name:         validateContainerName,
						Image:        images.Valkey,
						Command:      r.ValidateCommand,
						VolumeMounts: []corev1.VolumeMount{{Name: configVolumeName, MountPath: "/etc/valkey", ReadOnly: true}},
					}},
					Containers: []corev1.Container{{
						Name:           "valkey",
						Image:          images.Valkey, // default entrypoint; the conf path is the arg
						Args:           []string{"/etc/valkey/valkey.conf"},
						Ports:          []corev1.ContainerPort{{Name: valkeyPortName, ContainerPort: render.PortValkey}},
						VolumeMounts:   []corev1.VolumeMount{{Name: configVolumeName, MountPath: "/etc/valkey", ReadOnly: true}, {Name: valkeyDataVolume, MountPath: "/data"}},
						ReadinessProbe: portProbe(render.PortValkey),
						LivenessProbe:  portProbe(render.PortValkey),
					}, {
						// Metrics sidecar (ADR 0005).
						Name:  "redis-exporter",
						Image: images.RedisExporter,
						Args: []string{
							// The flag is redis.addr (not
							// redis.address) in the pinned exporter.
							fmt.Sprintf("--redis.addr=redis://127.0.0.1:%d", render.PortValkey),
						},
						Ports: []corev1.ContainerPort{{
							Name:          "valkey-metrics",
							ContainerPort: render.PortValkeyMetrics,
						}},
					}},
				},
			},
		},
	}
}

// valkeyPersistentVolumeClaim is the Valkey data PVC (only when
// spec.valkey.persistence is enabled).
func valkeyPersistentVolumeClaim(server *cnmsv1alpha1.MailServer) *corev1.PersistentVolumeClaim {
	name := render.ServiceName(server.Name, render.ComponentValkey)
	storage := quantityValue(server.Spec.Valkey.Storage, resource.MustParse("1Gi"))
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: objectMeta(server, name+"-"+valkeyDataVolume, render.ComponentValkey),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storage,
				},
			},
		},
	}
}

// shared volume and mount helpers.

func objectMeta(server *cnmsv1alpha1.MailServer, name string, c render.Component) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: server.Namespace,
		Labels:    objectLabels(server, c),
	}
}

func selectorMeta(sel *metav1.LabelSelector) *metav1.LabelSelector {
	return sel
}

// configVolume is the ConfigMap volume of a component (named
// "<server>-<component>").
func configVolume(name string) corev1.Volume {
	return corev1.Volume{
		Name: configVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			},
		},
	}
}

// tlsVolume is the (optional) certificate Secret volume. Optional so
// the pods can run before cert-manager has issued the certificate
// (degraded mode, documented).
func tlsVolume(secretName string) corev1.Volume {
	return corev1.Volume{
		Name: tlsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secretName,
				Optional:   new(true),
			},
		},
	}
}

// configSubPathMounts mounts the Postfix config files individually
// into the image's /etc/postfix, so the image's postfix-files
// (meta_directory) stays visible to `postfix check` and the master.
// subPath staleness is fine: config changes always change the
// pod-template hash and recreate the pods.
func configSubPathMounts(files ...string) []corev1.VolumeMount {
	mounts := make([]corev1.VolumeMount, 0, len(files))
	for _, f := range files {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      configVolumeName,
			MountPath: "/etc/postfix/" + f,
			SubPath:   f,
		})
	}
	return mounts
}

// localDMount mounts the Rspamd ConfigMap over the image's local.d
// (the rendered overrides replace the directory; the rest of the
// image's configuration tree is untouched, ADR 0004).
func localDMount() []corev1.VolumeMount {
	return []corev1.VolumeMount{{
		Name:      configVolumeName,
		MountPath: "/etc/rspamd/local.d",
		ReadOnly:  true,
	}}
}

// tlsMount mounts the certificate at the Postfix path.
func tlsMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: tlsVolumeName, MountPath: postfixTLSDir, ReadOnly: true}
}

// dovecotMounts are the standard Dovecot volume mounts: the rendered
// configuration (shadowing the image tree), the certificate, the
// shared runtime directory, and the mail volume when storage is
// enabled.
func dovecotMounts(withMailVolume bool) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: configVolumeName, MountPath: "/etc/dovecot", ReadOnly: true},
		{Name: tlsVolumeName, MountPath: dovecotSSLDir(), ReadOnly: true},
		{Name: runVolumeName, MountPath: dovecotRunDir},
	}
	if withMailVolume {
		mounts = append(mounts, corev1.VolumeMount{Name: dovecotMailVolume, MountPath: "/srv/mail"})
	}
	return mounts
}

// dovecotSSLDir is the Dovecot certificate directory (matches the
// rendered ssl cert_file/key_file, ADR 0004).
func dovecotSSLDir() string {
	return "/etc/dovecot/ssl"
}

// queueMount mounts the Postfix queue volume.
func queueMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: postfixQueueVolume, MountPath: "/var/spool/postfix"}
}
