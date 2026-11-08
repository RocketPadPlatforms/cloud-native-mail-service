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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// The status is written as a patch, so a change of the object since
// it was read (a spec edit, a label) does not make the write fail.
var _ = Describe("MailServer status write", func() {
	It("persists the status although the object changed since it was read", func() {
		ns, _ := grantFixture()
		createServerG(ns, testServerName, nil)
		key := types.NamespacedName{Name: testServerName, Namespace: ns}
		stale := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, key, stale)).To(Succeed())
		orig := stale.DeepCopy()

		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Labels = map[string]string{touchedKey: "yes"}
		})
		setCondition(&stale.Status.Conditions, stale.Generation, conditionReady,
			metav1.ConditionFalse, "Test", "patched")

		// An update from the stale copy conflicts ...
		err := k8sClient.Status().Update(ctx, stale.DeepCopy())
		Expect(errors.IsConflict(err)).To(BeTrue(), "update error: %v", err)

		// ... the patch does not.
		_, err = newReconcilerG(AuthorizationAllowlist, false).finish(ctx, stale, orig, nil)
		Expect(err).ToNot(HaveOccurred())
		status, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal("Test"))
	})
})
