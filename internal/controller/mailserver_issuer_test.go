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

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// A shared ClusterIssuer lets every tenant obtain certificates from
// it, so it needs the operator's opt-in (ADR 0003).
var _ = Describe("MailServer ClusterIssuer gate", func() {
	var ns, domain string
	getCertificate := func() error {
		cert := &unstructured.Unstructured{}
		cert.SetGroupVersionKind(certificateGVK)
		return k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, cert)
	}

	BeforeEach(func() {
		installCertificateCRD()
		ns, domain = grantFixture()
		createServerG(ns, testServerName, domain, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.TLS.IssuerRef = cnmsv1alpha1.IssuerReference{Kind: clusterIssuerKind, Name: "shared"}
		})
	})

	It("blocks a ClusterIssuer without --allow-cluster-issuers", func() {
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, testServerName)

		status, reason, msg := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonClusterIssuerNotAllowed))
		Expect(msg).To(ContainSubstring("--allow-cluster-issuers"))
		Expect(apierrors.IsNotFound(getCertificate())).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{}))).To(BeTrue())
	})

	It("creates the Certificate with the flag", func() {
		r := newReconcilerG(AuthorizationAllowlist, true)
		r.AllowClusterIssuers = true
		deployG(r, ns, testServerName)

		Expect(getCertificate()).To(Succeed())
		cert := &unstructured.Unstructured{}
		cert.SetGroupVersionKind(certificateGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, cert)).To(Succeed())
		kind, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "kind")
		Expect(kind).To(Equal(clusterIssuerKind))
	})

	It("always allows a namespaced Issuer", func() {
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.TLS.IssuerRef = cnmsv1alpha1.IssuerReference{Kind: issuerKind, Name: "own"}
		})
		deployG(newReconcilerG(AuthorizationAllowlist, true), ns, testServerName)
		Expect(getCertificate()).To(Succeed())
	})
})
