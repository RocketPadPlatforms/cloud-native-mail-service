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
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

const (
	lbIP4 = "203.0.113.50"
	lbIP6 = "2001:db8::50"
)

// recordsEnv is a MailServer reconciler with a fake resolver and clock.
type recordsEnv struct {
	r     *MailServerReconciler
	dns   *dnsrecords.FakeResolver
	clock *fakeClock
}

func newRecordsEnvG(mode domainAuthorizationMode) *recordsEnv {
	clock, dns := newFakeClock(), dnsrecords.NewFakeResolver()
	r := newReconcilerG(mode, true)
	r.Resolver, r.Now = dns, clock.Now
	r.Scheme = scheme.Scheme
	return &recordsEnv{r: r, dns: dns, clock: clock}
}

// setIngressG sets the LoadBalancer status of the inbound Service,
// which exists once the server was reconciled (envtest runs no cloud
// controller).
func setIngressG(ns string, ingress ...corev1.LoadBalancerIngress) {
	svc := &corev1.Service{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, svc)).To(Succeed())
	svc.Status.LoadBalancer.Ingress = ingress
	Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())
}

func getServerG(ns string) *cnmsv1alpha1.MailServer {
	s := &cnmsv1alpha1.MailServer{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, s)).To(Succeed())
	return s
}

// serverRecordsG returns "<type> <name> <value>" of the status records.
func serverRecordsG(ns string) []string {
	records := getServerG(ns).Status.Records
	out := make([]string, 0, len(records))
	for _, rec := range records {
		out = append(out, rec.Type+" "+rec.Name+" "+rec.Value)
	}
	return out
}

