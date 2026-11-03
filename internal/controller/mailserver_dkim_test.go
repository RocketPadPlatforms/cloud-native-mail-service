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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// rspamdObjectName is the Rspamd Deployment of the test server; byoSecret
// and the byoKey* names are the Secret and data keys of own-key specs.
const (
	rspamdObjectName = testServerName + "-rspamd"
	byoSecret        = "byo"
	byoKeyS1         = "s1.key"
	byoKeyS2         = "s2.key"
)

func rspamdDeploymentG(ns string) *appsv1.Deployment {
	dep := &appsv1.Deployment{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rspamdObjectName, Namespace: ns}, dep)).To(Succeed())
	return dep
}

// rspamdConfigG returns the Rspamd configuration the Deployment
// currently references.
func rspamdConfigG(ns string) map[string]string {
	for _, v := range rspamdDeploymentG(ns).Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil {
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: v.ConfigMap.Name, Namespace: ns}, cm)).To(Succeed())
			return cm.Data
		}
	}
	Fail("the Rspamd Deployment references no ConfigMap")
	return nil
}

// mountedKeysG lists the projected key items of the Rspamd pods as
// "<secret>/<key> -> <path>", and whether the volume exists at all.
func mountedKeysG(ns string) []string {
	var out []string
	for _, v := range rspamdDeploymentG(ns).Spec.Template.Spec.Volumes {
		if v.Projected == nil {
			continue
		}
		for _, s := range v.Projected.Sources {
			Expect(s.Secret.Optional).To(HaveValue(BeTrue()))
			for _, it := range s.Secret.Items {
				out = append(out, s.Secret.Name+"/"+it.Key+" -> "+it.Path)
			}
		}
	}
	return out
}

func byoKey(selector, key string, signing bool) cnmsv1alpha1.DKIMKey {
	return cnmsv1alpha1.DKIMKey{
		Selector:  selector,
		SecretRef: cnmsv1alpha1.SecretKeyReference{Name: byoSecret, Key: key},
		Signing:   new(signing),
	}
}

