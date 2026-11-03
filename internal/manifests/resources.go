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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Default resources of the data plane. The MailServer API has no
// resources field yet (docs/backlog.md), so these constants are the
// only knob; the values suit a small server and are starting points,
// not measurements.
//
// QoS: the Postfix and Dovecot pods are Guaranteed (requests equal
// limits for cpu and memory in every container, init containers
// included). Postfix holds the live mail queue and Dovecot is a single
// replica whose RWO volume takes minutes to fail over, so neither
// should be among the first evicted under node pressure. Rspamd (two
// stateless replicas) and Valkey (rebuildable state) are Burstable:
// the memory request equals the limit (memory cannot be reclaimed, so
// a request below the limit only invites an OOM kill), the cpu limit
// is above the request so they can burst. The cpu limit of a
// Guaranteed pod throttles it; the values leave headroom for that.
var (
	postfixMainResources     = guaranteed("500m", "256Mi")
	postfixMetricsResources  = guaranteed("50m", "128Mi") // jq slurps the queue listing
	postfixReloadResources   = guaranteed("10m", "32Mi")
	dovecotMainResources     = guaranteed("500m", "512Mi")
	dovecotReloadResources   = guaranteed("10m", "32Mi")
	dovecotWaitTLSResources  = guaranteed("10m", "16Mi")
	rspamdMainResources      = burstable("250m", "1", "768Mi")
	rspamdMetricsResources   = burstable("10m", "100m", "32Mi")
	valkeyMainResources      = burstable("50m", "500m", "512Mi")
	valkeyExporterResources  = burstable("10m", "100m", "32Mi")
	validatePostfixResources = burstable("50m", "500m", "128Mi")
	validateRspamdResources  = burstable("100m", "1", "512Mi") // configtest loads the whole rule set
	validateDovecotResources = burstable("50m", "500m", "128Mi")
	validateValkeyResources  = burstable("50m", "500m", "128Mi")
)

// guaranteed returns equal requests and limits.
func guaranteed(cpu, memory string) corev1.ResourceRequirements {
	return burstable(cpu, cpu, memory)
}

// burstable returns a cpu request below the cpu limit; the memory
// request equals the limit.
func burstable(cpuRequest, cpuLimit, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpuRequest),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpuLimit),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	}
}

// validationResources are the resources of the validation container of
// a component.
func validationResources(c render.Component) corev1.ResourceRequirements {
	switch c {
	case render.ComponentPostfix:
		return validatePostfixResources
	case render.ComponentRspamd:
		return validateRspamdResources
	case render.ComponentDovecot:
		return validateDovecotResources
	default:
		return validateValkeyResources
	}
}
