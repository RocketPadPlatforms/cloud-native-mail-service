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
	"fmt"
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

func certificateDNSNamesG(ns string) []string {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, cert)).To(Succeed())
	names, _, err := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	Expect(err).ToNot(HaveOccurred())
	return names
}

// postfixMainCFG returns the main.cf the Postfix StatefulSet of the
// test server currently references.
func postfixMainCFG(ns string) string { return postfixMainCFGOf(ns, testServerName) }

// postfixMainCFGOf is postfixMainCFG for the server of the given name.
func postfixMainCFGOf(ns, server string) string {
	name := server + "-postfix"
	sts := getStatefulSetG(ns, name)
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && strings.HasPrefix(v.ConfigMap.Name, name+"-") {
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
		domainB = addGrantedDomainG(ns, testServerName)
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		createTLSSecretG(ns)
		r = newReconcilerG(AuthorizationAllowlist, true)
		settle()
	})

	It("serves both domains while both are granted", func() {
		status, reason, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAuthorized))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA, domainB))
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
		Expect(msg).To(ContainSubstring("once the new configuration is rolled out"))
		// While the new revision rolls out the revoked domain is
		// still served, so Ready does not claim otherwise.
		readyStatus, readyReason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(readyStatus).To(Equal(metav1.ConditionFalse))
		Expect(readyReason).To(Equal(ReasonProgressing))
		// Once rolled out, Ready reports the unmet part of the spec.
		convergeWorkloadsG(ns)
		reconcileServerG(r, ns, testServerName)
		readyStatus, readyReason, _ = serverConditionG(ns, testServerName, conditionReady)
		Expect(readyStatus).To(Equal(metav1.ConditionFalse))
		Expect(readyReason).To(Equal(ReasonPartiallyAuthorized))

		Expect(podTemplateHash(getStatefulSetG(ns, postfixObjectName))).ToNot(Equal(before))
		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domainA + "\n"))
		Expect(cf).ToNot(ContainSubstring(domainB))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA))
	})

	It("reports the validation, not the revocation, until the new configuration is ready", func() {
		revoke(domainB)
		reconcileServerG(r, ns, testServerName)

		_, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(ReasonValidatingConfig))
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
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(
			"mail-postfix." + ns + ".svc.cluster.local"))
	})

	It("serves the domain again when the grant is restored", func() {
		revoke(domainB)
		settle()
		createGrantG(domainB, ns, false)
		settle()

		status, _, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA, domainB))
	})

	// The tenant controls the inputs below; none of them may keep a
	// revocation from reaching the data plane.
	expectFailClosed := func() {
		cf := postfixMainCFG(ns)
		Expect(cf).ToNot(ContainSubstring("virtual_mailbox_domains"))
		Expect(cf).ToNot(ContainSubstring(domainA))
		Expect(cf).ToNot(ContainSubstring(domainB))
	}

	// detach points the MailDomain of a domain at another server, which
	// is how a domain leaves this one (ADR 0014).
	detach := func(domain, server string) {
		md := getMailDomainG(ns, domainLabel(domain))
		md.Spec.ServerRef.Name = server
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
	}

	It("revokes a domain that moves to another server and serves it there", func() {
		// A server without a domain deploys nothing, so it starts with one.
		domainC := addGrantedDomainG(ns, otherServer)
		createServerG(ns, otherServer, nil)
		deployG(r, ns, otherServer)
		Expect(postfixMainCFGOf(ns, otherServer)).To(ContainSubstring(domainC))
		Expect(postfixMainCFGOf(ns, otherServer)).ToNot(ContainSubstring(domainB))

		detach(domainB, otherServer)
		settle()
		deployG(r, ns, otherServer)

		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domainA + "\n"))
		Expect(cf).ToNot(ContainSubstring(domainB))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA))
		Expect(postfixMainCFGOf(ns, otherServer)).To(ContainSubstring(domainB))
		Expect(postfixMainCFGOf(ns, otherServer)).To(ContainSubstring(domainC))
		// Nothing is unauthorized: the domain left, it was not revoked.
		status, reason, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAuthorized))
	})

	It("revokes a domain when its MailDomain is deleted", func() {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomain{
			ObjectMeta: metav1.ObjectMeta{Name: domainLabel(domainB), Namespace: ns},
		})).To(Succeed())
		settle()

		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domainA + "\n"))
		Expect(cf).ToNot(ContainSubstring(domainB))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA))
	})

	It("renders fail closed when the last domain leaves", func() {
		detach(domainA, otherServer)
		detach(domainB, otherServer)
		settle()

		expectFailClosed()
		Expect(certificateDNSNamesG(ns)).To(ConsistOf("mail-postfix." + ns + ".svc.cluster.local"))
		status, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoDomains))
	})

	It("keeps serving while the MailDirectory is missing and nothing is revoked", func() {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDirectory{
			ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: ns},
		})).To(Succeed())
		settle()

		_, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(ReasonDirectoryNotFound))
		Expect(postfixMainCFG(ns)).To(ContainSubstring(domainB))
	})

	It("revokes a domain although the MailDirectory is missing", func() {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDirectory{
			ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: ns},
		})).To(Succeed())
		revoke(domainB)
		settle()

		expectFailClosed()
		_, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(ReasonDirectoryNotFound))
	})

	It("revokes a domain and drops the Certificate although the ClusterIssuer is refused", func() {
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) { s.Spec.TLS.IssuerRef.Kind = clusterIssuerKind })
		settle()
		// Refused, but nothing is revoked: the running data plane stays.
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA, domainB))

		revoke(domainB)
		settle()

		expectFailClosed()
		_, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(ReasonClusterIssuerNotAllowed))
		cert := &unstructured.Unstructured{}
		cert.SetGroupVersionKind(certificateGVK)
		err := k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, cert)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("revokes a domain although the users Secret is broken", func() {
		createUsersSecretG(ns,
			"users:\n  - username: testuser\n    passwordHash: '$6$salt$hash'\n    mailbox: testuser@example.org\n")
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Submission = &cnmsv1alpha1.Submission{
				InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName},
			}
		})
		settle()
		Expect(k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: usersSecretName, Namespace: ns},
		})).To(Succeed())
		revoke(domainB)
		settle()

		expectFailClosed()
		_, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(ReasonStaticUsersInvalid))
	})

	// failClosedJobsG names the validation Jobs of the fail-closed
	// render, so a spec can finish those and leave the others.
	failClosedJobsG := func() map[string]bool {
		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
		plane, err := renderDataPlane(server, nil, nil, nil)
		Expect(err).ToNot(HaveOccurred())
		names := map[string]bool{}
		for _, v := range plane.Validations {
			names[v.Job.Name] = true
		}
		return names
	}

	for _, rejected := range []bool{false, true} {
		It(fmt.Sprintf("revokes a domain while another change is validating (rejected=%t)", rejected), func() {
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{Enabled: new(false)}
			})
			reconcileServerG(r, ns, testServerName)
			revoke(domainB)
			reconcileServerG(r, ns, testServerName)
			// The new configuration stays unfinished (or fails); only
			// the fail-closed one passes.
			closed := failClosedJobsG()
			jobs := jobsG(ns, testServerName, "")
			for i := range jobs {
				switch {
				case len(jobs[i].Status.Conditions) > 0:
				case closed[jobs[i].Name]:
					finishJobG(&jobs[i], true)
				case rejected:
					finishJobG(&jobs[i], false)
				}
			}
			reconcileServerG(r, ns, testServerName)

			expectFailClosed()
			validated, reason, _ := serverConditionG(ns, testServerName, ConditionConfigValidated)
			if rejected {
				Expect(validated).To(Equal(metav1.ConditionFalse))
				Expect(reason).To(Equal(ReasonConfigRejected))
			} else {
				Expect(validated).To(Equal(metav1.ConditionUnknown))
				Expect(reason).To(Equal(ReasonValidatingConfig))
			}
		})
	}
})

