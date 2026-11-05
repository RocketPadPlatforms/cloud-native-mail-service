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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// withHostname sets the hostname of a test server.
func withHostname(host string) func(*cnmsv1alpha1.MailServer) {
	return func(s *cnmsv1alpha1.MailServer) { s.Spec.Hostname = host }
}

// hostnameStateG returns "<status>/<reason>" of HostnameAuthorized.
func hostnameStateG(ns, name string) string {
	status, reason, _ := serverConditionG(ns, name, ConditionHostnameAuthorized)
	return fmt.Sprintf("%s/%s", status, reason)
}

var _ = Describe("MailServer hostname authorization", func() {
	var r *MailServerReconciler
	BeforeEach(func() { r = newReconcilerG(AuthorizationAllowlist, true) })

	It("authorizes a hostname granted to the namespace", func() {
		ns, _ := grantFixture()
		createServerG(ns, testServerName, nil)
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("True/" + ReasonAuthorized))
	})

	It("authorizes a hostname below a granted domain with subdomains", func() {
		ns, domain := uniqueNamespace(), uniqueDomain()
		createNamespaceG(ns)
		createDirectoryG(ns)
		createDomainG(ns, domain, testServerName)
		createGrantG(domain, ns, true)
		createUngrantedServerG(ns, withHostname("mail."+domain))
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("True/" + ReasonAuthorized))
	})

	It("blocks a hostname without a grant and keeps serving the domains", func() {
		ns, domain := grantFixture()
		createUngrantedServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		createTLSSecretG(ns)
		deployG(r, ns, testServerName)
		convergeWorkloadsG(ns)
		reconcileServerG(r, ns, testServerName)

		status, reason, msg := serverConditionG(ns, testServerName, ConditionHostnameAuthorized)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonHostnameNotAuthorized))
		Expect(msg).To(ContainSubstring(serverHostnameG(ns, testServerName)))
		// The domain is not touched: it is authorized and served, and
		// the data plane is rendered (no fail-closed replacement).
		status, reason, _ = serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAuthorized))
		Expect(postfixMainCFG(ns)).To(ContainSubstring("virtual_mailbox_domains = " + domain))
		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
		Expect(server.Status.Domains).To(HaveLen(1))
		Expect(server.Status.Domains[0].Served).To(BeTrue())
		// The server is not Ready for what its spec asks for.
		status, reason, _ = serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonHostnameNotAuthorized))
	})

	It("blocks a public suffix as hostname", func() {
		ns, _ := grantFixture()
		createUngrantedServerG(ns, withHostname("co.uk"))
		reconcileServerG(newReconcilerG(AuthorizationUnrestricted, true), ns, testServerName)
		status, reason, msg := serverConditionG(ns, testServerName, ConditionHostnameAuthorized)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonHostnameNotAuthorized))
		Expect(msg).To(ContainSubstring("public suffix"))
	})

	It("blocks both servers that use the same hostname", func() {
		ns, _ := grantFixture()
		host := serverHostnameG(ns, "shared")
		createServerG(ns, testServerName, withHostname(host))
		createServerG(ns, otherServer, withHostname(host))
		for _, name := range []string{testServerName, otherServer} {
			reconcileServerG(r, ns, name)
			Expect(hostnameStateG(ns, name)).To(Equal("False/" + ReasonDuplicateHostname))
		}

		// One leaves: the other is authorized again.
		Expect(k8sClient.Delete(ctx, newServerG(ns, otherServer, nil))).To(Succeed())
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("True/" + ReasonAuthorized))
	})

	It("counts every namespace when authorization is unrestricted", func() {
		ns, ns2 := uniqueNamespace(), uniqueNamespace()
		createNamespaceG(ns)
		createNamespaceG(ns2)
		host := "shared." + ns + ".example.net"
		createUngrantedServerG(ns, withHostname(host))
		createUngrantedServerG(ns2, withHostname(host))
		u := newReconcilerG(AuthorizationUnrestricted, true)
		reconcileServerG(u, ns, testServerName)
		reconcileServerG(u, ns2, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("False/" + ReasonDuplicateHostname))
		Expect(hostnameStateG(ns2, testServerName)).To(Equal("False/" + ReasonDuplicateHostname))
	})

	It("lets a granted namespace keep a hostname another namespace also claims", func() {
		ns, ns2 := uniqueNamespace(), uniqueNamespace()
		createNamespaceG(ns)
		createNamespaceG(ns2)
		host := "shared." + ns + ".example.net"
		createServerG(ns, testServerName, withHostname(host)) // grants host to ns
		createUngrantedServerG(ns2, withHostname(host))
		reconcileServerG(r, ns, testServerName)
		reconcileServerG(r, ns2, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("True/" + ReasonAuthorized))
		Expect(hostnameStateG(ns2, testServerName)).To(Equal("False/" + ReasonHostnameNotAuthorized))
	})

	It("follows a rename and a revoked grant", func() {
		ns, _ := grantFixture()
		createServerG(ns, testServerName, nil)
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("True/" + ReasonAuthorized))

		mutateServerG(ns, withHostname(serverHostnameG(ns, "renamed")))
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("False/" + ReasonHostnameNotAuthorized))

		createGrantG(serverHostnameG(ns, "renamed"), ns, false)
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("True/" + ReasonAuthorized))

		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{
			ObjectMeta: metav1.ObjectMeta{Name: serverHostnameG(ns, "renamed")},
		})).To(Succeed())
		reconcileServerG(r, ns, testServerName)
		Expect(hostnameStateG(ns, testServerName)).To(Equal("False/" + ReasonHostnameNotAuthorized))
	})
})

