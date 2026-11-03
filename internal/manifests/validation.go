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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

const (
	// validationLabel marks the pods of a validation Job. They do not
	// carry the component label, so no Service or PodDisruptionBudget
	// selects them.
	validationLabel = "cnms.rpp.gmbh/validation"

	// validationNameInfix sits between the workload name and the hash
	// in a validation Job name. Keep it short: the MailServer name
	// limit (api/v1alpha1) is derived from the longest generated name.
	validationNameInfix = "-v-"

	// validationTTLSeconds keeps a finished Job (and its pod logs)
	// around for an hour.
	validationTTLSeconds int32 = 3600
	// validationDeadlineSeconds bounds a hung validation; it is long
	// enough for the first image pull of a new node.
	validationDeadlineSeconds int64 = 900
)

// validation builds the Job that runs the validate command of a
// component against its new configuration, with the component image
// (ADR 0009). The Job name carries the config hash, so every rendered
// configuration is validated exactly once.
func validation(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) Validation {
	hash := configHash(c, r)
	name := render.ServiceName(server.Name, c) + validationNameInfix + hashSuffix(hash)

	podLabels := objectLabels(server, "")
	podLabels[validationLabel] = string(c)
	jobLabels := objectLabels(server, c)

	volumes, mounts := validationVolumes(server, c, r)
	podSecurity := &corev1.PodSecurityContext{SeccompProfile: podSeccomp()}
	var backoffLimit int32
	ttl := validationTTLSeconds
	deadline := validationDeadlineSeconds
	return Validation{
		Component: c,
		Workload:  render.ServiceName(server.Name, c),
		Hash:      hash,
		Job: &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   server.Namespace,
				Labels:      jobLabels,
				Annotations: map[string]string{ConfigHashAnnotation: hash},
			},
			Spec: batchv1.JobSpec{
				BackoffLimit:            &backoffLimit,
				TTLSecondsAfterFinished: &ttl,
				ActiveDeadlineSeconds:   &deadline,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
					Spec: corev1.PodSpec{
						RestartPolicy:                corev1.RestartPolicyNever,
						AutomountServiceAccountToken: new(false),
						SecurityContext:              podSecurity,
						Volumes:                      volumes,
						Containers: []corev1.Container{{
							Name:            validateContainerName,
							Image:           componentImage(c),
							Command:         r.ValidateCommand,
							VolumeMounts:    mounts,
							SecurityContext: validationSecurity(c),
							Resources:       validationResources(c),
						}},
					},
				},
			},
		},
	}
}

// validationSecurity is the security context of the validation
// container of a component: the user and capabilities of the workload
// it checks, except that nothing binds a port or signals a process.
func validationSecurity(c render.Component) *corev1.SecurityContext {
	switch c {
	case render.ComponentPostfix:
		return rootSecurity(postfixValidateCaps...)
	case render.ComponentRspamd:
		return nonRootSecurity(rspamdUserID, rspamdGroupID)
	case render.ComponentDovecot:
		return nonRootSecurity(dovecotVmailID, dovecotVmailID)
	default:
		return nonRootSecurity(valkeyID, valkeyID)
	}
}

// componentImage is the data-plane image of a component.
func componentImage(c render.Component) string {
	switch c {
	case render.ComponentPostfix:
		return images.Postfix
	case render.ComponentRspamd:
		return images.Rspamd
	case render.ComponentDovecot:
		return images.Dovecot
	default:
		return images.Valkey
	}
}

// validationVolumes are the volumes and mounts of a validation Job:
// the new configuration mounted exactly like the workload does, and
// scratch space where the check writes (no queue volume or
// certificate is needed).
func validationVolumes(server *cnmsv1alpha1.MailServer, c render.Component, r *render.RenderResult) ([]corev1.Volume, []corev1.VolumeMount) {
	config := configVolume(configMapName(server, c, r))
	switch c {
	case render.ComponentPostfix:
		// `postfix check` creates the queue directories.
		return []corev1.Volume{config, {
				Name:         postfixQueueVolume,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
			append(configSubPathMounts(), queueMount())
	case render.ComponentRspamd:
		// The keys are not mounted: configtest does not read key files,
		// and a missing key must not hold back the rollout (ADR 0011);
		// the controller checks them when it renders.
		return []corev1.Volume{config}, rspamdConfigMounts()
	case render.ComponentDovecot:
		// `doveconf -n` reads neither the users file nor the
		// certificate, so only the configuration is mounted; it
		// writes its binary configuration to /tmp.
		return []corev1.Volume{config, emptyDirVolume(tmpVolumeName)},
			[]corev1.VolumeMount{{Name: configVolumeName, MountPath: "/etc/dovecot", ReadOnly: true}, tmpMount()}
	default:
		// The validation starts the server, which writes its pidfile
		// to /tmp.
		return []corev1.Volume{config, emptyDirVolume(tmpVolumeName)},
			[]corev1.VolumeMount{{Name: configVolumeName, MountPath: "/etc/valkey", ReadOnly: true}, tmpMount()}
	}
}