// Before the first rollout there is no workload yet, but the
// Certificate already lists the domains and cert-manager keeps
// renewing it.
var _ = Describe("MailServer revocation before the first rollout", func() {
	var ns, domainA, domainB string
	var r *MailServerReconciler

	BeforeEach(func() {
		installCertificateCRD()
		ns, domainA = grantFixture()
		domainB = addGrantedDomainG(ns, testServerName)
		createServerG(ns, testServerName, nil)
		r = newReconcilerG(AuthorizationAllowlist, true)
		// The validation Jobs never finish: no workload is created.
		reconcileServerG(r, ns, testServerName)
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(domainA, domainB))
	})

	own := func() string { return "mail-postfix." + ns + ".svc.cluster.local" }

	It("stops renewing the Certificate when every grant is revoked", func() {
		for _, d := range []string{domainA, domainB} {
			Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: d}})).To(Succeed())
		}
		reconcileServerG(r, ns, testServerName)
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(own()))
	})

	It("drops a revoked domain from the Certificate while the directory is missing", func() {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDirectory{
			ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: ns},
		})).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domainB}})).To(Succeed())
		reconcileServerG(r, ns, testServerName)
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(own()))
	})
})

// minDomain returns the lexically smaller of two domains.
func minDomain(a, b string) string {
	if a < b {
		return a
	}
	return b
}
