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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

// mxRecords returns the MX records of the status.
func mxRecords(d *cnmsv1alpha1.MailDomain) []cnmsv1alpha1.DNSRecord {
	var out []cnmsv1alpha1.DNSRecord
	for _, rec := range d.Status.Records {
		if rec.Type == dnsrecords.TypeMX {
			out = append(out, rec)
		}
	}
	return out
}

// The MailDomain takes the MX target, the mx host of the policy and
// sending sources from its server (ADR 0014).
var _ = Describe("MailDomain records from the MailServer", func() {
	var ns, domain, name string
	var env *domainTestEnv
	BeforeEach(func() {
		ns = uniqueNamespace()
		domain = uniqueDomain()
		name = domainLabel(domain)
		createNamespaceG(ns)
		createDirectoryG(ns)
	})

	Describe("MX record", func() {
		BeforeEach(func() { env = newDomainEnvG(AuthorizationUnrestricted) })

		It("points at the hostname of the server and is verified", func() {
			createServerG(ns, testServerName, nil)
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)

			d := getMailDomainG(ns, name)
			mx := mxRecords(d)
			Expect(mx).To(HaveLen(1))
			Expect(mx[0].Name).To(Equal(domain))
			Expect(mx[0].Value).To(Equal("10 " + testHostnameG(ns)))
			Expect(mx[0].Verified).To(HaveValue(BeFalse()))

			env.dns.SetMX(domain, dnsrecords.MX{Host: testHostnameG(ns), Pref: 10}, dnsrecords.MX{Host: "backup.example.net", Pref: 20})
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)
			mx = mxRecords(getMailDomainG(ns, name))
			Expect(mx[0].Verified).To(HaveValue(BeTrue()))
		})

		It("is not required without a server", func() {
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)
			Expect(mxRecords(getMailDomainG(ns, name))).To(BeEmpty())
		})

		It("follows a rename of the hostname", func() {
			createServerG(ns, testServerName, nil)
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)

			renamed := serverHostnameG(ns, "renamed")
			createGrantG(renamed, ns, false)
			mutateServerG(ns, withHostname(renamed))
			env.reconcile(ns, name)
			mx := mxRecords(getMailDomainG(ns, name))
			Expect(mx).To(HaveLen(1))
			Expect(mx[0].Value).To(Equal("10 " + renamed))
		})

		It("is not part of the DNSEndpoint of externalDNS mode", func() {
			if !dnsEndpointCRDsInstalled() {
				Skip("needs the DNSEndpoint CRD (DNSENDPOINT_CRDS=1)")
			}
			createServerG(ns, testServerName, nil)
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)

			Expect(mxRecords(getMailDomainG(ns, name))).To(HaveLen(1))
			endpoints, _, err := unstructured.NestedSlice(getEndpointG(ns, name).Object, "spec", "endpoints")
			Expect(err).ToNot(HaveOccurred())
			Expect(endpoints).ToNot(BeEmpty())
			for _, e := range endpoints {
				Expect(e.(map[string]any)["recordType"]).To(Equal(dnsrecords.TypeTXT))
			}
		})
	})

	Describe("with the hostname not authorized", func() {
		BeforeEach(func() {
			env = newDomainEnvG(AuthorizationAllowlist)
			createGrantG(domain, ns, false)
			createUngrantedServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4}}
			})
		})

		It("requires no MX record and publishes no policy, but keeps the sources", func() {
			createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
			})
			env.settle(ns, name)

			d := getMailDomainG(ns, name)
			Expect(mxRecords(d)).To(BeEmpty())
			Expect(d.Status.MTAStS).To(BeNil())
			status, reason, message := domainCondG(ns, name, ConditionMTASTSReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonHostnameNotAuthorized))
			Expect(message).To(ContainSubstring(serverHostnameG(ns, testServerName)))
			Expect(message).ToNot(ContainSubstring("svc.cluster.local"))
			Expect(recordNamed(d, domain).Value).To(Equal("v=spf1 ip4:203.0.113.9 ~all"))
		})

		It("publishes the policy again once the hostname is granted", func() {
			createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
			})
			env.settle(ns, name)
			createGrantG(serverHostnameG(ns, testServerName), ns, false)
			env.reconcile(ns, name)

			d := getMailDomainG(ns, name)
			Expect(d.Status.MTAStS.Policy).To(ContainSubstring("mx: " + testHostnameG(ns) + "\r\n"))
			Expect(mxRecords(d)).To(HaveLen(1))
		})

		It("reports a hostname that two servers use", func() {
			createServerG(ns, otherServer, withHostname(serverHostnameG(ns, testServerName)))
			createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
			})
			env.settle(ns, name)
			status, reason, message := domainCondG(ns, name, ConditionMTASTSReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonHostnameNotAuthorized))
			Expect(message).To(ContainSubstring("2 MailServer objects"))
		})
	})

	Describe("SPF sources", func() {
		BeforeEach(func() { env = newDomainEnvG(AuthorizationUnrestricted) })

		It("adds the sources of the server to the ones of the domain", func() {
			createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4, "198.51.100.7"}, IP6: []string{"2001:db8::9"}}
			})
			createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.SPF = &cnmsv1alpha1.SPF{
					IP4: []string{"198.51.100.7", "192.0.2.5"}, Include: []string{"_spf.example.net"}, Qualifier: new("fail"),
				}
			})
			env.settle(ns, name)

			d := getMailDomainG(ns, name)
			Expect(recordNamed(d, domain).Value).To(Equal(
				"v=spf1 ip4:192.0.2.5 ip4:198.51.100.7 ip4:203.0.113.9 ip6:2001:db8::9 include:_spf.example.net -all"))
			status, reason, _ := domainCondG(ns, name, ConditionSPFReady)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonSPFGenerated))
		})

		It("needs no sources of its own when the server has them", func() {
			createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4}}
			})
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)
			Expect(recordNamed(getMailDomainG(ns, name), domain).Value).To(Equal("v=spf1 ip4:203.0.113.9 ~all"))
		})

		It("leaves out the sources of a server that does not serve the domain", func() {
			createServerG(ns, otherServer, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4}}
			})
			createMailDomainG(ns, domain, nil) // references testServerName, which does not exist
			env.settle(ns, name)
			status, reason, _ := domainCondG(ns, name, ConditionSPFReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonNoSendingSources))
		})

		It("follows a change of the sources", func() {
			createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4}}
			})
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4b}}
			})
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)
			Expect(recordNamed(getMailDomainG(ns, name), domain).Value).To(Equal("v=spf1 ip4:203.0.113.10 ~all"))
		})
	})
})

// Fixtures shared by the specs above and the other MailDomain tests.
const (
	sendIP4  = "203.0.113.9"
	sendIP4b = "203.0.113.10"
	reqAX    = "a/x"
	reqBY    = "b/y"
)
