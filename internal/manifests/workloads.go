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
	"maps"
	"slices"

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
	replicas := PostfixReplicas(server)
	queueStorage := resource.MustParse("1Gi")
	if server.Spec.Postfix != nil {
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
			Selector:    componentSelector(server, render.ComponentPostfix),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentPostfix),
					Annotations: podAnnotations(render.ComponentPostfix, r),
				},
				Spec: corev1.PodSpec{
					// The cert-reload sidecar signals the master
					// of the postfix container.
					ShareProcessNamespace:         new(true),
					TerminationGracePeriodSeconds: new(postfixTerminationGrace),
					SecurityContext: &corev1.PodSecurityContext{
						// The queue volume is group-owned by the
						// postfix group (postfixQueueGroup) so the
						// daemons can write it.
						FSGroup:        new(postfixQueueGroup),
						SeccompProfile: podSeccomp(),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server, render.ComponentPostfix),
					Volumes: append([]corev1.Volume{
						configVolume(configMapName(server, render.ComponentPostfix, r)),
						tlsVolume(TLSSecretName(server)),
						{
							Name:         postfixQueueVolume,
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: postfixQueueVolume}},
						},
						emptyDirVolume(postfixDataVolume),
						emptyDirVolume(tmpVolumeName),
					}, postfixSecretVolumes(server, r)...),
					Containers: []corev1.Container{{
						Name:  postfixContainerName,
						Image: images.Postfix, // default CMD: the entrypoint
						Ports: append([]corev1.ContainerPort{{Name: "smtp", ContainerPort: render.PortPostfixInbound}}, submissionPorts...),
						// The libpq settings of the pgsql tables.
						Env: envVars(r.Env),
						VolumeMounts: append(append(configSubPathMounts(),
							// smtpd loads the certificate from this
							// path (the sidecar's mount is a
							// different filesystem).
							tlsMount(),
							queueMount(),
							corev1.VolumeMount{Name: postfixDataVolume, MountPath: postfixDataDir}),
							postfixSecretMounts(r)...),
						// Root with the capabilities of postfixMainCaps
						// (security.go): the master binds, chowns,
						// drops privileges and signals its daemons. The
						// root filesystem is read-only; the data
						// directory is an emptyDir (checked with the
						// pinned image: start, mail, reload and stop).
						// Postfix warns at start that the emptyDir is
						// not owned by postfix and is world-writable;
						// the daemons can write it, so the warning is
						// harmless.
						SecurityContext: rootSecurity(postfixMainCaps...),
						Resources:       postfixMainResources,
						// The certificate is a plain (non-subPath)
						// mount: cert-manager updates the Secret in
						// place, and the cert-reload sidecar reacts
						// to it (renewal => reload, not rollout).
						// The startup probe gives a slow first start
						// (empty queue volume, map builds) up to 150s
						// before liveness can kill the container.
						StartupProbe:   startupPortProbe(render.PortPostfixInbound),
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
						SecurityContext: nonRootSecurity(postfixUserUID, postfixQueueGroup),
						Resources:       postfixMetricsResources,
						Ports: []corev1.ContainerPort{{
							Name:          "postfix-metrics",
							ContainerPort: render.PortPostfixMetrics,
						}},
						// postqueue needs the config (postconf) and
						// the queue (read-only: it only talks to the
						// showq socket); the certificate is not used.
						// The script writes its metrics file to /tmp.
						VolumeMounts: append(configSubPathMounts(),
							readOnlyMount(queueMount()), tmpMount()),
					}, {
						// Certificate reload sidecar: polls the
						// mounted certificate and runs `postfix
						// reload` when it changes (D5).
						Name:    "cert-reload",
						Image:   images.Postfix,
						Command: certReloadLoop(postfixTLSDir+"/"+TLSCertFile, r.ReloadCommand),
						// Root (`postfix reload` is superuser-only) with
						// the two capabilities it needs (security.go).
						SecurityContext: rootSecurity(postfixReloadCaps...),
						Resources:       postfixReloadResources,
						VolumeMounts: append(configSubPathMounts(),
							tlsMount(),
							// `postfix reload` talks to the master
							// via the queue-directory sockets.
							queueMount()),
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				// The queue is transient: a restored queue delivers stale
				// or duplicate mail (project plan, backup section), so
				// k8up must not back it up.
				ObjectMeta: volumeClaimMeta(server, render.ComponentPostfix, postfixQueueVolume, false),
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

	// Auth-only mode listens on the auth port only; the probes use it.
	ports := []corev1.ContainerPort{{Name: "auth", ContainerPort: render.PortDovecotAuth}}
	probePort := render.PortDovecotAuth
	var volumeClaimTemplates []corev1.PersistentVolumeClaim
	if mailboxes {
		probePort = render.PortDovecotLMTPListen
		ports = append(ports,
			corev1.ContainerPort{Name: "lmtp", ContainerPort: render.PortDovecotLMTPListen},
			corev1.ContainerPort{Name: "imap", ContainerPort: render.PortDovecotIMAPListen},
			corev1.ContainerPort{Name: "imaps", ContainerPort: render.PortDovecotIMAPSListen})
		volumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			// The mail volume is the backup target (k8up backs up
			// PVCs unless excluded; the explicit "true" also holds
			// when k8up only backs up annotated volumes).
			ObjectMeta: volumeClaimMeta(server, render.ComponentDovecot, dovecotMailVolume, true),
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
			Selector:    componentSelector(server, render.ComponentDovecot),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentDovecot),
					Annotations: podAnnotations(render.ComponentDovecot, r),
				},
				Spec: corev1.PodSpec{
					// The cert-reload sidecar signals the Dovecot
					// master (kill -HUP) in the dovecot container;
					// both run as UID dovecotVmailID (the image
					// USER is vmail), so the signal is permitted.
					ShareProcessNamespace: new(true),
					SecurityContext: &corev1.PodSecurityContext{
						// The image runs rootless as vmail; fsGroup
						// group-owns the emptyDir runtime and the
						// mail volume (dovecotVmailID).
						FSGroup:        new(dovecotVmailID),
						SeccompProfile: podSeccomp(),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server, render.ComponentDovecot),
					Volumes: append([]corev1.Volume{
						configVolume(configMapName(server, render.ComponentDovecot, r)),
						tlsVolume(TLSSecretName(server)),
						emptyDirVolume(runVolumeName),
						// doveconf writes its binary configuration here.
						emptyDirVolume(tmpVolumeName),
					}, dovecotSecretVolumes(server, r)...),
					// The pinned Dovecot 2.4 image cannot start without the
					// certificate files (fatal at startup, whatever ssl says), and
					// it cannot serve without TLS anyway (ssl =
					// required). Wait for them in an init container:
					// the pod stays in Init until cert-manager has
					// issued the Secret instead of crash-looping.
					InitContainers: []corev1.Container{{
						Name:            waitTLSContainerName,
						Image:           images.Postfix, // busybox sh and sleep
						Command:         waitForTLSFiles(dovecotSSLDir()),
						VolumeMounts:    []corev1.VolumeMount{{Name: tlsVolumeName, MountPath: dovecotSSLDir(), ReadOnly: true}},
						SecurityContext: nonRootSecurity(nobodyID, nobodyID),
						Resources:       dovecotWaitTLSResources,
					}},
					Containers: []corev1.Container{{
						Name:            dovecotContainerName,
						Image:           images.Dovecot, // default CMD: tini + dovecot -F
						Ports:           ports,
						VolumeMounts:    dovecotMounts(mailboxes, len(r.SecretData) > 0),
						SecurityContext: dovecotSecurity(mailboxes),
						Resources:       dovecotMainResources,
						ReadinessProbe:  portProbe(probePort),
						LivenessProbe:   portProbe(probePort),
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
						SecurityContext: nonRootSecurity(dovecotVmailID, dovecotVmailID),
						Resources:       dovecotReloadResources,
						Command: certReloadLoop(dovecotSSLDir()+"/"+TLSCertFile,
							[]string{"kill", "-HUP", "$(cat " + dovecotRunDir + "/master.pid)"}),
						// Only the certificate and the pidfile; the
						// sidecar needs no users file or config.
						VolumeMounts: []corev1.VolumeMount{
							{Name: tlsVolumeName, MountPath: dovecotSSLDir(), ReadOnly: true},
							{Name: runVolumeName, MountPath: dovecotRunDir},
						},
					}},
				},
			},
			VolumeClaimTemplates: volumeClaimTemplates,
		},
	}
}

