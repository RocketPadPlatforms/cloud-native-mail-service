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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

// fakeClock is the clock of the MailDomain reconciler in tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now().UTC().Truncate(time.Second)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// domainTestEnv is one MailDomain reconciler with its fake clock.
type domainTestEnv struct {
	r     *MailDomainReconciler
	dns   *dnsrecords.FakeResolver
	clock *fakeClock
}

func newDomainEnvG(mode domainAuthorizationMode) *domainTestEnv {
	clock, dns := newFakeClock(), dnsrecords.NewFakeResolver()
	return &domainTestEnv{
		clock: clock,
		dns:   dns,
		r: &MailDomainReconciler{
			Client:              k8sClient,
			Scheme:              scheme.Scheme,
			APIReader:           k8sClient,
			DomainAuthorization: mode,
			Resolver:            dns,
			Now:                 clock.Now,
		},
	}
}

func (e *domainTestEnv) reconcile(ns, name string) ctrl.Result {
	res, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}})
	Expect(err).ToNot(HaveOccurred())
	return res
}

// createMailDomainG creates a MailDomain named after its domain.
func createMailDomainG(ns, domain string, mutate func(*cnmsv1alpha1.MailDomain)) *cnmsv1alpha1.MailDomain {
	d := &cnmsv1alpha1.MailDomain{
		ObjectMeta: metav1.ObjectMeta{Name: domainLabel(domain), Namespace: ns},
		Spec:       cnmsv1alpha1.MailDomainSpec{Domain: domain},
	}
	if mutate != nil {
		mutate(d)
	}
	Expect(k8sClient.Create(ctx, d)).To(Succeed())
	return d
}

func getMailDomainG(ns, name string) *cnmsv1alpha1.MailDomain {
	d := &cnmsv1alpha1.MailDomain{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, d)).To(Succeed())
	return d
}

// domainCondG returns the status, reason and message of a condition;
// the status is empty when the condition is absent.
func domainCondG(ns, name, condType string) (metav1.ConditionStatus, string, string) {
	c := meta.FindStatusCondition(getMailDomainG(ns, name).Status.Conditions, condType)
	if c == nil {
		return "", "", ""
	}
	return c.Status, c.Reason, c.Message
}

var _ = Describe("MailDomain controller", func() {
	var ns, domain, name string
	BeforeEach(func() {
		ns = uniqueNamespace()
		domain = uniqueDomain()
		name = domainLabel(domain)
		createNamespaceG(ns)
	})

	Describe("authorization", func() {
		It("authorizes a granted domain", func() {
			createGrantG(domain, ns, false)
			createMailDomainG(ns, domain, nil)
			newDomainEnvG(AuthorizationAllowlist).reconcile(ns, name)

			status, reason, _ := domainCondG(ns, name, ConditionAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAuthorized))
			d := getMailDomainG(ns, name)
			Expect(d.Status.ObservedGeneration).To(Equal(d.Generation))
		})

		It("reports the disabled authorization in unrestricted mode", func() {
			createMailDomainG(ns, domain, nil)
			newDomainEnvG(AuthorizationUnrestricted).reconcile(ns, name)

			status, reason, _ := domainCondG(ns, name, ConditionAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAuthorizationDisabled))
		})

		It("blocks a domain without a grant and says why in Ready", func() {
			createMailDomainG(ns, domain, nil)
			newDomainEnvG(AuthorizationAllowlist).reconcile(ns, name)

			status, reason, message := domainCondG(ns, name, ConditionAuthorized)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonNotGranted))
			Expect(message).To(ContainSubstring(domain))
			status, reason, _ = domainCondG(ns, name, conditionReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonNotGranted))
		})

		It("blocks a domain granted to another namespace", func() {
			createGrantG(domain, "someone-else", false)
			createMailDomainG(ns, domain, nil)
			newDomainEnvG(AuthorizationAllowlist).reconcile(ns, name)

			_, reason, _ := domainCondG(ns, name, ConditionAuthorized)
			Expect(reason).To(Equal(ReasonNotGranted))
		})

		It("blocks every claimant of a duplicated domain", func() {
			createGrantG(domain, ns, false)
			createMailDomainG(ns, domain, nil)
			dup := createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) { d.Name = "duplicate" })
			env := newDomainEnvG(AuthorizationAllowlist)
			env.reconcile(ns, name)
			env.reconcile(ns, dup.Name)

			for _, n := range []string{name, dup.Name} {
				status, reason, _ := domainCondG(ns, n, ConditionAuthorized)
				Expect(status).To(Equal(metav1.ConditionFalse))
				Expect(reason).To(Equal(ReasonDuplicateDomain))
			}
		})

		It("revokes a domain when its grant goes away", func() {
			createGrantG(domain, ns, false)
			createMailDomainG(ns, domain, nil)
			env := newDomainEnvG(AuthorizationAllowlist)
			env.reconcile(ns, name)
			status, _, _ := domainCondG(ns, name, ConditionAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))

			Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domain}})).To(Succeed())
			env.reconcile(ns, name)
			status, reason, _ := domainCondG(ns, name, ConditionAuthorized)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonNotGranted))
		})

		It("rejects a public suffix even with a grant", func() {
			// co.uk passes the API pattern but is a public suffix.
			suffix := "co.uk"
			createMailDomainG(ns, suffix, nil)
			newDomainEnvG(AuthorizationUnrestricted).reconcile(ns, domainLabel(suffix))

			status, reason, _ := domainCondG(ns, domainLabel(suffix), ConditionAuthorized)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonPublicSuffix))
		})

		It("does not write the status again when nothing changed", func() {
			createGrantG(domain, ns, false)
			createMailDomainG(ns, domain, nil)
			env := newDomainEnvG(AuthorizationAllowlist)
			env.settle(ns, name)
			first := getMailDomainG(ns, name).ResourceVersion

			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)
			Expect(getMailDomainG(ns, name).ResourceVersion).To(Equal(first))
		})

		It("ignores a MailDomain that is gone", func() {
			newDomainEnvG(AuthorizationAllowlist).reconcile(ns, "missing")
		})
	})
})
