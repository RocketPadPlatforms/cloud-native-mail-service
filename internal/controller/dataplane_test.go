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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// workloadConfigMapG returns the name of the ConfigMap a workload pod
// template mounts.
func workloadConfigMapG(ns, name string) string {
	sts := getStatefulSetG(ns, name)
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil {
			return v.ConfigMap.Name
		}
	}
	Fail("no ConfigMap volume in " + name)
	return ""
}

// staleWorkloadsClient serves frozen copies of the workloads, like a
// cache that lags behind the API server.
type staleWorkloadsClient struct {
	client.Client
	sts map[string]*appsv1.StatefulSet
	dep map[string]*appsv1.Deployment
}

// freezeWorkloadsG snapshots the four workloads of the test server.
func freezeWorkloadsG(c client.Client, ns string) *staleWorkloadsClient {
	stale := &staleWorkloadsClient{
		Client: c,
		sts:    map[string]*appsv1.StatefulSet{},
		dep:    map[string]*appsv1.Deployment{},
	}
	for _, n := range []string{"mail-postfix", "mail-dovecot"} {
		stale.sts[n] = getStatefulSetG(ns, n)
	}
	for _, n := range []string{testServerName + "-rspamd", testServerName + "-valkey"} {
		d := &appsv1.Deployment{}
		Expect(c.Get(ctx, types.NamespacedName{Name: n, Namespace: ns}, d)).To(Succeed())
		stale.dep[n] = d
	}
	return stale
}