// rspamdDeployment renders the Rspamd workload: Deployment with
// maxUnavailable 0 and surge (project plan HA section).
func rspamdDeployment(server *cnmsv1alpha1.MailServer, r *render.RenderResult, dkim *corev1.Volume) *appsv1.Deployment {
	name := render.ServiceName(server.Name, render.ComponentRspamd)
	replicas := int32(2)
	if server.Spec.Rspamd != nil {
		replicas = int32Value(server.Spec.Rspamd.Replicas, replicas)
	}
	volumes := []corev1.Volume{
		configVolume(configMapName(server, render.ComponentRspamd, r)),
		{
			// On-disk module state only (ADR 0004);
			// shared state lives in the per-MailServer
			// Valkey. Restarts reset it deliberately.
			Name:         "state",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			// Scratch space of the read-only metrics sidecar.
			Name:         tmpVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
	mounts := append(rspamdConfigMounts(), corev1.VolumeMount{Name: stateVolumeName, MountPath: rspamdStateDir})
	if len(r.DataFiles) > 0 {
		volumes = append(volumes, dataVolume(dataConfigMapName(server, render.ComponentRspamd, r)))
		mounts = append(mounts, dataMount(render.RspamdDataDir))
	}
	if dkim != nil {
		volumes = append(volumes, *dkim)
		mounts = append(mounts, corev1.VolumeMount{Name: dkimVolumeName, MountPath: render.DKIMDir, ReadOnly: true})
	}
	return &appsv1.Deployment{
		ObjectMeta: objectMeta(server, name, render.ComponentRspamd),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: componentSelector(server, render.ComponentRspamd),
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
					Annotations: podAnnotations(render.ComponentRspamd, r),
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup:        new(rspamdGroupID),
						SeccompProfile: podSeccomp(),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server, render.ComponentRspamd),
					Volumes:                   volumes,
					Containers: []corev1.Container{{
						Name:            "rspamd",
						SecurityContext: nonRootSecurity(rspamdUserID, rspamdGroupID),
						Resources:       rspamdMainResources,
						Image:           images.Rspamd, // default CMD: rspamd -f
						// The controller port is for the probes and
						// loopback clients only; no Service exposes it.
						Ports:          []corev1.ContainerPort{{Name: "milter", ContainerPort: render.PortRspamdMilter}, {Name: "controller", ContainerPort: render.PortRspamdController}},
						VolumeMounts:   mounts,
						ReadinessProbe: portProbe(render.PortRspamdController),
						LivenessProbe:  portProbe(render.PortRspamdController),
					}, {
						// Metrics sidecar (ADR 0005): copies the
						// loopback-only controller /metrics and serves
						// it on 9151. It runs in the Postfix image
						// for busybox (wget, httpd), which the Rspamd
						// image lacks.
						Name:            rspamdMetricsContainer,
						Image:           images.Postfix,
						Command:         rspamdMetricsLoop(render.PortRspamdMetrics, render.PortRspamdController),
						SecurityContext: nonRootSecurity(nobodyID, nobodyID),
						Resources:       rspamdMetricsResources,
						Ports:           []corev1.ContainerPort{{Name: rspamdMetricsContainer, ContainerPort: render.PortRspamdMetrics}},
						VolumeMounts:    []corev1.VolumeMount{tmpMount()},
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
	// A rolling update starts the new pod before it stops the old one,
	// and the new pod cannot attach the ReadWriteOnce data volume while
	// the old one holds it (on another node), so the rollout would hang.
	// With persistence the old pod is stopped first; without it the
	// default rolling update is fine (emptyDir, no volume to hand over).
	var strategy appsv1.DeploymentStrategy
	if valkeyPersistence(server) {
		strategy.Type = appsv1.RecreateDeploymentStrategyType
	}
	return &appsv1.Deployment{
		ObjectMeta: objectMeta(server, name, render.ComponentValkey),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: strategy,
			Selector: componentSelector(server, render.ComponentValkey),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      objectLabels(server, render.ComponentValkey),
					Annotations: podAnnotations(render.ComponentValkey, r),
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						// The container runs as the valkey user, not as
						// root dropping to it, so the volume has to be
						// group-writable for it.
						FSGroup:        new(valkeyID),
						SeccompProfile: podSeccomp(),
					},
					TopologySpreadConstraints: topologySpreadConstraints(server, render.ComponentValkey),
					Volumes: []corev1.Volume{
						configVolume(configMapName(server, render.ComponentValkey, r)),
						dataVolume,
					},
					Containers: []corev1.Container{{
						Name:            "valkey",
						Image:           images.Valkey, // default entrypoint; the conf path is the arg
						SecurityContext: nonRootSecurity(valkeyID, valkeyID),
						Resources:       valkeyMainResources,
						Args:            []string{"/etc/valkey/valkey.conf"},
						Ports:           []corev1.ContainerPort{{Name: valkeyPortName, ContainerPort: render.PortValkey}},
						VolumeMounts:    []corev1.VolumeMount{{Name: configVolumeName, MountPath: "/etc/valkey", ReadOnly: true}, {Name: valkeyDataVolume, MountPath: "/data"}},
						ReadinessProbe:  portProbe(render.PortValkey),
						LivenessProbe:   portProbe(render.PortValkey),
					}, {
						// Metrics sidecar (ADR 0005).
						Name:            "redis-exporter",
						Image:           images.RedisExporter,
						SecurityContext: nonRootSecurity(nobodyID, nobodyID),
						Resources:       valkeyExporterResources,
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
	meta := objectMeta(server, name+"-"+valkeyDataVolume, render.ComponentValkey)
	// Excluded from k8up: the state is rebuildable (except trained
	// Bayes data; an opt-in with a consistent dump is the backlog).
	meta.Annotations = backupAnnotations(server, false)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: meta,
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

// volumeClaimMeta is the metadata of a StatefulSet volumeClaimTemplate:
// the name, the managed labels and the k8up annotation. The
// StatefulSet controller copies both onto the PVCs it creates; the
// templates of an existing StatefulSet cannot change, so the labels
// and annotations reach only PVCs created after the StatefulSet.
func volumeClaimMeta(server *cnmsv1alpha1.MailServer, c render.Component, name string, backup bool) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        name,
		Labels:      objectLabels(server, c),
		Annotations: backupAnnotations(server, backup),
	}
}

func objectMeta(server *cnmsv1alpha1.MailServer, name string, c render.Component) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: server.Namespace,
		Labels:    objectLabels(server, c),
	}
}

// secretFilesVolume is the volume of a rendered Secret of a component
// (password hashes, directory passwords). Mode 0440: the owner is root,
// and the pod fsGroup (the group of the engine user) grants the read.
func secretFilesVolume(name, secretName string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: new(int32(0o440))},
		},
	}
}

