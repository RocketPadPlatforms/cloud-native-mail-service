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
	"slices"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

func TestMapServerDomains(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailDomain{}, mailDomainServerIndex, indexMailDomainByServer).
		WithObjects(
			domainClaimIn("a", "x", "x.example.org", "s1"),
			domainClaimIn("a", "y", "y.example.org", "s1"),
			domainClaimIn("a", "z", "z.example.org", "s2"),
			domainClaimIn("b", "w", "w.example.org", "s1"), // same server name, other namespace
		).Build()
	r := &MailDomainReconciler{Client: c}
	// The domains are in the namespace of the server; a server that is
	// gone is mapped by its name.
	got := requestKeys(r.mapServerDomains(context.Background(), serverIn("a", "s1")))
	if want := []string{"a/x", "a/y"}; !slices.Equal(got, want) {
		t.Errorf("mapServerDomains = %v, want %v", got, want)
	}
	if got := r.mapServerDomains(context.Background(), &cnmsv1alpha1.MailDomain{}); got != nil {
		t.Errorf("mapServerDomains(MailDomain) = %v, want none", got)
	}
}

var _ = Describe("MailDomain MTA-STS hosts", func() {
	var ns, domain, name string
	var env *domainTestEnv
	enableMTASTS := func(d *cnmsv1alpha1.MailDomain) {
		d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
	}
	policyHosts := func() string {
		env.settle(ns, name)
		d := getMailDomainG(ns, name)
		if d.Status.MTAStS == nil {
			return ""
		}
		return d.Status.MTAStS.Policy
	}
	BeforeEach(func() {
		ns = uniqueNamespace()
		domain = uniqueDomain()
		name = domainLabel(domain)
		createNamespaceG(ns)
		createDirectoryG(ns)
		env = newDomainEnvG(AuthorizationUnrestricted)
		createMailDomainG(ns, domain, enableMTASTS)
	})

	It("has no policy while no MailServer serves the domain", func() {
		Expect(policyHosts()).To(BeEmpty())
		status, reason, _ := domainCondG(ns, name, ConditionMTASTSReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoMailServer))

		// A server of the namespace that the domain does not reference
		// does not count.
		createServerG(ns, otherServer, nil)
		Expect(policyHosts()).To(BeEmpty())
	})

	It("lists the host name of the server that serves the domain", func() {
		createServerG(ns, "mail", nil)
		Expect(policyHosts()).To(ContainSubstring("mx: " + domain + "\r\n"))
		status, reason, _ := domainCondG(ns, name, ConditionMTASTSReady)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonPolicyAvailable))
		d := getMailDomainG(ns, name)
		Expect(recordNamed(d, "_mta-sts."+domain).Value).To(Equal("v=STSv1; id=" + d.Status.MTAStS.PolicyID))
	})

	It("lists the primary domain of a server that serves several domains", func() {
		// The server presents its first sorted domain: a subdomain sorts
		// before its parent.
		sub := "a." + domain
		createMailDomainG(ns, sub, nil)
		createServerG(ns, "mail", nil)
		Expect(policyHosts()).To(ContainSubstring("mx: " + sub + "\r\n"))
		Expect(policyHosts()).ToNot(ContainSubstring("mx: " + domain + "\r\n"))
	})

	It("follows the domain to the server it moves to", func() {
		sub := "a." + domain
		createMailDomainG(ns, sub, func(d *cnmsv1alpha1.MailDomain) { d.Spec.ServerRef.Name = "mail-2" })
		createServerG(ns, "mail", nil)
		createServerG(ns, "mail-2", nil)
		Expect(policyHosts()).To(ContainSubstring("mx: " + domain + "\r\n"))

		d := getMailDomainG(ns, name)
		d.Spec.ServerRef.Name = "mail-2"
		Expect(k8sClient.Update(ctx, d)).To(Succeed())
		// mail-2 now serves both; its first sorted domain is the host.
		Expect(policyHosts()).To(ContainSubstring("mx: " + sub + "\r\n"))
	})

	It("does not list a server of another namespace", func() {
		ns2 := uniqueNamespace()
		createNamespaceG(ns2)
		createDirectoryG(ns2)
		createServerG(ns2, "mail", nil)
		Expect(policyHosts()).To(BeEmpty())
	})
})

var _ = Describe("MailDomain MTA-STS watches", func() {
	It("follows a MailServer that starts serving the domain", func() {
		const wait, poll = "20s", "200ms"
		ns, domain := uniqueNamespace(), uniqueDomain()
		name := domainLabel(domain)
		createNamespaceG(ns)
		createDirectoryG(ns)
		createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
			d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
		})
		startManagerG(mailDomainSetup(AuthorizationUnrestricted))
		reason := func() (string, error) {
			_, r, _ := domainCondG(ns, name, ConditionMTASTSReady)
			return r, nil
		}
		Eventually(reason, wait, poll).Should(Equal(ReasonNoMailServer))

		// The ten-minute safety net is far outside the wait: only the
		// MailServer watch can flip the condition.
		createServerG(ns, "mail", nil)
		Eventually(reason, wait, poll).Should(Equal(ReasonPolicyAvailable))
		Expect(getMailDomainG(ns, name).Status.MTAStS.Policy).To(ContainSubstring("mx: " + domain))

		Expect(k8sClient.Delete(ctx, newServerG(ns, "mail", nil))).To(Succeed())
		Eventually(reason, wait, poll).Should(Equal(ReasonNoMailServer))
	})
})
