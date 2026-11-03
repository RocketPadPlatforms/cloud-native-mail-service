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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// foreignSelector is the selector of a tenant's own Service.
var foreignSelector = map[string]string{"app": "mine"}

// The operator only touches objects it controls: it never overwrites
// an object of someone else that happens to carry a generated name,
// and it only deletes ServiceMonitors it created itself.
var _ = Describe("MailServer object ownership", func() {
	var ns, domain string
	BeforeEach(func() {
		ns, domain = grantFixture()
	})

	It("does not overwrite a Service it does not control", func() {
		foreign := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: postfixObjectName, Namespace: ns},
			Spec: corev1.ServiceSpec{
				Selector: foreignSelector,
				Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}},
			},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		createServerG(ns, testServerName, domain, nil)

		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, testServerName)

		status, reason, msg := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonOwnershipConflict))
		Expect(msg).To(ContainSubstring(`Service "` + postfixObjectName + `"`))

		got := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(BeEmpty())
		Expect(got.Spec.Selector).To(Equal(foreignSelector))
		Expect(got.Spec.Ports).To(HaveLen(1))
		Expect(got.Spec.Ports[0].Port).To(Equal(int32(8080)))
		// No workload was applied on top of the conflict.
		err := k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	// The immutable configuration and the validation Jobs are named by
	// their content, so a tenant could pre-create them: a Job that
	// "passed" or a ConfigMap with other content.
	Describe("pre-created content-named objects", func() {
		var plane *manifests.DataPlane
		BeforeEach(func() {
			createServerG(ns, testServerName, domain, nil)
			server := &cnmsv1alpha1.MailServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
			var err error
			plane, err = renderDataPlane(server, []string{domain}, nil, nil)
			Expect(err).ToNot(HaveOccurred())
		})

		expectConflictWith := func(r *MailServerReconciler, kind, name string) {
			reconcileServerG(r, ns, testServerName)
			status, reason, msg := serverConditionG(ns, testServerName, conditionReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonOwnershipConflict))
			Expect(msg).To(ContainSubstring(kind + ` "` + name + `"`))
			err := k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		expectConflict := func(kind, name string) {
			expectConflictWith(newReconcilerG(AuthorizationAllowlist, true), kind, name)
		}

		It("does not accept a foreign validation Job as the result", func() {
			want := plane.Validations[0].Job
			foreign := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: want.Name, Namespace: ns},
				Spec:       *want.Spec.DeepCopy(),
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			finishJobG(foreign, true)

			expectConflict("Job", want.Name)
		})

		It("does not accept a foreign ConfigMap in place of the configuration", func() {
			name := plane.Configs[0].GetName()
			Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Data:       map[string]string{"main.cf": "# not the rendered configuration"},
			})).To(Succeed())

			expectConflict("ConfigMap", name)
		})

		// The manager cache only holds the objects labelled as managed
		// by the operator, so a foreign object of the same name is
		// invisible to a cached read.
		It("sees foreign objects although the manager cache does not hold them", func() {
			cm := plane.Configs[0].GetName()
			Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cm, Namespace: ns},
				Data:       map[string]string{"main.cf": "# not the rendered configuration"},
			})).To(Succeed())
			mgr := startManagerG()
			Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())
			cached := &corev1.ConfigMap{}
			err := mgr.GetClient().Get(ctx, types.NamespacedName{Name: cm, Namespace: ns}, cached)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the cache must not hold unlabelled ConfigMaps")

			expectConflictWith(&MailServerReconciler{
				Client: mgr.GetClient(), Scheme: mgr.GetScheme(), APIReader: mgr.GetAPIReader(),
				DomainAuthorization: AuthorizationAllowlist, EnableInsecureTestFeatures: true,
			}, "ConfigMap", cm)
		})
	})

	// Same for the applied (updatable) kinds: the manager cache does
	// not hold an unlabelled Service, and the apply must still refuse it.
	It("does not overwrite a foreign Service the manager cache does not hold", func() {
		Expect(k8sClient.Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: postfixObjectName, Namespace: ns},
			Spec: corev1.ServiceSpec{
				Selector: foreignSelector,
				Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}},
			},
		})).To(Succeed())
		createServerG(ns, testServerName, domain, nil)
		mgr := startManagerG()
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())
		cached := &corev1.Service{}
		err := mgr.GetClient().Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, cached)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the cache must not hold unlabelled Services")

		reconcileServerG(&MailServerReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), APIReader: mgr.GetAPIReader(),
			DomainAuthorization: AuthorizationAllowlist, EnableInsecureTestFeatures: true,
		}, ns, testServerName)

		status, reason, msg := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonOwnershipConflict))
		Expect(msg).To(ContainSubstring(`Service "` + postfixObjectName + `"`))
		got := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(BeEmpty())
		Expect(got.Spec.Selector).To(Equal(foreignSelector))
	})

	It("keeps updating the objects it controls", func() {
		createServerG(ns, testServerName, domain, nil)
		r := newReconcilerG(AuthorizationAllowlist, true)
		deployG(r, ns, testServerName)

		svc := &corev1.Service{}
		key := types.NamespacedName{Name: postfixObjectName, Namespace: ns}
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Spec.Ports = svc.Spec.Ports[:1]
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		reconcileServerG(r, ns, testServerName)
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(len(svc.Spec.Ports)).To(BeNumerically(">", 1))
		status, reason, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAuthorized))
	})

	Describe("ServiceMonitor cleanup", func() {
		monitor := func(name string, labels map[string]string) *unstructured.Unstructured {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(serviceMonitorGVK)
			u.SetName(name)
			u.SetNamespace(ns)
			u.SetLabels(labels)
			u.Object["spec"] = map[string]any{
				"selector":  map[string]any{"matchLabels": map[string]any{"a": "b"}},
				"endpoints": []any{map[string]any{"port": "metrics"}},
			}
			return u
		}
		exists := func(name string) bool {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(serviceMonitorGVK)
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, u)
			return err == nil
		}

		It("deletes only ServiceMonitors the server controls", func() {
			if os.Getenv("PROMETHEUS_CRDS") != "1" {
				Skip("needs the Prometheus Operator CRDs")
			}
			instance := map[string]string{manifests.InstanceLabelKey: testServerName}
			cnms := map[string]string{
				manifests.InstanceLabelKey: testServerName,
				manifests.ManagedByLabel:   manifests.ManagedByValue,
				manifests.ServerLabel:      testServerName,
			}
			// A Helm release with the same instance label.
			Expect(k8sClient.Create(ctx, monitor("helm-release", instance))).To(Succeed())
			// Carries every CNMS label but has no controller reference.
			Expect(k8sClient.Create(ctx, monitor("lookalike", cnms))).To(Succeed())

			enabled, disabled := prometheusEnabled, "disabled"
			createServerG(ns, testServerName, domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Observability = &cnmsv1alpha1.Observability{PrometheusOperator: &enabled}
			})
			r := newReconcilerG(AuthorizationAllowlist, true)
			reconcileServerG(r, ns, testServerName)
			Expect(listServiceMonitorsG(ns)).To(HaveLen(5)) // 3 of the server, 2 foreign

			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Observability = &cnmsv1alpha1.Observability{PrometheusOperator: &disabled}
			})
			reconcileServerG(r, ns, testServerName)

			Expect(exists("helm-release")).To(BeTrue())
			Expect(exists("lookalike")).To(BeTrue())
			Expect(listServiceMonitorsG(ns)).To(HaveLen(2))
		})
	})
})
