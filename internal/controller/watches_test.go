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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

func watchTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := cnmsv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func serverIn(ns, name string) *cnmsv1alpha1.MailServer {
	return &cnmsv1alpha1.MailServer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func domainClaimIn(ns, name, domain, server string) *cnmsv1alpha1.MailDomain {
	return &cnmsv1alpha1.MailDomain{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cnmsv1alpha1.MailDomainSpec{
			Domain:    domain,
			ServerRef: cnmsv1alpha1.NameReference{Name: server},
		},
	}
}

func requestKeys(reqs []reconcile.Request) []string {
	keys := make([]string, 0, len(reqs))
	for _, r := range reqs {
		keys = append(keys, r.Namespace+"/"+r.Name)
	}
	return keys
}

func TestMapDomainServersCoversEveryClaimantsServer(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailDomain{}, mailDomainDomainIndex, indexMailDomainByDomain).
		WithObjects(
			domainClaimIn("a", "x", "example.org", "s1"),
			domainClaimIn("b", "y", "example.org", "s2"),
			domainClaimIn("c", "z", "other.example", "s3"),
		).Build()
	r := &MailServerReconciler{Client: c}

	got := requestKeys(r.mapDomainServers(context.Background(), domainClaimIn("a", "x", "example.org", "s1")))
	if len(got) != 2 || got[0] != "a/s1" || got[1] != "b/s2" {
		t.Errorf("mapDomainServers = %v, want [a/s1 b/s2]", got)
	}

	// A deleted claim (not in the cache any more) still reaches the
	// server it was attached to and the servers of the remaining
	// claimants; the old object of a moved claim does the same for the
	// server it left.
	got = requestKeys(r.mapDomainServers(context.Background(), domainClaimIn("d", "gone", "example.org", "s0")))
	if len(got) != 3 || got[0] != "a/s1" || got[1] != "b/s2" || got[2] != "d/s0" {
		t.Errorf("mapDomainServers(deleted) = %v, want [a/s1 b/s2 d/s0]", got)
	}
}

func TestMapGrantServersEnqueuesEveryServer(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithObjects(serverIn("a", "s1"), serverIn("b", "s2")).Build()
	r := &MailServerReconciler{Client: c}
	grant := &cnmsv1alpha1.MailDomainGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "example.org"},
		Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: "a"},
	}
	got := requestKeys(r.mapGrantServers(context.Background(), grant))
	if len(got) != 2 {
		t.Errorf("mapGrantServers = %v, want both servers (also in other namespaces)", got)
	}
}

func TestMapNamespaceGrants(t *testing.T) {
	grant := func(name, ns string) *cnmsv1alpha1.MailDomainGrant {
		return &cnmsv1alpha1.MailDomainGrant{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: ns},
		}
	}
	c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailDomainGrant{}, grantNamespaceIndex, indexGrantByNamespace).
		WithObjects(grant("a.example", "a"), grant("b.example", "a"), grant("c.example", "b")).Build()
	r := &MailDomainGrantReconciler{Client: c}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	got := r.mapNamespaceGrants(context.Background(), ns)
	if len(got) != 2 || got[0].Name != "a.example" || got[1].Name != "b.example" {
		t.Errorf("mapNamespaceGrants = %v, want the two grants of namespace a", got)
	}
}

func TestFinishRequeuesEveryServer(t *testing.T) {
	for _, tc := range []struct {
		status metav1.ConditionStatus
		want   time.Duration
	}{
		{metav1.ConditionFalse, requeueInterval},
		{metav1.ConditionTrue, readyRequeueInterval},
	} {
		server := serverIn("a", "s1")
		orig := server.DeepCopy()
		server.Status.Conditions = []metav1.Condition{{
			Type: conditionReady, Status: tc.status, Reason: "Test", LastTransitionTime: metav1.Now(),
		}}
		c := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
			WithStatusSubresource(&cnmsv1alpha1.MailServer{}).WithObjects(server).Build()
		r := &MailServerReconciler{Client: c}
		res, err := r.finish(context.Background(), server, orig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.RequeueAfter != tc.want {
			t.Errorf("Ready=%s requeues after %v, want %v", tc.status, res.RequeueAfter, tc.want)
		}
	}
}

// startManagerG runs a manager with the given controllers for the
// current spec: the watches, field indexes and map functions are then
// exercised end to end, without calling Reconcile by hand.
func startManagerG(setups ...func(ctrl.Manager) error) ctrl.Manager {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Cache:   CacheOptions(),
		Client:  ClientOptions(),
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Every spec starts its own manager with the same
		// controller names.
		Controller: config.Controller{SkipNameValidation: new(true)},
	})
	Expect(err).ToNot(HaveOccurred())
	Expect(IndexFields(mgr)).To(Succeed())
	for _, setup := range setups {
		Expect(setup(mgr)).To(Succeed())
	}
	mgrCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		Expect(mgr.Start(mgrCtx)).To(Succeed())
	}()
	DeferCleanup(func() {
		stop()
		Eventually(done, "30s").Should(BeClosed())
	})
	return mgr
}

