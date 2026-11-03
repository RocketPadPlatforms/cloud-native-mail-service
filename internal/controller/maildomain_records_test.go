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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

// settle reconciles twice: the first pass creates the key, the second
// looks its record up.
func (e *domainTestEnv) settle(ns, name string) {
	e.reconcile(ns, name)
	e.reconcile(ns, name)
}

// restart replaces the reconciler by a new one with an empty
// verification state, as after an operator restart.
func (e *domainTestEnv) restart() {
	old := e.r
	e.r = &MailDomainReconciler{
		Client: old.Client, Scheme: old.Scheme, APIReader: old.APIReader,
		DomainAuthorization: old.DomainAuthorization, Resolver: old.Resolver,
		MTASTSHosts: old.MTASTSHosts, Now: old.Now,
	}
}

// publish puts every record of the status into the fake DNS.
func (e *domainTestEnv) publish(d *cnmsv1alpha1.MailDomain) {
	for _, rec := range d.Status.Records {
		e.dns.Set(rec.Name, rec.Value)
	}
}

func recordNames(d *cnmsv1alpha1.MailDomain) []string {
	names := make([]string, 0, len(d.Status.Records))
	for _, rec := range d.Status.Records {
		names = append(names, rec.Name)
	}
	return names
}

func recordNamed(d *cnmsv1alpha1.MailDomain, name string) cnmsv1alpha1.DNSRecord {
	for _, rec := range d.Status.Records {
		if rec.Name == name {
			return rec
		}
	}
	Fail("no record named " + name)
	return cnmsv1alpha1.DNSRecord{}
}

