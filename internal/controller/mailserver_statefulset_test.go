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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// applyObject must not delete a StatefulSet because of a storage size
// and must recreate it (background propagation, PVCs untouched) when
// the claim template names change (ADR 0010). envtest runs no garbage
// collector, so the delete options are asserted through a client
// interceptor.
var _ = Describe("StatefulSet volume claim template changes", func() {
	var (
		ns      string
		server  *cnmsv1alpha1.MailServer
		deleted []client.DeleteOptions
		wrapped client.Client
		failNew bool
	)
	BeforeEach(func() {
		ns, _ = grantFixture()
		createServerG(ns, testServerName, nil)
		deployG(newReconcilerG(AuthorizationAllowlist, true), ns, testServerName)
		server = &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())

		deleted, failNew = nil, false
		watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).ToNot(HaveOccurred())
		wrapped = interceptor.NewClient(watchClient, interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				o := client.DeleteOptions{}
				o.ApplyOptions(opts)
				deleted = append(deleted, o)
				return c.Delete(ctx, obj, opts...)
			},
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if failNew {
					return errors.NewAlreadyExists(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, obj.GetName())
				}
				return c.Create(ctx, obj, opts...)
			},
		})
	})

	// desired is the live StatefulSet with a mutation applied, the way
	// a new render would present it to applyObject.
	desired := func(name string, mutate func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
		d := getStatefulSetG(ns, name).DeepCopy()
		d.ResourceVersion, d.UID, d.OwnerReferences = "", "", nil
		mutate(d)
		return d
	}
	mailTemplate := func() []corev1.PersistentVolumeClaim {
		return []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "mail"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				}},
			},
		}}
	}

	It("keeps the StatefulSet and its templates when only a size differs", func() {
		d := desired(postfixObjectName, func(d *appsv1.StatefulSet) {
			d.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("5Gi")
			d.Spec.Replicas = new(int32(3))
		})
		Expect(applyObject(ctx, wrapped, wrapped, scheme.Scheme, server, d)).To(Succeed())

		Expect(deleted).To(BeEmpty())
		sts := getStatefulSetG(ns, postfixObjectName)
		Expect(*sts.Spec.Replicas).To(Equal(int32(3)))
		Expect(sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]).
			To(Equal(resource.MustParse("1Gi")))
	})

	It("recreates the StatefulSet with background propagation when the template names change", func() {
		old := getStatefulSetG(ns, "mail-dovecot")
		d := desired("mail-dovecot", func(d *appsv1.StatefulSet) { d.Spec.VolumeClaimTemplates = mailTemplate() })
		Expect(applyObject(ctx, wrapped, wrapped, scheme.Scheme, server, d)).To(Succeed())

		Expect(deleted).To(HaveLen(1))
		Expect(*deleted[0].PropagationPolicy).To(Equal(metav1.DeletePropagationBackground))
		sts := getStatefulSetG(ns, "mail-dovecot")
		Expect(sts.UID).ToNot(Equal(old.UID))
		Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
	})

	It("does not fail when the recreated StatefulSet still exists", func() {
		failNew = true
		d := desired("mail-dovecot", func(d *appsv1.StatefulSet) { d.Spec.VolumeClaimTemplates = mailTemplate() })
		Expect(applyObject(ctx, wrapped, wrapped, scheme.Scheme, server, d)).To(Succeed())
	})
})