func (c *staleWorkloadsClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch o := obj.(type) {
	case *appsv1.StatefulSet:
		if s, ok := c.sts[key.Name]; ok {
			s.DeepCopyInto(o)
			return nil
		}
	case *appsv1.Deployment:
		if d, ok := c.dep[key.Name]; ok {
			d.DeepCopyInto(o)
			return nil
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

var _ = Describe("MailServer validation gate", func() {
	var ns string
	var r *MailServerReconciler
	enableMailboxes := func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
	}
	BeforeEach(func() {
		ns, _ = grantFixture()
		r = newReconcilerG(AuthorizationAllowlist, true)
		createTLSSecretG(ns, "mail-tls")
		createServerG(ns, "mail", nil)
	})

	It("creates the Jobs and holds back the workloads until they succeed", func() {
		reconcileServerG(r, ns, "mail")

		jobs := jobsG(ns, "mail", "")
		Expect(jobs).To(HaveLen(4))
		for _, j := range jobs {
			Expect(j.OwnerReferences).To(HaveLen(1))
			Expect(j.OwnerReferences[0].Kind).To(Equal("MailServer"))
			Expect(*j.Spec.BackoffLimit).To(BeZero())
			Expect(j.Spec.TTLSecondsAfterFinished).ToNot(BeNil())
		}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &corev1.Service{})).To(Succeed())
		status, reason, _ := serverConditionG(ns, "mail", ConditionConfigValidated)
		Expect(status).To(Equal(metav1.ConditionUnknown))
		Expect(reason).To(Equal(ReasonValidatingConfig))
		readyStatus, readyReason, _ := serverConditionG(ns, "mail", conditionReady)
		Expect(readyStatus).To(Equal(metav1.ConditionFalse))
		Expect(readyReason).To(Equal(ReasonValidatingConfig))

		// One succeeded Job is not enough: all or nothing.
		first := jobsG(ns, "mail", "postfix")
		finishJobG(&first[0], true)
		reconcileServerG(r, ns, "mail")
		err = k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{})
		Expect(errors.IsNotFound(err)).To(BeTrue())

		finishJobsG(ns, "mail", true)
		reconcileServerG(r, ns, "mail")
		sts := getStatefulSetG(ns, postfixObjectName)
		Expect(sts.Spec.Template.Spec.Volumes[0].ConfigMap.Name).To(Equal(configMapsG(ns, "postfix")[0].Name))
		status, reason, _ = serverConditionG(ns, "mail", ConditionConfigValidated)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonConfigValidated))
	})

	It("never creates the workloads of a rejected first configuration", func() {
		reconcileServerG(r, ns, "mail")
		finishJobsG(ns, "mail", true)
		// Replace the result of one Job by a failure.
		dovecotJob := jobsG(ns, "mail", "dovecot")[0]
		Expect(k8sClient.Delete(ctx, &dovecotJob, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		reconcileServerG(r, ns, "mail")
		finishJobG(&jobsG(ns, "mail", "dovecot")[0], false)
		reconcileServerG(r, ns, "mail")

		err := k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
		_, reason, _ := serverConditionG(ns, "mail", conditionReady)
		Expect(reason).To(Equal(ReasonConfigRejected))
	})

	Describe("a configuration change", func() {
		var oldPostfixCM, oldDovecotCM string
		BeforeEach(func() {
			deployG(r, ns, "mail")
			convergeWorkloadsG(ns)
			reconcileServerG(r, ns, "mail")
			oldPostfixCM = workloadConfigMapG(ns, postfixObjectName)
			oldDovecotCM = workloadConfigMapG(ns, "mail-dovecot")
			status, _, _ := serverConditionG(ns, "mail", conditionReady)
			Expect(status).To(Equal(metav1.ConditionTrue))
			mutateServerG(ns, enableMailboxes)
		})

		It("keeps the old templates until the new Jobs succeed", func() {
			reconcileServerG(r, ns, "mail")

			Expect(workloadConfigMapG(ns, "mail-dovecot")).To(Equal(oldDovecotCM))
			Expect(configMapsG(ns, "dovecot")).To(HaveLen(2))
			status, reason, _ := serverConditionG(ns, "mail", ConditionConfigValidated)
			Expect(status).To(Equal(metav1.ConditionUnknown))
			Expect(reason).To(Equal(ReasonValidatingConfig))

			finishJobsG(ns, "mail", true)
			reconcileServerG(r, ns, "mail")
			Expect(workloadConfigMapG(ns, "mail-dovecot")).ToNot(Equal(oldDovecotCM))
			Expect(workloadConfigMapG(ns, postfixObjectName)).ToNot(Equal(oldPostfixCM))
		})

		It("rejects a failed validation and keeps the working data plane", func() {
			reconcileServerG(r, ns, "mail")
			finishJobsG(ns, "mail", false)
			reconcileServerG(r, ns, "mail")

			Expect(workloadConfigMapG(ns, "mail-dovecot")).To(Equal(oldDovecotCM))
			Expect(workloadConfigMapG(ns, postfixObjectName)).To(Equal(oldPostfixCM))
			status, reason, msg := serverConditionG(ns, "mail", ConditionConfigValidated)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonConfigRejected))
			Expect(msg).To(ContainSubstring("BackoffLimitExceeded"))
			Expect(msg).To(ContainSubstring("mail-dovecot-v-"))
			readyStatus, readyReason, readyMsg := serverConditionG(ns, "mail", conditionReady)
			Expect(readyStatus).To(Equal(metav1.ConditionFalse))
			Expect(readyReason).To(Equal(ReasonConfigRejected))
			Expect(readyMsg).To(Equal(msg))

			// The rejected objects stay (no flapping re-validation).
			Expect(configMapsG(ns, "dovecot")).To(HaveLen(2))

			// Reverting the spec to the running configuration needs
			// no validation: the old template was validated.
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) { s.Spec.Mailboxes = nil })
			reconcileServerG(r, ns, "mail")
			status, reason, _ = serverConditionG(ns, "mail", ConditionConfigValidated)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonConfigValidated))
		})

		It("deletes nothing while a newer change validates, even from a lagging cache", func() {
			// The cache still shows the converged first revision.
			stale := freezeWorkloadsG(k8sClient, ns)

			// The second revision is applied; its pods are not updated.
			reconcileServerG(r, ns, "mail")
			finishJobsG(ns, "mail", true)
			reconcileServerG(r, ns, "mail")
			rollingPostfixCM := workloadConfigMapG(ns, postfixObjectName)
			Expect(rollingPostfixCM).ToNot(Equal(oldPostfixCM))

			// A third change starts validating.
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{Enabled: new(false)}
			})
			lagging := newReconcilerG(AuthorizationAllowlist, true)
			lagging.Client = stale
			reconcileServerG(lagging, ns, "mail")
			status, _, _ := serverConditionG(ns, "mail", ConditionConfigValidated)
			Expect(status).To(Equal(metav1.ConditionUnknown))

			// Everything the running and the previous template mount stays.
			cms := configMapsG(ns, "postfix")
			postfixCMs := make([]string, 0, len(cms))
			for _, cm := range cms {
				postfixCMs = append(postfixCMs, cm.Name)
			}
			Expect(postfixCMs).To(ContainElements(oldPostfixCM, rollingPostfixCM))
		})

		It("removes superseded objects only after the rollout finished", func() {
			reconcileServerG(r, ns, "mail")
			finishJobsG(ns, "mail", true)
			reconcileServerG(r, ns, "mail")

			// New templates are applied but the pods are not updated:
			// the old objects must stay.
			names := func() []string {
				cms := configMapsG(ns, "dovecot")
				out := make([]string, 0, len(cms))
				for _, cm := range cms {
					out = append(out, cm.Name)
				}
				return out
			}
			Expect(names()).To(ContainElement(oldDovecotCM))
			Expect(names()).To(HaveLen(2))

			convergeWorkloadsG(ns)
			reconcileServerG(r, ns, "mail")
			Expect(names()).To(ConsistOf(workloadConfigMapG(ns, "mail-dovecot")))
			Expect(configMapsG(ns, "postfix")).To(HaveLen(1))
			// Only the Jobs of the current configuration remain.
			Expect(jobsG(ns, "mail", "")).To(HaveLen(4))
			for _, j := range jobsG(ns, "mail", "dovecot") {
				Expect(j.Annotations[manifests.ConfigHashAnnotation]).
					To(Equal(podTemplateHash(getStatefulSetG(ns, "mail-dovecot"))))
			}
		})
	})

	It("tolerates objects that already exist", func() {
		reconcileServerG(r, ns, "mail")
		before := configMapsG(ns, "postfix")
		// A second reconcile creates the same immutable objects again.
		reconcileServerG(r, ns, "mail")
		Expect(configMapsG(ns, "postfix")).To(HaveLen(1))
		Expect(jobsG(ns, "mail", "postfix")).To(HaveLen(1))

		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
		dup := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: before[0].Name, Namespace: ns}}
		Expect(createOwned(ctx, k8sClient, k8sClient, scheme.Scheme, server, dup)).To(Succeed())
	})
})