// dovecotSecretVolumes are the Secret volumes of Dovecot: the live users
// Secret, which always exists and is mounted whole (a subPath mount is never
// updated by the kubelet, ADR 0015, section 4.7), and, for a directory with
// a bind password or CA, the hash-named Secret of the revision. A directory
// without either renders no Secret, so there is nothing to mount.
func dovecotSecretVolumes(server *cnmsv1alpha1.MailServer, r *render.RenderResult) []corev1.Volume {
	volumes := []corev1.Volume{secretFilesVolume(usersVolumeName, LiveUsersSecretName(server))}
	if len(r.SecretData) > 0 {
		volumes = append(volumes, secretFilesVolume(directoryVolumeName, secretName(server, render.ComponentDovecot, r)))
	}
	return volumes
}

// postfixSecretVolumes is the volume of the directory tables (they carry
// the bind password), present when the render has a directory.
func postfixSecretVolumes(server *cnmsv1alpha1.MailServer, r *render.RenderResult) []corev1.Volume {
	if len(r.SecretData) == 0 {
		return nil
	}
	return []corev1.Volume{secretFilesVolume(directoryVolumeName, secretName(server, render.ComponentPostfix, r))}
}

// postfixSecretMounts mounts the directory tables, read-only, where the
// rendered main.cf looks for them.
func postfixSecretMounts(r *render.RenderResult) []corev1.VolumeMount {
	if len(r.SecretData) == 0 {
		return nil
	}
	return []corev1.VolumeMount{{Name: directoryVolumeName, MountPath: render.PostfixSecretDir, ReadOnly: true}}
}