var _ = Describe("Controller watches", func() {
	const wait, poll = "20s", "200ms"

	authorized := func(ns string) func() (string, error) {
		return func() (string, error) {
			status, reason, _ := serverConditionG(ns, testServerName, ConditionDomainsAuthorized)
			return string(status) + "/" + reason, nil
		}
	}

	It("re-evaluates a server when another namespace claims its domain", func() {
		ns, ns2, domain := uniqueNamespace(), uniqueNamespace(), uniqueDomain()
		createNamespaceG(ns)
		createNamespaceG(ns2)
		createDirectoryG(ns)
		createDomainG(ns, domain, testServerName)
		createServerG(ns, testServerName, nil)
		startManagerG(mailServerSetup(AuthorizationUnrestricted))
		Eventually(authorized(ns), wait, poll).
			Should(Equal("True/" + ReasonAuthorizationDisabled))

		// The competing claim sits in a namespace without any server.
		// The not-ready server's 30s re-check is far outside the wait,
		// so only the cross-namespace watch can flip the condition.
		createDomainG(ns2, domain, testServerName)
		Eventually(authorized(ns), wait, poll).
			Should(Equal("False/" + ReasonDuplicateDomain))
	})

	It("re-evaluates both servers when a domain moves", func() {
		ns := uniqueNamespace()
		createNamespaceG(ns)
		createDirectoryG(ns)
		domainA, domainB := uniqueDomain(), uniqueDomain()
		createDomainG(ns, domainA, testServerName)
		createDomainG(ns, domainB, testServerName)
		createDomainG(ns, uniqueDomain(), otherServer) // so that "other" has a domain
		createServerG(ns, testServerName, nil)
		createServerG(ns, otherServer, nil)
		startManagerG(mailServerSetup(AuthorizationUnrestricted))
		served := func(server string) func() ([]string, error) {
			return func() ([]string, error) {
				s := &cnmsv1alpha1.MailServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server, Namespace: ns}, s); err != nil {
					return nil, err
				}
				var names []string
				for _, d := range s.Status.Domains {
					if d.Served {
						names = append(names, d.Domain)
					}
				}
				return names, nil
			}
		}
		Eventually(served(testServerName), wait, poll).Should(ConsistOf(domainA, domainB))

		// Both servers are not ready and re-check every 30s, so only the
		// watch on the old and the new object can update them in time.
		md := &cnmsv1alpha1.MailDomain{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: domainLabel(domainB), Namespace: ns}, md)).To(Succeed())
		md.Spec.ServerRef.Name = otherServer
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
		Eventually(served(testServerName), wait, poll).Should(ConsistOf(domainA))
		Eventually(served(otherServer), wait, poll).Should(ContainElement(domainB))

		Expect(k8sClient.Delete(ctx, md)).To(Succeed())
		Eventually(served(otherServer), wait, poll).ShouldNot(ContainElement(domainB))
	})

	It("re-evaluates a server when its grant is deleted", func() {
		ns, domain := grantFixture()
		createServerG(ns, testServerName, nil)
		startManagerG(mailServerSetup(AuthorizationAllowlist))
		Eventually(authorized(ns), wait, poll).
			Should(Equal("True/" + ReasonAuthorized))

		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{
			ObjectMeta: metav1.ObjectMeta{Name: domain},
		})).To(Succeed())
		Eventually(authorized(ns), wait, poll).
			Should(Equal("False/" + ReasonNotGranted))
	})

	It("re-checks a grant when its namespace appears", func() {
		ns, domain := uniqueNamespace(), uniqueDomain()
		createGrantG(domain, ns, false)
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domain}})
		})
		startManagerG(func(m ctrl.Manager) error {
			return (&MailDomainGrantReconciler{Client: m.GetClient(), Scheme: m.GetScheme()}).SetupWithManager(m)
		})
		grantCondition := func() (string, error) {
			g := &cnmsv1alpha1.MailDomainGrant{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: domain}, g); err != nil {
				return "", err
			}
			for _, c := range g.Status.Conditions {
				if c.Type == ConditionNamespaceExists {
					return string(c.Status), nil
				}
			}
			return "", nil
		}
		Eventually(grantCondition, wait, poll).Should(Equal(string(metav1.ConditionFalse)))

		createNamespaceG(ns)
		Eventually(grantCondition, wait, poll).Should(Equal(string(metav1.ConditionTrue)))
	})
})

// mailServerSetup wires a MailServerReconciler on the manager's cached
// client, like cmd/main.go does.
func mailServerSetup(mode domainAuthorizationMode) func(ctrl.Manager) error {
	return func(m ctrl.Manager) error {
		return (&MailServerReconciler{
			Client: m.GetClient(), Scheme: m.GetScheme(), APIReader: m.GetAPIReader(),
			DomainAuthorization: mode,
		}).SetupWithManager(m)
	}
}
