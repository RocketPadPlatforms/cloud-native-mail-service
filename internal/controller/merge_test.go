/*
Copyright 2026 Rocket Pad Platforms GmbH.

Licensed under the Apache License, Version 2.0 (the "License");
you may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// TestMergeOwnedFieldsDeployment covers the Deployment churn guard:
// annotations maintained by other actors (the kube deployment
// controller writes deployment.kubernetes.io/revision onto the
// object) must survive a reconcile, and a reconcile whose owned
// fields already match must report no change so it does not write.
func TestMergeOwnedFieldsDeployment(t *testing.T) {
	replicas := int32(1)
	deploy := func(annotations map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mail-test", Annotations: annotations,
			},
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		}
	}

	existing := deploy(map[string]string{
		manifests.ConfigHashAnnotation:      "abc",
		"deployment.kubernetes.io/revision": "1",
	})
	desired := deploy(map[string]string{manifests.ConfigHashAnnotation: "abc"})

	if mergeOwnedFields(existing, desired) {
		t.Fatal("no change expected when only a foreign annotation differs")
	}
	if existing.Annotations["deployment.kubernetes.io/revision"] != "1" {
		t.Fatal("foreign annotation must be preserved")
	}

	// A changed owned annotation is reported and applied.
	desired = deploy(map[string]string{manifests.ConfigHashAnnotation: "def"})
	if !mergeOwnedFields(existing, desired) {
		t.Fatal("owned annotation change expected")
	}
	if got := existing.Annotations[manifests.ConfigHashAnnotation]; got != "def" {
		t.Fatalf("owned annotation = %q, want def", got)
	}
	if existing.Annotations["deployment.kubernetes.io/revision"] != "1" {
		t.Fatal("foreign annotation must survive an owned change")
	}
}
