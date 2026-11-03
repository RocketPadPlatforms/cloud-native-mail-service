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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// installCertificateCRD installs a minimal cert-manager Certificate
// CRD for one spec and removes it afterwards, so the other specs keep
// running without the integration.
func installCertificateCRD() {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: crdCertificate},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cert-manager.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind: "Certificate", ListKind: "CertificateList",
				Plural: "certificates", Singular: "certificate",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: new(true),
					},
				},
			}},
		},
	}
	Expect(k8sClient.Create(ctx, crd)).To(Succeed())
	DeferCleanup(func() {
		_ = k8sClient.Delete(ctx, crd)
		// A terminating CRD still counts as installed for the next spec.
		Eventually(func() bool {
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(crd), &apiextensionsv1.CustomResourceDefinition{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})
	Eventually(func() error {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(certificateGVK)
		return k8sClient.List(ctx, list)
	}).Should(Succeed())
}

func certificateDNSNamesG(ns, name string) []string {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, cert)).To(Succeed())
	names, _, err := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	Expect(err).ToNot(HaveOccurred())
	return names
}

// postfixMainCFG returns the main.cf the Postfix StatefulSet of the
// test server currently references.
func postfixMainCFG(ns string) string {
	sts := getStatefulSetG(ns, postfixObjectName)
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && strings.HasPrefix(v.ConfigMap.Name, postfixObjectName+"-") {
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: v.ConfigMap.Name, Namespace: ns}, cm)).To(Succeed())
			return cm.Data["main.cf"]
		}
	}
	Fail("the Postfix StatefulSet references no ConfigMap")
	return ""
}

// A revoked grant must reach the data plane (ADR 0003): the server is
// re-rendered with the authorized subset, and the Certificate loses
// the revoked name.
var _ = Describe("MailServer domain revocation", func() {
	var ns, domainA, domainB string
	var r *MailServerReconciler

	// settle reconciles until the validation Jobs of the new
	// configuration passed and the workloads were applied.
	settle := func() { deployG(r, ns, testServerName) }
	revoke := func(domain string) {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{
			ObjectMeta: metav1.ObjectMeta{Name: domain},
		})).To(Succeed())
	}

	BeforeEach(func() {
		installCertificateCRD()
		ns, domainA = grantFixture()
		domainB = uniqueDomain()
		createDomainG(ns, domainB)
		createGrantG(domainB, ns, false)
		createServerG(ns, testServerName, domainA, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Domains = append(s.Spec.Domains, cnmsv1alpha1.NameReference{Name: domainLabel(domainB)})
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		r = newReconcilerG(AuthorizationAllowlist, true)
		settle()
	})

	It("serves both domains while both are granted", func() {
		status, reason, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAuthorized))
		Expect(certificateDNSNamesG(ns, testServerName)).To(ConsistOf(domainA, domainB))
		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + minDomain(domainA, domainB) + ", "))
	})

	It("re-renders without a revoked domain and keeps the other", func() {
		before := podTemplateHash(getStatefulSetG(ns, postfixObjectName))
		revoke(domainB)
		settle()

		status, reason, msg := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonPartiallyAuthorized))
		Expect(msg).To(ContainSubstring(domainB))
		// Ready reports the unmet part of the spec.
		readyStatus, readyReason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(readyStatus).To(Equal(metav1.ConditionFalse))
		Expect(readyReason).To(Equal(ReasonPartiallyAuthorized))

		Expect(podTemplateHash(getStatefulSetG(ns, postfixObjectName))).ToNot(Equal(before))
		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domainA + "\n"))
		Expect(cf).ToNot(ContainSubstring(domainB))
		Expect(certificateDNSNamesG(ns, testServerName)).To(ConsistOf(domainA))
	})

	It("renders fail closed when every grant is revoked", func() {
		before := podTemplateHash(getStatefulSetG(ns, postfixObjectName))
		revoke(domainA)
		revoke(domainB)
		settle()

		status, reason, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNotGranted))
		Expect(podTemplateHash(getStatefulSetG(ns, postfixObjectName))).ToNot(Equal(before))
		cf := postfixMainCFG(ns)
		Expect(cf).ToNot(ContainSubstring("virtual_mailbox_domains"))
		Expect(cf).ToNot(ContainSubstring("smtpd_sasl_type"))
		Expect(cf).ToNot(ContainSubstring(domainA))
		Expect(cf).ToNot(ContainSubstring(domainB))
		Expect(certificateDNSNamesG(ns, testServerName)).To(ConsistOf(
			"mail-postfix." + ns + ".svc.cluster.local"))
	})

	It("serves the domain again when the grant is restored", func() {
		revoke(domainB)
		settle()
		createGrantG(domainB, ns, false)
		settle()

		status, _, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(certificateDNSNamesG(ns, testServerName)).To(ConsistOf(domainA, domainB))
	})
})

// minDomain returns the lexically smaller of two domains.
func minDomain(a, b string) string {
	if a < b {
		return a
	}
	return b
}