// The hostname is what the server presents: it replaces the domains on
// the certificate and in HELO, a failing one does not take the domains
// down, and a revoked one is replaced like a revoked domain (ADR 0014).
var _ = Describe("MailServer hostname rendering", func() {
	var ns, domain string
	var r *MailServerReconciler
	settle := func() { deployG(r, ns, testServerName) }
	revokeHostname := func(host string) {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: host}})).To(Succeed())
	}
	own := func() string { return "mail-postfix." + ns + ".svc.cluster.local" }

	BeforeEach(func() {
		installCertificateCRD()
		ns, domain = grantFixture()
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		createTLSSecretG(ns)
		r = newReconcilerG(AuthorizationAllowlist, true)
		settle()
	})

	It("presents the hostname and puts only it on the certificate", func() {
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(testHostnameG(ns)))
		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("\nmyhostname = " + testHostnameG(ns) + "\n"))
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domain + "\n"))
		Expect(cf).ToNot(ContainSubstring(own()))
	})

	It("keeps the hostname and the certificate when a domain joins that sorts first", func() {
		before := certificateDNSNamesG(ns)
		first := "a-" + uniqueDomain() // sorts before the hostname, which starts with "mail."
		createDomainG(ns, first, testServerName)
		createGrantG(first, ns, false)
		settle()
		Expect(postfixMainCFG(ns)).To(ContainSubstring(first))
		Expect(postfixMainCFG(ns)).To(ContainSubstring("\nmyhostname = " + testHostnameG(ns) + "\n"))
		Expect(certificateDNSNamesG(ns)).To(Equal(before))
	})

	It("falls back to the Service name and keeps serving the domains when the hostname is revoked", func() {
		revokeHostname(testHostnameG(ns))
		settle()

		status, reason, _ := serverConditionG(ns, testServerName, ConditionHostnameAuthorized)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonHostnameNotAuthorized))
		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("\nmyhostname = " + own() + "\n"))
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domain + "\n"))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(own()))
		// Domains are untouched.
		status, _, _ = serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
		Expect(status).To(Equal(metav1.ConditionTrue))
	})

	// finishClosedJobs lets the validation Jobs of the fail-closed render
	// (which presents hostname) pass and leaves the others running, or
	// fails them.
	finishClosedJobs := func(hostname string, rejected bool) {
		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
		plane, err := renderDataPlane(server, nil, hostname, nil, nil)
		Expect(err).ToNot(HaveOccurred())
		closed := map[string]bool{}
		for _, v := range plane.Validations {
			closed[v.Job.Name] = true
		}
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
	}

	// A hostname that is revoked while the new configuration is still
	// validating is still presented by the running pods: the fail-closed
	// render replaces them, as for a revoked domain.
	for _, rejected := range []bool{false, true} {
		It(fmt.Sprintf("replaces the running data plane when the hostname is revoked while another change is validating (rejected=%t)", rejected), func() {
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{Enabled: new(false)}
			})
			reconcileServerG(r, ns, testServerName)
			revokeHostname(testHostnameG(ns))
			reconcileServerG(r, ns, testServerName)
			finishClosedJobs("", rejected)
			reconcileServerG(r, ns, testServerName)

			cf := postfixMainCFG(ns)
			Expect(cf).ToNot(ContainSubstring("virtual_mailbox_domains"))
			Expect(cf).To(ContainSubstring("\nmyhostname = " + own() + "\n"))
			Expect(certificateDNSNamesG(ns)).To(ConsistOf(own()))
		})
	}

	It("treats a rename as the revocation of the old name", func() {
		renamed := serverHostnameG(ns, "renamed")
		createGrantG(renamed, ns, false)
		mutateServerG(ns, withHostname(renamed))
		reconcileServerG(r, ns, testServerName)
		// The new configuration is validating while the pods still
		// present the old name: the fail-closed render replaces them.
		finishClosedJobs(renamed, false)
		reconcileServerG(r, ns, testServerName)
		Expect(postfixMainCFG(ns)).ToNot(ContainSubstring("virtual_mailbox_domains"))

		settle()
		cf := postfixMainCFG(ns)
		Expect(cf).To(ContainSubstring("\nmyhostname = " + renamed + "\n"))
		Expect(cf).To(ContainSubstring("virtual_mailbox_domains = " + domain + "\n"))
		Expect(certificateDNSNamesG(ns)).To(ConsistOf(renamed))
	})
})