var _ = Describe("MailDomain records", func() {
	var ns, domain, name string
	var env *domainTestEnv
	BeforeEach(func() {
		ns = uniqueNamespace()
		domain = uniqueDomain()
		name = domainLabel(domain)
		createNamespaceG(ns)
		env = newDomainEnvG(AuthorizationUnrestricted)
	})

	It("lists every required record with its value", func() {
		env.r.MTASTSHosts = func(context.Context, *cnmsv1alpha1.MailDomain) ([]string, error) {
			return []string{"mail." + domain}, nil
		}
		createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
			d.Spec.SPF = &cnmsv1alpha1.SPF{IP4: []string{"203.0.113.77"}}
			d.Spec.DMARC = &cnmsv1alpha1.DMARC{Policy: new("reject"), Rua: new("dmarc@" + domain)}
			d.Spec.TLSRPT = &cnmsv1alpha1.TLSRPT{Enabled: new(true), Rua: new("tlsrpt@" + domain)}
			d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true), Mode: new("enforce")}
		})
		env.settle(ns, name)

		d := getMailDomainG(ns, name)
		selector := dkim.Selector("", dkim.RSA, env.clock.Now())
		Expect(recordNames(d)).To(Equal([]string{
			selector + "._domainkey." + domain, domain, "_dmarc." + domain, "_mta-sts." + domain, "_smtp._tls." + domain,
		}))
		key, err := dkim.Parse(keySecretsG(ns, name)[0].Data[keySecretDataKey])
		Expect(err).ToNot(HaveOccurred())
		Expect(recordNamed(d, selector+"._domainkey."+domain).Value).To(Equal(key.TXTValue()))
		Expect(recordNamed(d, domain).Value).To(Equal("v=spf1 ip4:203.0.113.77 ~all"))
		Expect(recordNamed(d, "_dmarc."+domain).Value).To(Equal("v=DMARC1; p=reject; rua=mailto:dmarc@" + domain))
		Expect(recordNamed(d, "_smtp._tls."+domain).Value).To(Equal("v=TLSRPTv1; rua=mailto:tlsrpt@" + domain))
		Expect(d.Status.MTAStS.Policy).To(ContainSubstring("mode: enforce\r\nmx: mail." + domain + "\r\n"))
		Expect(recordNamed(d, "_mta-sts."+domain).Value).To(Equal("v=STSv1; id=" + d.Status.MTAStS.PolicyID))
		for _, rec := range d.Status.Records {
			Expect(rec.Type).To(Equal("TXT"))
		}
		for _, c := range []string{ConditionSPFReady, ConditionMTASTSReady} {
			status, _, _ := domainCondG(ns, name, c)
			Expect(status).To(Equal(metav1.ConditionTrue), c)
		}
	})

	It("reports missing MTA-STS hosts and SPF sources instead of records", func() {
		createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
			d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
		})
		env.settle(ns, name)

		d := getMailDomainG(ns, name)
		Expect(d.Status.Records).To(HaveLen(1)) // the DKIM record only
		Expect(d.Status.MTAStS).To(BeNil())
		status, reason, _ := domainCondG(ns, name, ConditionMTASTSReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoMailServer))
		status, reason, _ = domainCondG(ns, name, ConditionSPFReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoSendingSources))
	})

	It("warns when a report address needs an authorization record", func() {
		createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
			d.Spec.DMARC = &cnmsv1alpha1.DMARC{Rua: new("reports@example.com")}
		})
		env.settle(ns, name)

		status, reason, message := domainCondG(ns, name, ConditionReportAuthorization)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonReceiverRecordRequired))
		Expect(message).To(ContainSubstring(domain + "._report._dmarc.example.com"))
	})

	Describe("verification", func() {
		var key string
		BeforeEach(func() {
			createMailDomainG(ns, domain, nil)
			key = dkim.Selector("", dkim.RSA, env.clock.Now()) + "._domainkey." + domain
		})

		It("verifies the records through the resolver", func() {
			res := env.reconcile(ns, name)
			// A key that was just created is looked up at once.
			Expect(res.RequeueAfter).To(Equal(time.Second))
			res = env.reconcile(ns, name)
			Expect(res.RequeueAfter).To(Equal(dnsrecords.UnverifiedMin))

			d := getMailDomainG(ns, name)
			Expect(d.Status.Records).To(HaveLen(1))
			Expect(d.Status.Records[0].Verified).To(HaveValue(BeFalse()))
			Expect(d.Status.Records[0].LastChecked.Time).To(BeTemporally("==", env.clock.Now()))
			status, reason, message := domainCondG(ns, name, ConditionRecordsVerified)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonRecordsNotVerified))
			Expect(message).To(ContainSubstring(key))
			status, reason, _ = domainCondG(ns, name, conditionReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonRecordsNotVerified))

			env.publish(d)
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)
			d = getMailDomainG(ns, name)
			Expect(d.Status.Records[0].Verified).To(HaveValue(BeTrue()))
			Expect(d.Status.Records[0].LastChecked.Time).To(BeTemporally("==", env.clock.Now()))
			status, reason, _ = domainCondG(ns, name, ConditionRecordsVerified)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAllVerified))
			status, reason, _ = domainCondG(ns, name, conditionReady)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonReady))
		})

		It("backs off while a record is not verified and writes nothing", func() {
			env.settle(ns, name)
			version := getMailDomainG(ns, name).ResourceVersion
			for attempt := 1; attempt < 6; attempt++ {
				env.clock.Advance(dnsrecords.NextCheck(false, attempt-1))
				res := env.reconcile(ns, name)
				Expect(res.RequeueAfter).To(Equal(dnsrecords.NextCheck(false, attempt)), "attempt %d", attempt)
			}
			Expect(getMailDomainG(ns, name).ResourceVersion).To(Equal(version))
		})

		It("does not look the records up again before they are due", func() {
			env.settle(ns, name)
			env.dns.Set(key, "v=DKIM1; k=rsa; p=AAAA") // a wrong value: not verified either way
			lookups := 0
			env.r.Resolver = countingResolver{env.dns, &lookups}
			env.clock.Advance(10 * time.Second)
			env.reconcile(ns, name)
			Expect(lookups).To(BeZero())
			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)
			Expect(lookups).To(Equal(1))
		})

		It("keeps the status unchanged when the operator restarts", func() {
			env.settle(ns, name)
			env.publish(getMailDomainG(ns, name))
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)
			version := getMailDomainG(ns, name).ResourceVersion

			env.restart()
			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)
			Expect(getMailDomainG(ns, name).ResourceVersion).To(Equal(version))
		})

		It("refreshes lastChecked of a verified record once an hour", func() {
			env.settle(ns, name)
			env.publish(getMailDomainG(ns, name))
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)
			checked := getMailDomainG(ns, name).Status.Records[0].LastChecked.Time
			version := getMailDomainG(ns, name).ResourceVersion

			env.clock.Advance(dnsrecords.VerifiedInterval - time.Minute)
			env.reconcile(ns, name)
			Expect(getMailDomainG(ns, name).ResourceVersion).To(Equal(version))
			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)
			d := getMailDomainG(ns, name)
			Expect(d.Status.Records[0].LastChecked.Time).To(BeTemporally("==", checked.Add(dnsrecords.VerifiedInterval)))
		})

		It("keeps the previous answer on a temporary error and turns Unknown after repeated ones", func() {
			env.settle(ns, name)
			env.publish(getMailDomainG(ns, name))
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)

			env.dns.SetError(key, errors.New("SERVFAIL"))
			for failure := 1; failure <= dnsrecords.FailuresBeforeUnknown; failure++ {
				status, _, _ := domainCondG(ns, name, ConditionRecordsVerified)
				Expect(status).To(Equal(metav1.ConditionTrue), "before failure %d", failure)
				env.clock.Advance(dnsrecords.VerifiedInterval)
				env.reconcile(ns, name)
				Expect(getMailDomainG(ns, name).Status.Records[0].Verified).To(HaveValue(BeTrue()))
				env.clock.Advance(dnsrecords.UnverifiedMax)
			}
			status, reason, _ := domainCondG(ns, name, ConditionRecordsVerified)
			Expect(status).To(Equal(metav1.ConditionUnknown))
			Expect(reason).To(Equal(ReasonLookupFailing))
			status, _, _ = domainCondG(ns, name, conditionReady)
			Expect(status).To(Equal(metav1.ConditionFalse))

			env.dns.Set(key, getMailDomainG(ns, name).Status.Records[0].Value)
			env.clock.Advance(dnsrecords.UnverifiedMax)
			env.reconcile(ns, name)
			status, _, _ = domainCondG(ns, name, ConditionRecordsVerified)
			Expect(status).To(Equal(metav1.ConditionTrue))
		})

		It("is Unknown, not False, while a lookup has never answered", func() {
			env.dns.SetError(key, errors.New("i/o timeout"))
			env.settle(ns, name)
			status, reason, _ := domainCondG(ns, name, ConditionRecordsVerified)
			Expect(status).To(Equal(metav1.ConditionUnknown))
			Expect(reason).To(Equal(ReasonChecking))
			Expect(getMailDomainG(ns, name).Status.Records[0].Verified).To(BeNil())

			for range dnsrecords.FailuresBeforeUnknown {
				env.clock.Advance(dnsrecords.UnverifiedMax)
				env.reconcile(ns, name)
			}
			status, reason, _ = domainCondG(ns, name, ConditionRecordsVerified)
			Expect(status).To(Equal(metav1.ConditionUnknown))
			Expect(reason).To(Equal(ReasonLookupFailing))
		})
	})

	Describe("rotation with overlap", func() {
		It("keeps both selectors valid while the key changes", func() {
			createMailDomainG(ns, domain, withDKIM(func(d *cnmsv1alpha1.DKIM) {
				d.Rotation = &cnmsv1alpha1.DKIMRotation{
					Interval: metav1.Duration{Duration: 24 * time.Hour},
					Overlap:  &metav1.Duration{Duration: time.Hour},
				}
			}))
			first := dkim.Selector("", dkim.RSA, env.clock.Now())
			env.settle(ns, name)
			env.publish(getMailDomainG(ns, name))
			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)

			// The interval is over: the new key is created and published, the old one signs.
			env.clock.Advance(25 * time.Hour)
			env.settle(ns, name)
			second := dkim.Selector("", dkim.RSA, env.clock.Now())
			d := getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{first}))
			Expect(selectorsOf(d.Status.DKIM.Pending)).To(Equal([]string{second}))
			Expect(recordNames(d)).To(ConsistOf(
				first+"._domainkey."+domain, second+"._domainkey."+domain))
			status, reason, _ := domainCondG(ns, name, ConditionDKIMRotation)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonAwaitingDNS))
			Expect(recordNamed(d, second+"._domainkey."+domain).Verified).To(HaveValue(BeFalse()))

			// Once the record is published it has to stay verified for the hold period.
			env.publish(d)
			env.clock.Advance(dnsrecords.UnverifiedMin)
			env.reconcile(ns, name)
			Expect(recordNamed(getMailDomainG(ns, name), second+"._domainkey."+domain).Verified).To(HaveValue(BeTrue()))
			env.clock.Advance(dkim.HoldPeriod - time.Minute)
			env.reconcile(ns, name)
			Expect(selectorsOf(getMailDomainG(ns, name).Status.DKIM.Active)).To(Equal([]string{first}))

			env.clock.Advance(2 * time.Minute)
			env.reconcile(ns, name)
			d = getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{second}))
			Expect(selectorsOf(d.Status.DKIM.Retiring)).To(Equal([]string{first}))
			Expect(d.Status.DKIM.Retiring[0].Until.Time).To(BeTemporally("==", env.clock.Now().Add(time.Hour)))
			Expect(d.Status.DKIM.Pending).To(BeEmpty())
			// Both selectors stay required, and so verifiable, during the overlap.
			Expect(recordNames(d)).To(ConsistOf(
				first+"._domainkey."+domain, second+"._domainkey."+domain))
			Expect(secretNames(keySecretsG(ns, name))).To(HaveLen(2))
			status, _, _ = domainCondG(ns, name, ConditionDKIMRotation)
			Expect(status).To(Equal(metav1.ConditionTrue))

			env.clock.Advance(30 * time.Minute)
			env.reconcile(ns, name)
			Expect(recordNames(getMailDomainG(ns, name))).To(HaveLen(2))

			// The overlap is over: the old record leaves the set and its Secret is deleted.
			env.clock.Advance(31 * time.Minute)
			env.reconcile(ns, name)
			d = getMailDomainG(ns, name)
			Expect(d.Status.DKIM.Retiring).To(BeEmpty())
			Expect(recordNames(d)).To(Equal([]string{second + "._domainkey." + domain}))
			Expect(secretNames(keySecretsG(ns, name))).To(Equal([]string{dkim.SecretName(name, second)}))
			state, _ := rotationStateG(ns, name)
			Expect(state.Active).To(Equal([]string{second}))
			Expect(state.Retiring).To(BeEmpty())
		})

		It("does not activate a key whose record never verifies", func() {
			createMailDomainG(ns, domain, withDKIM(func(d *cnmsv1alpha1.DKIM) {
				d.Rotation = &cnmsv1alpha1.DKIMRotation{
					Interval: metav1.Duration{Duration: 24 * time.Hour},
					Overlap:  &metav1.Duration{Duration: time.Hour},
				}
			}))
			first := dkim.Selector("", dkim.RSA, env.clock.Now())
			env.settle(ns, name)
			env.clock.Advance(25 * time.Hour)
			env.settle(ns, name)
			for range 10 {
				env.clock.Advance(time.Hour)
				env.reconcile(ns, name)
			}
			d := getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{first}))
			Expect(d.Status.DKIM.Pending).To(HaveLen(1))
			_, reason, _ := domainCondG(ns, name, ConditionDKIMRotation)
			Expect(reason).To(Equal(ReasonAwaitingDNS))
		})
	})
})

// countingResolver counts lookups.
type countingResolver struct {
	dnsrecords.Resolver
	n *int
}

func (c countingResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	*c.n++
	return c.Resolver.LookupTXT(ctx, name)
}
