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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

func grantFor(domain, ns string, includeSubdomains bool) *cnmsv1alpha1.MailDomainGrant {
	return &cnmsv1alpha1.MailDomainGrant{
		ObjectMeta: metav1.ObjectMeta{Name: domain},
		Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: ns, IncludeSubdomains: &includeSubdomains},
	}
}

func TestMapGrantDomains(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).WithObjects(
		domainClaimIn("a", "apex", "example.org", "s"),
		domainClaimIn("b", "other-tenant", "example.org", "s"), // competes for the domain
		domainClaimIn("b", "sub", "mail.example.org", "s"),     // covered by includeSubdomains
		domainClaimIn("a", "own-namespace", "unrelated.example", "s"),
		domainClaimIn("c", "unrelated", "other.example", "s"),
		domainClaimIn("c", "lookalike", "notexample.org", "s"), // a suffix, but not a subdomain
	).Build()
	r := &MailDomainReconciler{Client: c}

	got := requestKeys(r.mapGrantDomains(context.Background(), grantFor("example.org", "a", true)))
	slices.Sort(got)
	want := []string{"a/apex", "a/own-namespace", "b/other-tenant", "b/sub"}
	if !slices.Equal(got, want) {
		t.Errorf("mapGrantDomains = %v, want %v", got, want)
	}
}

func TestMapPeerDomains(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).WithObjects(
		domainClaimIn("a", "x", "example.org", "s"),
		domainClaimIn("b", "y", "example.org", "s"),
		domainClaimIn("c", "z", "other.example", "s"),
	).Build()
	r := &MailDomainReconciler{Client: c}

	// A deleted claim is not in the list any more but still reaches the
	// remaining claimants.
	got := requestKeys(r.mapPeerDomains(context.Background(), domainClaimIn("d", "gone", "example.org", "s")))
	if want := []string{reqAX, reqBY}; !slices.Equal(got, want) {
		t.Errorf("mapPeerDomains = %v, want %v", got, want)
	}
}

// The mx host of a policy is the hostname of the server, not one of its
// domains (ADR 0014), so a domain that attaches or leaves reaches no other
// domain of the server.
func TestMapPeerDomainsLeavesTheOtherDomainsOfTheServerAlone(t *testing.T) {
	withMTASTS := func(d *cnmsv1alpha1.MailDomain) *cnmsv1alpha1.MailDomain {
		d.Spec.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true)}
		return d
	}
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).WithObjects(
		withMTASTS(domainClaimIn("a", "on", "on.example.org", "s")),
		domainClaimIn("a", "off", "off.example.org", "s"),
	).Build()
	r := &MailDomainReconciler{Client: c}

	if got := r.mapPeerDomains(context.Background(), domainClaimIn("a", "new", "new.example.org", "s")); len(got) != 0 {
		t.Errorf("mapPeerDomains = %v, want none", requestKeys(got))
	}
}

// mailDomainSetup wires a MailDomainReconciler on the manager's cached
// client, like cmd/main.go does.
func mailDomainSetup(mode domainAuthorizationMode) func(ctrl.Manager) error {
	return func(m ctrl.Manager) error {
		return (&MailDomainReconciler{
			Client: m.GetClient(), Scheme: m.GetScheme(), APIReader: m.GetAPIReader(),
			DomainAuthorization: mode, Resolver: dnsrecords.NewFakeResolver(),
		}).SetupWithManager(m)
	}
}

var _ = Describe("MailDomain watches", func() {
	const wait, poll = "20s", "200ms"

	authorized := func(ns, name string) func() (string, error) {
		return func() (string, error) {
			status, reason, _ := domainCondG(ns, name, ConditionAuthorized)
			return string(status) + "/" + reason, nil
		}
	}

	It("re-evaluates a domain when its grant is deleted and created", func() {
		ns, domain := uniqueNamespace(), uniqueDomain()
		name := domainLabel(domain)
		createNamespaceG(ns)
		createGrantG(domain, ns, false)
		createMailDomainG(ns, domain, nil)
		startManagerG(mailDomainSetup(AuthorizationAllowlist))
		Eventually(authorized(ns, name), wait, poll).Should(Equal("True/" + ReasonAuthorized))

		// The safety-net requeue is ten minutes away: only the grant
		// watch can flip the condition within the wait.
		grant := &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domain}}
		Expect(k8sClient.Delete(ctx, grant)).To(Succeed())
		Eventually(authorized(ns, name), wait, poll).Should(Equal("False/" + ReasonNotGranted))

		createGrantG(domain, ns, false)
		Eventually(authorized(ns, name), wait, poll).Should(Equal("True/" + ReasonAuthorized))
	})

	It("re-evaluates a domain when another namespace claims it", func() {
		ns, ns2, domain := uniqueNamespace(), uniqueNamespace(), uniqueDomain()
		name := domainLabel(domain)
		createNamespaceG(ns)
		createNamespaceG(ns2)
		createMailDomainG(ns, domain, nil)
		startManagerG(mailDomainSetup(AuthorizationUnrestricted))
		Eventually(authorized(ns, name), wait, poll).Should(Equal("True/" + ReasonAuthorizationDisabled))

		createMailDomainG(ns2, domain, nil)
		Eventually(authorized(ns, name), wait, poll).Should(Equal("False/" + ReasonDuplicateDomain))
	})

	It("creates the DNSEndpoint again when it is deleted, if the CRD is installed", func() {
		if !dnsEndpointCRDsInstalled() {
			Skip("needs the DNSEndpoint CRD (DNSENDPOINT_CRDS=1)")
		}
		ns, domain := uniqueNamespace(), uniqueDomain()
		name := domainLabel(domain)
		createNamespaceG(ns)
		createMailDomainG(ns, domain, externalDNS)
		startManagerG(mailDomainSetup(AuthorizationUnrestricted))
		endpoint := func() (bool, error) { return getEndpointG(ns, name) != nil, nil }
		Eventually(endpoint, wait, poll).Should(BeTrue())

		Expect(k8sClient.Delete(ctx, getEndpointG(ns, name))).To(Succeed())
		Eventually(endpoint, wait, poll).Should(BeTrue())
	})
})