var _ = Describe("MailServer hostname watches", func() {
	const wait, poll = "20s", "200ms"

	It("frees a hostname when the other server using it goes away or renames", func() {
		ns, ns2 := uniqueNamespace(), uniqueNamespace()
		createNamespaceG(ns)
		createNamespaceG(ns2)
		createDirectoryG(ns)
		createDirectoryG(ns2)
		host := "shared." + ns + ".example.net"
		createUngrantedServerG(ns, withHostname(host))
		createUngrantedServerG(ns2, withHostname(host))
		startManagerG(mailServerSetup(AuthorizationUnrestricted))
		// A not-ready server is re-checked after 30s, far outside the
		// wait: only the watch between the servers can flip the verdict.
		state := func(ns string) func() string { return func() string { return hostnameStateG(ns, testServerName) } }
		Eventually(state(ns), wait, poll).Should(Equal("False/" + ReasonDuplicateHostname))
		Eventually(state(ns2), wait, poll).Should(Equal("False/" + ReasonDuplicateHostname))

		other := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns2}, other)).To(Succeed())
		other.Spec.Hostname = "renamed." + ns + ".example.net"
		Expect(k8sClient.Update(ctx, other)).To(Succeed())
		Eventually(state(ns), wait, poll).Should(Equal("True/" + ReasonAuthorizationDisabled))
		Eventually(state(ns2), wait, poll).Should(Equal("True/" + ReasonAuthorizationDisabled))

		Expect(k8sClient.Delete(ctx, other)).To(Succeed())
		createUngrantedServerG(ns2, withHostname(host))
		Eventually(state(ns), wait, poll).Should(Equal("False/" + ReasonDuplicateHostname))
		Expect(k8sClient.Delete(ctx, newServerG(ns2, testServerName, nil))).To(Succeed())
		Eventually(state(ns), wait, poll).Should(Equal("True/" + ReasonAuthorizationDisabled))
	})
})
