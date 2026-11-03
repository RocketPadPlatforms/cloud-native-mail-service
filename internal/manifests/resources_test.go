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
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Every container of every pod (init containers and validation Jobs
// included) has cpu and memory requests and limits, and the memory
// request equals the limit.
func TestEveryContainerHasResources(t *testing.T) {
	for _, pc := range allPods(t, true) {
		for _, c := range slices.Concat(pc.pod.InitContainers, pc.pod.Containers) {
			r := c.Resources
			for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				req, lim := r.Requests[name], r.Limits[name]
				if req.IsZero() || lim.IsZero() {
					t.Errorf("%s/%s: %s needs a request and a limit: %+v", pc.name, c.Name, name, r)
				}
			}
			if r.Requests.Memory().Cmp(*r.Limits.Memory()) != 0 {
				t.Errorf("%s/%s: memory request %s != limit %s", pc.name, c.Name, r.Requests.Memory(), r.Limits.Memory())
			}
		}
	}
}

// guaranteedPod reports whether the pod has the Guaranteed QoS class:
// requests equal limits for cpu and memory in every container.
func guaranteedPod(pod corev1.PodSpec) bool {
	for _, c := range slices.Concat(pod.InitContainers, pod.Containers) {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			req, lim := c.Resources.Requests[name], c.Resources.Limits[name]
			if req.Cmp(lim) != 0 {
				return false
			}
		}
	}
	return true
}

// The Postfix pod holds the live mail queue and the Dovecot pod is a
// single replica with a slow-to-fail-over RWO volume: both are
// Guaranteed, the stateless or rebuildable Rspamd and Valkey pods are
// not.
func TestGuaranteedQoSOfStatefulPods(t *testing.T) {
	want := map[string]bool{
		testName + "-postfix": true,
		testName + "-dovecot": true,
		testName + "-rspamd":  false,
		testName + "-valkey":  false,
	}
	for _, pc := range allPods(t, true) {
		if pc.validation {
			continue
		}
		if got := guaranteedPod(pc.pod); got != want[pc.name] {
			t.Errorf("%s: Guaranteed = %v, want %v", pc.name, got, want[pc.name])
		}
	}
}