var _ = Describe("MailServer DKIM keys", func() {
	var ns, domain, name string
	var r *MailServerReconciler
	var env *domainTestEnv
	BeforeEach(func() {
		ns, domain = grantFixture()
		name = domainLabel(domain)
		r = newReconcilerG(AuthorizationAllowlist, true)
		env = newDomainEnvG(AuthorizationAllowlist)
		createServerG(ns, testServerName, domain, nil)
	})

	// useOwnKeys switches the MailDomain to bring-your-own keys.
	useOwnKeys := func(keys ...cnmsv1alpha1.DKIMKey) {
		md := getMailDomainG(ns, name)
		md.Spec.DKIM = &cnmsv1alpha1.DKIM{Keys: keys}
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
	}

	It("signs with the generated key, guards it and mounts it", func() {
		env.reconcile(ns, name)
		selector := dkim.Selector("", dkim.RSA, env.clock.Now())
		deployG(r, ns, testServerName)

		cfg := rspamdConfigG(ns)
		Expect(cfg["dkim_signing.conf"]).To(ContainSubstring(
			`{ path = "/etc/rspamd/dkim/` + domain + `/` + selector + `.key"; selector = "` + selector + `"; }`))
		Expect(cfg[render.RspamdLocalLua]).To(ContainSubstring(`["` + domain + `"] = { "` + domain + `:s=` + selector + `" }`))
		Expect(mountedKeysG(ns)).To(Equal([]string{
			dkim.SecretName(name, selector) + "/private.key -> " + domain + "/" + selector + ".key"}))
		status, reason, _ := serverConditionG(ns, testServerName, ConditionDKIMKeysAvailable)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonDKIMKeysAvailable))

		// The key bytes never reach a ConfigMap, nor a pod template.
		cms := &corev1.ConfigMapList{}
		Expect(k8sClient.List(ctx, cms, client.InNamespace(ns))).To(Succeed())
		for _, cm := range cms.Items {
			for f, content := range cm.Data {
				Expect(content).ToNot(ContainSubstring("PRIVATE KEY"), cm.Name+"/"+f)
			}
		}
	})

	It("signs with both algorithms and lists both selectors", func() {
		md := getMailDomainG(ns, name)
		md.Spec.DKIM = &cnmsv1alpha1.DKIM{Ed25519: new(true)}
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
		env.reconcile(ns, name)
		deployG(r, ns, testServerName)

		rsaSel := dkim.Selector("", dkim.RSA, env.clock.Now())
		edSel := dkim.Selector("", dkim.Ed25519, env.clock.Now())
		Expect(rspamdConfigG(ns)[render.RspamdLocalLua]).To(ContainSubstring(
			`{ "` + domain + `:s=` + rsaSel + `", "` + domain + `:s=` + edSel + `" }`))
		Expect(mountedKeysG(ns)).To(HaveLen(2))
	})

	It("uses bring-your-own keys that sign and leaves the others out", func() {
		createSecretG(ns, byoSecret, map[string][]byte{byoKeyS1: pemKeyG(dkim.RSA), byoKeyS2: pemKeyG(dkim.Ed25519)})
		useOwnKeys(byoKey("s1", byoKeyS1, true), byoKey("s2", byoKeyS2, false))
		deployG(r, ns, testServerName)

		Expect(mountedKeysG(ns)).To(Equal([]string{"byo/s1.key -> " + domain + "/s1.key"}))
		cfg := rspamdConfigG(ns)
		Expect(cfg["dkim_signing.conf"]).To(ContainSubstring(`selector = "s1"`))
		Expect(cfg["dkim_signing.conf"]).ToNot(ContainSubstring("s2"))
		Expect(cfg[render.RspamdLocalLua]).ToNot(ContainSubstring("s2"))
		status, _, _ := serverConditionG(ns, testServerName, ConditionDKIMKeysAvailable)
		Expect(status).To(Equal(metav1.ConditionTrue))
	})

	Describe("a domain without a usable key", func() {
		unusable := []struct {
			title   string
			prepare func()
			message string
		}{
			{"has no key at all yet", func() {}, "no active signing key yet"},
			{"has a key Secret that is missing", func() {
				useOwnKeys(byoKey("s1", byoKeyS1, true))
			}, `Secret "byo" not found`},
			{"has a Secret without the key", func() {
				createSecretG(ns, byoSecret, map[string][]byte{"other": []byte("x")})
				useOwnKeys(byoKey("s1", byoKeyS1, true))
			}, `has no key "s1.key"`},
			{"has a key that does not parse", func() {
				createSecretG(ns, byoSecret, map[string][]byte{byoKeyS1: []byte("not a key")})
				useOwnKeys(byoKey("s1", byoKeyS1, true))
			}, "is not usable"},
			{"has an RSA key below the size floor", func() {
				createSecretG(ns, byoSecret, map[string][]byte{byoKeyS1: shortRSAKeyG()})
				useOwnKeys(byoKey("s1", byoKeyS1, true))
			}, "is not usable"},
		}
		for _, tc := range unusable {
			It(tc.title+" is still served and fails closed", func() {
				tc.prepare()
				res := reconcileServerG(r, ns, testServerName)
				// The rollout does not wait for the key.
				finishJobsG(ns, testServerName, true)
				reconcileServerG(r, ns, testServerName)
				getDeploymentG(ns, rspamdObjectName)

				status, reason, message := serverConditionG(ns, testServerName, ConditionDKIMKeysAvailable)
				Expect(status).To(Equal(metav1.ConditionFalse))
				Expect(reason).To(Equal(ReasonDKIMKeysMissing))
				Expect(message).To(ContainSubstring(domain))
				Expect(message).To(ContainSubstring(tc.message))
				Expect(message).ToNot(ContainSubstring("PRIVATE KEY"))
				// Never part of Ready: the server is configured, not blocked.
				_, readyReason, _ := serverConditionG(ns, testServerName, conditionReady)
				Expect(readyReason).ToNot(Equal(ReasonDKIMKeysMissing))
				status, _, _ = serverConditionG(ns, testServerName, ConditionConfigValidated)
				Expect(status).To(Equal(metav1.ConditionTrue))

				// Nothing is mounted for it. A key that exists but cannot
				// be used still has its selector in the guard, so mail of
				// the domain is deferred, not sent unsigned.
				Expect(mountedKeysG(ns)).To(BeEmpty())
				guard := rspamdConfigG(ns)[render.RspamdLocalLua]
				if tc.title == "has no key at all yet" {
					Expect(guard).To(ContainSubstring(`["` + domain + `"] = {}`))
				} else {
					Expect(guard).To(ContainSubstring(`["` + domain + `"] = { "` + domain + `:s=s1" }`))
				}
				Expect(res.RequeueAfter).To(BeNumerically("<=", dkimKeyRetry))
			})
		}

		It("refuses a key Secret that belongs to another MailDomain", func() {
			// The rotation state names a selector whose derived Secret name
			// is taken by another MailDomain's key: it must not be mounted.
			selector := dkim.Selector("", dkim.RSA, env.clock.Now())
			secretName := dkim.SecretName(name, selector)
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: secretName, Namespace: ns,
					Labels: map[string]string{labelMailDomain: "foreign"},
				},
				Data: map[string][]byte{keySecretDataKey: pemKeyG(dkim.RSA)},
			})).To(Succeed())
			md := getMailDomainG(ns, name)
			md.Annotations = map[string]string{dkim.AnnotationRotation: dkim.Encode(dkim.State{Active: []string{selector}})}
			Expect(k8sClient.Update(ctx, md)).To(Succeed())

			reconcileServerG(r, ns, testServerName)
			finishJobsG(ns, testServerName, true)
			reconcileServerG(r, ns, testServerName)
			getDeploymentG(ns, rspamdObjectName)

			status, reason, message := serverConditionG(ns, testServerName, ConditionDKIMKeysAvailable)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonDKIMKeysMissing))
			Expect(message).To(ContainSubstring(`Secret "` + secretName + `" belongs to MailDomain "foreign"`))
			Expect(mountedKeysG(ns)).To(BeEmpty())
			// The selector stays in the guard, so mail of the domain is deferred.
			Expect(rspamdConfigG(ns)[render.RspamdLocalLua]).To(ContainSubstring(
				`["` + domain + `"] = { "` + domain + `:s=` + selector + `" }`))
		})

		It("picks the key up without a validation Job once it can be read", func() {
			useOwnKeys(byoKey("s1", byoKeyS1, true))
			deployG(r, ns, testServerName)
			hash := podTemplateHash(getStatefulSetG(ns, testServerName+"-postfix"))
			rspamdHash := rspamdDeploymentG(ns).Spec.Template.Annotations["cnms.rpp.gmbh/config-hash"]
			jobs := len(jobsG(ns, testServerName, "rspamd"))
			Expect(mountedKeysG(ns)).To(BeEmpty())

			createSecretG(ns, byoSecret, map[string][]byte{byoKeyS1: pemKeyG(dkim.RSA)})
			reconcileServerG(r, ns, testServerName)

			Expect(mountedKeysG(ns)).To(Equal([]string{"byo/s1.key -> " + domain + "/s1.key"}))
			status, _, _ := serverConditionG(ns, testServerName, ConditionDKIMKeysAvailable)
			Expect(status).To(Equal(metav1.ConditionTrue))
			// The configuration is the same, so it is not validated again.
			Expect(rspamdDeploymentG(ns).Spec.Template.Annotations["cnms.rpp.gmbh/config-hash"]).To(Equal(rspamdHash))
			Expect(podTemplateHash(getStatefulSetG(ns, testServerName+"-postfix"))).To(Equal(hash))
			Expect(jobsG(ns, testServerName, "rspamd")).To(HaveLen(jobs))
		})
	})

	It("takes a revoked domain out of the signer, the guard and the pods", func() {
		other := uniqueDomain()
		createDomainG(ns, other)
		createGrantG(other, ns, false)
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Domains = append(s.Spec.Domains, cnmsv1alpha1.NameReference{Name: domainLabel(other)})
		})
		env.reconcile(ns, name)
		env.reconcile(ns, domainLabel(other))
		deployG(r, ns, testServerName)
		Expect(rspamdConfigG(ns)[render.RspamdLocalLua]).To(ContainSubstring(other + ":s="))
		Expect(mountedKeysG(ns)).To(HaveLen(2))

		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: other}})).To(Succeed())
		deployG(r, ns, testServerName)

		cfg := rspamdConfigG(ns)
		for _, f := range []string{"dkim_signing.conf", render.RspamdLocalLua} {
			Expect(cfg[f]).ToNot(ContainSubstring(other), f)
			Expect(cfg[f]).To(ContainSubstring(domain), f)
		}
		Expect(mountedKeysG(ns)).To(HaveLen(1))
		// The key Secrets stay (ADR 0003).
		Expect(keySecretsG(ns, domainLabel(other))).To(HaveLen(1))
	})

	It("rolls the signing selector through the validation gate on a rotation", func() {
		md := getMailDomainG(ns, name)
		md.Spec.DKIM = &cnmsv1alpha1.DKIM{Rotation: &cnmsv1alpha1.DKIMRotation{
			Interval: metav1.Duration{Duration: 24 * time.Hour},
			Overlap:  &metav1.Duration{Duration: time.Hour},
		}}
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
		first := dkim.Selector("", dkim.RSA, env.clock.Now())
		env.settle(ns, name)
		env.publish(getMailDomainG(ns, name))
		env.clock.Advance(time.Minute)
		env.reconcile(ns, name)
		deployG(r, ns, testServerName)
		convergeWorkloadsG(ns)
		reconcileServerG(r, ns, testServerName)
		oldHash := rspamdDeploymentG(ns).Spec.Template.Annotations["cnms.rpp.gmbh/config-hash"]
		Expect(mountedKeysG(ns)).To(HaveLen(1))

		// The next key is created and published, and the old one keeps signing:
		// nothing changes in the data plane.
		env.clock.Advance(25 * time.Hour)
		env.settle(ns, name)
		second := dkim.Selector("", dkim.RSA, env.clock.Now())
		reconcileServerG(r, ns, testServerName)
		Expect(jobsG(ns, testServerName, "rspamd")).To(HaveLen(1))
		Expect(rspamdDeploymentG(ns).Spec.Template.Annotations["cnms.rpp.gmbh/config-hash"]).To(Equal(oldHash))

		// The record is verified for the hold period: the new key signs.
		env.publish(getMailDomainG(ns, name))
		env.clock.Advance(dnsrecords.UnverifiedMin)
		env.reconcile(ns, name)
		env.clock.Advance(dkim.HoldPeriod - time.Minute)
		env.reconcile(ns, name)
		env.clock.Advance(2 * time.Minute)
		env.reconcile(ns, name)
		Expect(selectorsOf(getMailDomainG(ns, name).Status.DKIM.Active)).To(Equal([]string{second}))

		// The new selector set has another hash: the old pods keep the old
		// configuration until the validation Jobs pass.
		reconcileServerG(r, ns, testServerName)
		Expect(jobsG(ns, testServerName, "rspamd")).To(HaveLen(2))
		Expect(rspamdDeploymentG(ns).Spec.Template.Annotations["cnms.rpp.gmbh/config-hash"]).To(Equal(oldHash))
		Expect(rspamdConfigG(ns)[render.RspamdLocalLua]).To(ContainSubstring(":s=" + first))
		status, reason, _ := serverConditionG(ns, testServerName, ConditionConfigValidated)
		Expect(status).To(Equal(metav1.ConditionUnknown))
		Expect(reason).To(Equal(ReasonValidatingConfig))

		finishJobsG(ns, testServerName, true)
		reconcileServerG(r, ns, testServerName)
		Expect(rspamdDeploymentG(ns).Spec.Template.Annotations["cnms.rpp.gmbh/config-hash"]).ToNot(Equal(oldHash))
		cfg := rspamdConfigG(ns)
		Expect(cfg[render.RspamdLocalLua]).To(ContainSubstring(":s=" + second))
		Expect(cfg[render.RspamdLocalLua]).ToNot(ContainSubstring(":s=" + first))
		Expect(mountedKeysG(ns)).To(Equal([]string{
			dkim.SecretName(name, second) + "/private.key -> " + domain + "/" + second + ".key"}))
	})
})

