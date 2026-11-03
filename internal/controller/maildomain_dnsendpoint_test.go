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
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// dnsEndpointCRDsInstalled reports whether the suite installed the
// DNSEndpoint CRD (DNSENDPOINT_CRDS=1).
func dnsEndpointCRDsInstalled() bool { return os.Getenv("DNSENDPOINT_CRDS") == "1" }

func newEndpointG(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(dnsEndpointGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// getEndpointG returns the DNSEndpoint of a MailDomain, or nil.
func getEndpointG(ns, mailDomain string) *unstructured.Unstructured {
	u := newEndpointG(ns, mailDomain+"-dns")
	err := k8sClient.Get(ctx, types.NamespacedName{Name: u.GetName(), Namespace: ns}, u)
	if apierrors.IsNotFound(err) {
		return nil
	}
	Expect(err).ToNot(HaveOccurred())
	return u
}

func externalDNS(d *cnmsv1alpha1.MailDomain) { d.Spec.DNSMode = new("externalDNS") }

// crdFailingReader fails the CRD lookups only; the Secret reads of the
// reconciler pass through.
type crdFailingReader struct{ client.Reader }

func (r crdFailingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
		return errors.New("the API server is not reachable")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

var _ = Describe("MailDomain DNSEndpoint", func() {
	var ns, domain, name string
	var env *domainTestEnv
	BeforeEach(func() {
		ns = uniqueNamespace()
		domain = uniqueDomain()
		name = domainLabel(domain)
		createNamespaceG(ns)
		env = newDomainEnvG(AuthorizationUnrestricted)
	})

	Context("when the CRD lookup fails", func() {
		BeforeEach(func() { env.r.APIReader = crdFailingReader{k8sClient} })

		It("does not report DNSPublished in manual mode", func() {
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)

			status, _, _ := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(BeEmpty())
			_, reason, _ := domainCondG(ns, name, conditionReady)
			Expect(reason).ToNot(Equal(ReasonCRDCheckFailed))
		})

		It("reports CRDCheckFailed in externalDNS mode", func() {
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)

			status, reason, _ := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonCRDCheckFailed))
		})
	})

	Context("without the DNSEndpoint CRD", func() {
		BeforeEach(func() {
			if dnsEndpointCRDsInstalled() {
				Skip("the DNSEndpoint CRD is installed")
			}
		})

		It("lists the records in the status and reports the missing CRD", func() {
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)

			Expect(getMailDomainG(ns, name).Status.Records).To(HaveLen(1))
			status, reason, _ := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonExternalDNSNotInstalled))
			status, reason, _ = domainCondG(ns, name, conditionReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonExternalDNSNotInstalled))
		})

		It("looks for the CRD again later", func() {
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)
			env.publish(getMailDomainG(ns, name))
			env.clock.Advance(time.Minute)
			res := env.reconcile(ns, name)
			Expect(res.RequeueAfter).To(Equal(crdCacheTTL))
		})

		It("needs no CRD in manual mode", func() {
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)
			status, _, _ := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(BeEmpty())
		})
	})

	Context("with the DNSEndpoint CRD", func() {
		BeforeEach(func() {
			if !dnsEndpointCRDsInstalled() {
				Skip("needs the DNSEndpoint CRD (DNSENDPOINT_CRDS=1)")
			}
		})

		It("publishes every record as a TXT endpoint owned by the MailDomain", func() {
			createMailDomainG(ns, domain, func(d *cnmsv1alpha1.MailDomain) {
				externalDNS(d)
				d.Spec.DMARC = &cnmsv1alpha1.DMARC{Policy: new("quarantine")}
			})
			env.settle(ns, name)

			md := getMailDomainG(ns, name)
			ep := getEndpointG(ns, name)
			Expect(ep).ToNot(BeNil())
			Expect(metav1.IsControlledBy(ep, md)).To(BeTrue())
			endpoints, _, err := unstructured.NestedSlice(ep.Object, "spec", "endpoints")
			Expect(err).ToNot(HaveOccurred())
			Expect(endpoints).To(HaveLen(len(md.Status.Records)))
			Expect(md.Status.Records).To(HaveLen(2))
			for i, rec := range md.Status.Records {
				e := endpoints[i].(map[string]any)
				Expect(e).To(HaveKeyWithValue("dnsName", rec.Name))
				Expect(e).To(HaveKeyWithValue("recordType", "TXT"))
				Expect(e).To(HaveKeyWithValue("recordTTL", int64(3600)))
				Expect(e["targets"]).To(Equal([]any{rec.Value}))
			}
			status, reason, _ := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonDNSEndpointApplied))
		})

		It("writes the DNSEndpoint only when the records change", func() {
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)
			version := getEndpointG(ns, name).GetResourceVersion()
			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)
			Expect(getEndpointG(ns, name).GetResourceVersion()).To(Equal(version))

			d := getMailDomainG(ns, name)
			d.Spec.DMARC = &cnmsv1alpha1.DMARC{Policy: new("reject")}
			Expect(k8sClient.Update(ctx, d)).To(Succeed())
			env.reconcile(ns, name)
			endpoints, _, _ := unstructured.NestedSlice(getEndpointG(ns, name).Object, "spec", "endpoints")
			Expect(endpoints).To(HaveLen(2))
		})

		It("never overwrites a DNSEndpoint it does not control", func() {
			foreign := newEndpointG(ns, name+"-dns")
			Expect(unstructured.SetNestedSlice(foreign.Object, []any{map[string]any{
				"dnsName": "other." + domain, "recordType": "TXT", "targets": []any{"mine"},
			}}, "spec", "endpoints")).To(Succeed())
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)

			status, reason, message := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonOwnershipConflict))
			Expect(message).To(ContainSubstring("not controlled by this MailDomain"))
			got := getEndpointG(ns, name)
			Expect(got.GetResourceVersion()).To(Equal(foreign.GetResourceVersion()))
			Expect(getMailDomainG(ns, name).Status.Records).To(HaveLen(1)) // still listed
		})

		It("removes its DNSEndpoint when the mode becomes manual but leaves a foreign one", func() {
			createMailDomainG(ns, domain, externalDNS)
			env.settle(ns, name)
			Expect(getEndpointG(ns, name)).ToNot(BeNil())

			d := getMailDomainG(ns, name)
			d.Spec.DNSMode = new("manual")
			Expect(k8sClient.Update(ctx, d)).To(Succeed())
			env.reconcile(ns, name)
			Expect(getEndpointG(ns, name)).To(BeNil())
			status, _, _ := domainCondG(ns, name, ConditionDNSPublished)
			Expect(status).To(BeEmpty())

			foreign := newEndpointG(ns, name+"-dns")
			Expect(unstructured.SetNestedSlice(foreign.Object, []any{}, "spec", "endpoints")).To(Succeed())
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			env.reconcile(ns, name)
			Expect(getEndpointG(ns, name)).ToNot(BeNil())
		})

		Describe("revocation", func() {
			BeforeEach(func() {
				env = newDomainEnvG(AuthorizationAllowlist)
				createGrantG(domain, ns, false)
				createMailDomainG(ns, domain, externalDNS)
				env.settle(ns, name)
				Expect(getEndpointG(ns, name)).ToNot(BeNil())
				Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domain}})).To(Succeed())
			})

			It("removes the DNSEndpoint after the grace period and keeps the keys", func() {
				res := env.reconcile(ns, name)
				status, _, _ := domainCondG(ns, name, ConditionAuthorized)
				Expect(status).To(Equal(metav1.ConditionFalse))
				Expect(getEndpointG(ns, name)).ToNot(BeNil())
				status, reason, _ := domainCondG(ns, name, ConditionDNSPublished)
				Expect(status).To(Equal(metav1.ConditionFalse))
				Expect(reason).To(Equal(ReasonAuthorizationRevoked))
				Expect(res.RequeueAfter).To(Equal(readyRequeueInterval)) // the safety net comes before the 15 minutes

				env.clock.Advance(revocationGrace - time.Minute)
				res = env.reconcile(ns, name)
				Expect(getEndpointG(ns, name)).ToNot(BeNil())
				Expect(res.RequeueAfter).To(Equal(time.Minute))

				env.clock.Advance(time.Minute)
				env.reconcile(ns, name)
				Expect(getEndpointG(ns, name)).To(BeNil())
				status, _, _ = domainCondG(ns, name, ConditionDNSPublished)
				Expect(status).To(BeEmpty())
				Expect(keySecretsG(ns, name)).To(HaveLen(1))
			})

			It("keeps the DNSEndpoint when the grant returns within the grace period", func() {
				env.reconcile(ns, name)
				env.clock.Advance(revocationGrace - time.Minute)
				createGrantG(domain, ns, false)
				env.reconcile(ns, name)
				env.clock.Advance(time.Hour)
				env.reconcile(ns, name)

				Expect(getEndpointG(ns, name)).ToNot(BeNil())
				status, _, _ := domainCondG(ns, name, ConditionAuthorized)
				Expect(status).To(Equal(metav1.ConditionTrue))
				status, reason, _ := domainCondG(ns, name, ConditionDNSPublished)
				Expect(status).To(Equal(metav1.ConditionTrue))
				Expect(reason).To(Equal(ReasonDNSEndpointApplied))
			})

			It("restarts the grace period when the status was lost", func() {
				env.reconcile(ns, name)
				env.clock.Advance(revocationGrace + time.Minute)
				// A restore brings back the spec without status; the timer starts again.
				d := getMailDomainG(ns, name)
				d.Status = cnmsv1alpha1.MailDomainStatus{}
				Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
				env.restart()
				env.reconcile(ns, name)
				Expect(getEndpointG(ns, name)).ToNot(BeNil())
				env.clock.Advance(revocationGrace)
				env.reconcile(ns, name)
				Expect(getEndpointG(ns, name)).To(BeNil())
			})
		})
	})
})