// envVars turns the environment of a render into container variables in
// a stable order.
func envVars(env map[string]string) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}
	out := make([]corev1.EnvVar, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, corev1.EnvVar{Name: k, Value: env[k]})
	}
	return out
}

// configVolume is the volume of the immutable, hash-named ConfigMap
// of a component.
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
// the pods can start before cert-manager has issued the certificate:
// Postfix then starts without TLS (smtpd logs "disabling TLS
// support", submission refuses AUTH until the cert-reload sidecar
// reloads the new certificate), and the Dovecot pod waits in its
// init container (waitForTLSFiles).
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
func configSubPathMounts() []corev1.VolumeMount {
	// The files of the Postfix render (the table files are always
	// rendered, possibly without a line).
	files := append([]string{"main.cf", "master.cf"}, render.PostfixTableFiles...)
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

// dataVolume is the volume of the immutable, hash-named data ConfigMap of a
// component (dataConfigMap), mounted as a whole directory.
func dataVolume(name string) corev1.Volume {
	return corev1.Volume{
		Name: dataVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}},
		},
	}
}

func dataMount(path string) corev1.VolumeMount {
	return corev1.VolumeMount{Name: dataVolumeName, MountPath: path, ReadOnly: true}
}

// rspamdConfigMounts mounts the Rspamd ConfigMap over the image's
// local.d (the rendered overrides replace the directory; the rest of
// the image's configuration tree is untouched, ADR 0004), and the guard
// script at the path Rspamd loads it from (ADR 0013). The script is
// also visible in local.d, where nothing reads it. Both the workload
// and the validation Job use it, because configtest rejects a broken
// script. subPath staleness is fine: the ConfigMap is immutable and
// named by its content.
func rspamdConfigMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{{
		Name:      configVolumeName,
		MountPath: "/etc/rspamd/local.d",
		ReadOnly:  true,
	}, {
		Name:      configVolumeName,
		MountPath: "/etc/rspamd/" + render.RspamdLocalLua,
		SubPath:   render.RspamdLocalLua,
		ReadOnly:  true,
	}}
}