var _ = Describe("MailServer records", func() {
	var ns string
	var env *recordsEnv
	var host string
	withSPF := func(s *cnmsv1alpha1.MailServer) {
		s.Spec.SPF = &cnmsv1alpha1.ServerSPF{IP4: []string{sendIP4c}, IP6: []string{"2001:db8::7"}}
	}
	spfValue := "v=spf1 ip4:198.51.100.7 ip6:2001:db8::7 -all"

	BeforeEach(func() {
		ns, _ = grantFixture()
		host = testHostnameG(ns)
		env = newRecordsEnvG(AuthorizationAllowlist)
	})
	// deploy reconciles the server twice so the Services exist, and
	// sets the ingress of the inbound Service.
	deploy := func(ingress ...corev1.LoadBalancerIngress) {
		reconcileServerG(env.r, ns, testServerName)
		setIngressG(ns, ingress...)
		reconcileServerG(env.r, ns, testServerName)
	}

	It("lists an A and an AAAA record per address and the SPF record of the hostname", func() {
		createServerG(ns, testServerName, withSPF)
		deploy(corev1.LoadBalancerIngress{IP: lbIP6}, corev1.LoadBalancerIngress{IP: lbIP4})

		Expect(serverRecordsG(ns)).To(Equal([]string{
			"A " + host + " " + lbIP4,
			"AAAA " + host + " " + lbIP6,
			"TXT " + host + " " + spfValue,
		}))
		status, reason, _ := serverConditionG(ns, testServerName, ConditionAddressRecords)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAddressesListed))
		// The lookups have not found anything yet.
		for _, rec := range getServerG(ns).Status.Records {
			Expect(rec.Verified).To(HaveValue(BeFalse()), rec.Type)
		}
		status, reason, _ = serverConditionG(ns, testServerName, ConditionRecordsVerified)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonRecordsNotVerified))
	})

	It("verifies the records through the resolver and never lets them decide on Ready", func() {
		createServerG(ns, testServerName, withSPF)
		createTLSSecretG(ns)
		deployG(env.r, ns, testServerName)
		setIngressG(ns, corev1.LoadBalancerIngress{IP: lbIP4})
		convergeWorkloadsG(ns)
		reconcileServerG(env.r, ns, testServerName)
		status, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionTrue), reason)
		Expect(serverRecordsG(ns)).ToNot(BeEmpty())
		verified, _, _ := serverConditionG(ns, testServerName, ConditionRecordsVerified)
		Expect(verified).To(Equal(metav1.ConditionFalse))

		env.dns.SetHost(host, lbIP4, "203.0.113.99")
		env.dns.Set(host, spfValue)
		env.clock.Advance(dnsrecords.UnverifiedMax)
		reconcileServerG(env.r, ns, testServerName)
		for _, rec := range getServerG(ns).Status.Records {
			Expect(rec.Verified).To(HaveValue(BeTrue()), rec.Type)
		}
		status, reason, _ = serverConditionG(ns, testServerName, ConditionRecordsVerified)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAllVerified))
	})

	It("keeps the last answer and writes nothing while a lookup fails or is not due", func() {
		createServerG(ns, testServerName, nil)
		deploy(corev1.LoadBalancerIngress{IP: lbIP4})
		env.dns.SetHost(host, lbIP4)
		env.clock.Advance(dnsrecords.UnverifiedMax)
		reconcileServerG(env.r, ns, testServerName)
		Expect(getServerG(ns).Status.Records[0].Verified).To(HaveValue(BeTrue()))

		version := getServerG(ns).ResourceVersion
		reconcileServerG(env.r, ns, testServerName) // not due
		Expect(getServerG(ns).ResourceVersion).To(Equal(version))

		env.dns.SetError(host, errors.New("SERVFAIL"))
		env.clock.Advance(2 * time.Hour)
		reconcileServerG(env.r, ns, testServerName)
		Expect(getServerG(ns).Status.Records[0].Verified).To(HaveValue(BeTrue()))
	})

	It("lists no address record for a ClusterIP Service and says why", func() {
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			withSPF(s)
			s.Spec.Inbound = &cnmsv1alpha1.Inbound{ServiceType: new("ClusterIP")}
		})
		// The Service exists after the first pass.
		reconcileServerG(env.r, ns, testServerName)
		reconcileServerG(env.r, ns, testServerName)

		Expect(serverRecordsG(ns)).To(Equal([]string{"TXT " + host + " " + spfValue}))
		status, reason, message := serverConditionG(ns, testServerName, ConditionAddressRecords)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoExternalAddress))
		Expect(message).To(ContainSubstring("ClusterIP"))
	})

	It("lists no address record for a LoadBalancer with a DNS name", func() {
		createServerG(ns, testServerName, nil)
		deploy(corev1.LoadBalancerIngress{Hostname: "lb.example.net"})

		Expect(serverRecordsG(ns)).To(BeEmpty())
		status, reason, message := serverConditionG(ns, testServerName, ConditionAddressRecords)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonLoadBalancerHostname))
		Expect(message).To(ContainSubstring("lb.example.net"))
		status, reason, _ = serverConditionG(ns, testServerName, ConditionRecordsVerified)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonNoRecords))
	})

	It("waits for the address of a LoadBalancer", func() {
		createServerG(ns, testServerName, nil)
		deploy()
		status, reason, _ := serverConditionG(ns, testServerName, ConditionAddressRecords)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonAddressPending))

		setIngressG(ns, corev1.LoadBalancerIngress{IP: lbIP4})
		reconcileServerG(env.r, ns, testServerName)
		Expect(serverRecordsG(ns)).To(Equal([]string{"A " + host + " " + lbIP4}))
	})

	It("leaves out the SPF record when a domain of the same name carries it", func() {
		ns, _ = grantFixture() // a fresh namespace for a hostname that is a domain
		apex := uniqueDomain()
		createDomainG(ns, apex, testServerName)
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			withSPF(s)
			s.Spec.Hostname = apex
		})
		deploy(corev1.LoadBalancerIngress{IP: lbIP4})
		Expect(serverRecordsG(ns)).To(Equal([]string{"A " + apex + " " + lbIP4}))
	})

	It("reports the SPF record when the domain of the same name belongs to another server", func() {
		ns, _ = grantFixture()
		apex := uniqueDomain()
		createDomainG(ns, apex, otherServer)
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			withSPF(s)
			s.Spec.Hostname = apex
		})
		deploy(corev1.LoadBalancerIngress{IP: lbIP4})
		Expect(serverRecordsG(ns)).To(Equal([]string{
			"A " + apex + " " + lbIP4,
			"TXT " + apex + " " + spfValue,
		}))
	})

	It("cuts the address records so that the SPF record still fits", func() {
		createServerG(ns, testServerName, withSPF)
		ingress := make([]corev1.LoadBalancerIngress, 0, maxServerRecords+6)
		for i := range maxServerRecords + 6 {
			ingress = append(ingress, corev1.LoadBalancerIngress{IP: fmt.Sprintf("203.0.113.%d", i+1)})
		}
		deploy(ingress...)
		records := serverRecordsG(ns)
		Expect(records).To(HaveLen(maxServerRecords))
		Expect(records[len(records)-1]).To(Equal("TXT " + host + " " + spfValue))
	})

	It("lists nothing while the hostname is not authorized", func() {
		createUngrantedServerG(ns, withSPF)
		deploy(corev1.LoadBalancerIngress{IP: lbIP4})
		Expect(getServerG(ns).Status.Records).To(BeEmpty())
		Expect(meta.FindStatusCondition(getServerG(ns).Status.Conditions, ConditionAddressRecords)).To(BeNil())
		Expect(meta.FindStatusCondition(getServerG(ns).Status.Conditions, ConditionRecordsVerified)).To(BeNil())

		// Granted later, the records appear.
		createGrantG(host, ns, false)
		reconcileServerG(env.r, ns, testServerName)
		Expect(serverRecordsG(ns)).To(HaveLen(2))
	})

	It("follows a rename of the hostname", func() {
		createServerG(ns, testServerName, nil)
		deploy(corev1.LoadBalancerIngress{IP: lbIP4})
		renamed := serverHostnameG(ns, "renamed")
		createGrantG(renamed, ns, false)
		mutateServerG(ns, withHostname(renamed))
		reconcileServerG(env.r, ns, testServerName)
		Expect(serverRecordsG(ns)).To(Equal([]string{"A " + renamed + " " + lbIP4}))
	})

	It("reports Unknown, not a failure, without a resolver", func() {
		createServerG(ns, testServerName, nil)
		env.r.Resolver = nil
		deploy(corev1.LoadBalancerIngress{IP: lbIP4})
		status, reason, _ := serverConditionG(ns, testServerName, ConditionRecordsVerified)
		Expect(status).To(Equal(metav1.ConditionUnknown))
		Expect(reason).To(Equal(ReasonChecking))
	})
})
