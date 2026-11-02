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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// convergeStatefulSetG marks a StatefulSet as fully rolled out.
func convergeStatefulSetG(ns, name string) {
	sts := getStatefulSetG(ns, name)
	n := *sts.Spec.Replicas
	sts.Status = appsv1.StatefulSetStatus{
		ObservedGeneration: sts.Generation,
		Replicas:           n, UpdatedReplicas: n, ReadyReplicas: n,
		CurrentRevision: "rev", UpdateRevision: "rev",
	}
	Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
}

// convergeDeploymentG marks a Deployment as fully rolled out.
func convergeDeploymentG(ns, name string) {
	dep := &appsv1.Deployment{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, dep)).To(Succeed())
	n := *dep.Spec.Replicas
	dep.Status = appsv1.DeploymentStatus{
		ObservedGeneration: dep.Generation,
		Replicas:           n, UpdatedReplicas: n, ReadyReplicas: n, AvailableReplicas: n,
	}
	Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
}

// convergeWorkloadsG marks all four workloads of the server as rolled
// out (envtest runs no StatefulSet or Deployment controller).
func convergeWorkloadsG(ns string) {
	convergeStatefulSetG(ns, testServerName+"-postfix")
	convergeStatefulSetG(ns, testServerName+"-dovecot")
	convergeDeploymentG(ns, testServerName+"-rspamd")
	convergeDeploymentG(ns, testServerName+"-valkey")
}

func createTLSSecretG(ns, name string) {
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       map[string][]byte{tlsCertKey: []byte("crt"), "tls.key": []byte("key")},
	})).To(Succeed())
}

var _ = Describe("MailServer rollout readiness", func() {
	var ns, domain string
	BeforeEach(func() {
		ns, domain = grantFixture()
		createServerG(ns, "mail", domain, nil)
		createTLSSecretG(ns, "mail-tls")
		deployG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
	})

	It("is Ready only when every workload finished its rollout", func() {
		status, reason, _ := serverConditionG(ns, "mail", ConditionWorkloadsReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonProgressing))

		convergeWorkloadsG(ns)
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
		status, reason, _ = serverConditionG(ns, "mail", conditionReady)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal("Ready"))
	})

	It("is not Ready while a StatefulSet revision rollout is pending", func() {
		convergeWorkloadsG(ns)
		sts := getStatefulSetG(ns, "mail-postfix")
		sts.Status.UpdateRevision = "rev2"
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

		status, reason, msg := serverConditionG(ns, "mail", ConditionWorkloadsReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonProgressing))
		Expect(msg).To(ContainSubstring("mail-postfix"))
	})

	It("is not Ready while the controller has not observed the spec", func() {
		convergeWorkloadsG(ns)
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "mail-rspamd", Namespace: ns}, dep)).To(Succeed())
		dep.Status.ObservedGeneration = dep.Generation - 1
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

		status, _, msg := serverConditionG(ns, "mail", ConditionWorkloadsReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(msg).To(ContainSubstring("mail-rspamd"))
	})

	It("reports RolloutStalled when the workloads stay not ready past the deadline", func() {
		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
		cond := meta.FindStatusCondition(server.Status.Conditions, ConditionWorkloadsReady)
		cond.LastTransitionTime = metav1.NewTime(time.Now().Add(-2 * rolloutDeadline))
		Expect(k8sClient.Status().Update(ctx, server)).To(Succeed())
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

		_, reason, _ := serverConditionG(ns, "mail", ConditionWorkloadsReady)
		Expect(reason).To(Equal(ReasonRolloutStalled))
	})

	It("reports RolloutStalled when a Deployment exceeds its progress deadline", func() {
		convergeWorkloadsG(ns)
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "mail-rspamd", Namespace: ns}, dep)).To(Succeed())
		dep.Status.UpdatedReplicas = 1
		dep.Status.Conditions = []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
			Reason: "ProgressDeadlineExceeded",
		}}
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

		status, reason, _ := serverConditionG(ns, "mail", conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonRolloutStalled))
	})
})