// dkimVolume is the projected volume that puts the signing keys of the
// authorized domains at render.DKIMDir (ADR 0011, section 5): one item
// per available active key, at <domain>/<selector>.key, so domains that
// share a selector name do not collide. Keys that could not be read
// are left out. nil when no key is available.
//
// Every Secret source is optional, so a Secret deleted after the pod
// started does not wedge the pod: the files vanish, the guard defers
// the mail of the domain, and the pod stays up. The mode 0440 together
// with the pod fsGroup (the Rspamd group) lets the Rspamd user read
// the files and nobody else.
func dkimVolume(req *render.RenderRequest) *corev1.Volume {
	items := map[string][]corev1.KeyToPath{}
	for _, d := range slices.Compact(slices.Sorted(slices.Values(req.AuthorizedDomains))) {
		for _, k := range req.ActiveKeys(d) {
			if k.Available {
				items[k.SecretName] = append(items[k.SecretName], corev1.KeyToPath{
					Key: k.SecretKey, Path: render.DKIMKeyPath(d, k.Selector),
				})
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	var sources []corev1.VolumeProjection
	for _, name := range slices.Sorted(maps.Keys(items)) {
		sources = append(sources, corev1.VolumeProjection{Secret: &corev1.SecretProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
			Items:                items[name],
			Optional:             new(true),
		}})
	}
	return &corev1.Volume{
		Name: dkimVolumeName,
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources:     sources,
			DefaultMode: new(int32(0o440)),
		}},
	}
}

// tlsMount mounts the certificate at the Postfix path.
func tlsMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: tlsVolumeName, MountPath: postfixTLSDir, ReadOnly: true}
}

// dovecotMounts are the standard Dovecot volume mounts: the rendered
// configuration (shadowing the image tree), the live users files, the
// directory secrets when there are any, the certificate, the
// shared runtime directory, and the mail volume when storage is
// enabled.
func dovecotMounts(withMailVolume, withDirectorySecret bool) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: configVolumeName, MountPath: "/etc/dovecot", ReadOnly: true},
		{Name: usersVolumeName, MountPath: render.DovecotUsersDir, ReadOnly: true},
	}
	if withDirectorySecret {
		mounts = append(mounts, corev1.VolumeMount{Name: directoryVolumeName, MountPath: render.DovecotSecretDir, ReadOnly: true})
	}
	mounts = append(mounts,
		corev1.VolumeMount{Name: tlsVolumeName, MountPath: dovecotSSLDir(), ReadOnly: true},
		corev1.VolumeMount{Name: runVolumeName, MountPath: dovecotRunDir},
		tmpMount())
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

// readOnlyMount returns m with ReadOnly set.
func readOnlyMount(m corev1.VolumeMount) corev1.VolumeMount {
	m.ReadOnly = true
	return m
}

// queueMount mounts the Postfix queue volume.
func queueMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: postfixQueueVolume, MountPath: "/var/spool/postfix"}
}