func TestActiveKeyRefs(t *testing.T) {
	md := &cnmsv1alpha1.MailDomain{ObjectMeta: metav1.ObjectMeta{Name: "ex"}}
	if got := activeKeyRefs(md); len(got) != 0 {
		t.Errorf("a MailDomain without state has keys %v", got)
	}

	md.Annotations = map[string]string{dkim.AnnotationRotation: dkim.Encode(dkim.State{
		Active:   []string{"rsa-sel", "ed-sel"},
		Retiring: []dkim.RetiringKey{{Selector: "old", Until: time.Now()}},
	})}
	got := activeKeyRefs(md)
	want := []keyRef{ // the state is sorted
		{"ed-sel", "ex-dkim-ed-sel", "private.key"},
		{"rsa-sel", "ex-dkim-rsa-sel", "private.key"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("generated keys = %v, want %v", got, want)
	}

	md.Annotations[dkim.AnnotationRotation] = "not json"
	if got := activeKeyRefs(md); len(got) != 0 {
		t.Errorf("an unusable annotation gives keys %v", got)
	}

	// Own keys come from the spec, the annotation is ignored, and only
	// keys that sign count.
	md.Spec.DKIM = &cnmsv1alpha1.DKIM{Keys: []cnmsv1alpha1.DKIMKey{
		{Selector: "a", SecretRef: cnmsv1alpha1.SecretKeyReference{Name: "s", Key: "k1.key"}},
		{Selector: "b", SecretRef: cnmsv1alpha1.SecretKeyReference{Name: "s", Key: "k2.key"}, Signing: new(false)},
	}}
	if got := activeKeyRefs(md); len(got) != 1 || got[0] != (keyRef{"a", "s", "k1.key"}) {
		t.Errorf("own keys = %v", got)
	}
}
