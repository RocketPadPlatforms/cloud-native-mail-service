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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
)

// keySecretsG lists the key Secrets of a MailDomain, sorted by name.
func keySecretsG(ns, mailDomain string) []corev1.Secret {
	list := &corev1.SecretList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns),
		client.MatchingLabels{labelMailDomain: mailDomain})).To(Succeed())
	return list.Items
}

// drainEvents returns the events the fake recorder has received so far.
func drainEvents(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func secretNames(secrets []corev1.Secret) []string {
	names := make([]string, 0, len(secrets))
	for _, s := range secrets {
		names = append(names, s.Name)
	}
	return names
}

// pemKeyG returns a generated private key as PKCS#8 PEM.
func pemKeyG(alg dkim.Algorithm) []byte {
	pemKey, err := dkim.Generate(alg, rand.Reader)
	Expect(err).ToNot(HaveOccurred())
	return pemKey
}

// shortRSAKeyG returns a 1024-bit RSA key, below the floor of 2048.
func shortRSAKeyG() []byte {
	k, err := rsa.GenerateKey(rand.Reader, 1024)
	Expect(err).ToNot(HaveOccurred())
	der, err := x509.MarshalPKCS8PrivateKey(k)
	Expect(err).ToNot(HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func createSecretG(ns, name string, data map[string][]byte) {
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       data,
	})).To(Succeed())
}

// rotationStateG decodes the rotation annotation of a MailDomain.
func rotationStateG(ns, name string) (dkim.State, bool) {
	st, found, err := dkim.Decode(getMailDomainG(ns, name).Annotations[dkim.AnnotationRotation])
	Expect(err).ToNot(HaveOccurred())
	return st, found
}

func withDKIM(fn func(*cnmsv1alpha1.DKIM)) func(*cnmsv1alpha1.MailDomain) {
	return func(d *cnmsv1alpha1.MailDomain) {
		d.Spec.DKIM = &cnmsv1alpha1.DKIM{}
		fn(d.Spec.DKIM)
	}
}

func selectorsOf(keys []cnmsv1alpha1.DKIMKeyStatus) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Selector)
	}
	return out
}

var _ = Describe("MailDomain DKIM keys", func() {
	var ns, domain, name string
	var env *domainTestEnv
	BeforeEach(func() {
		ns = uniqueNamespace()
		domain = uniqueDomain()
		name = domainLabel(domain)
		createNamespaceG(ns)
		env = newDomainEnvG(AuthorizationUnrestricted)
	})

	Describe("generated keys", func() {
		It("creates an active RSA key and records the state", func() {
			createMailDomainG(ns, domain, nil)
			env.reconcile(ns, name)

			selector := dkim.Selector("", dkim.RSA, env.clock.Now())
			secrets := keySecretsG(ns, name)
			Expect(secrets).To(HaveLen(1))
			s := secrets[0]
			Expect(s.Name).To(Equal(dkim.SecretName(name, selector)))
			Expect(s.Immutable).To(HaveValue(BeTrue()))
			Expect(s.OwnerReferences).To(BeEmpty())
			Expect(s.Annotations).To(HaveKeyWithValue(annotationKeyAlgorithm, "rsa"))
			Expect(s.Annotations).To(HaveKeyWithValue(annotationKeyCreated, env.clock.Now().Format(time.RFC3339)))
			Expect(s.Labels).To(HaveKeyWithValue(labelAppManagedBy, "cnms"))
			key, err := dkim.Parse(s.Data[keySecretDataKey])
			Expect(err).ToNot(HaveOccurred())
			Expect(key.Algorithm).To(Equal(dkim.RSA))
			Expect(key.Bits).To(Equal(2048))

			state, found := rotationStateG(ns, name)
			Expect(found).To(BeTrue())
			Expect(state.Active).To(Equal([]string{selector}))

			d := getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{selector}))
			Expect(d.Status.DKIM.Active[0].Algorithm).To(Equal("rsa"))
			Expect(d.Status.DKIM.Active[0].KeyCreated.Time).To(BeTemporally("==", env.clock.Now()))
			Expect(d.Status.DKIM.Pending).To(BeEmpty())
			status, reason, _ := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonKeysValid))
			status, reason, _ = domainCondG(ns, name, ConditionDKIMRotation)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonNoPendingKey))
		})

		It("adds an Ed25519 key when enabled", func() {
			createMailDomainG(ns, domain, withDKIM(func(d *cnmsv1alpha1.DKIM) { d.Ed25519 = new(true) }))
			env.reconcile(ns, name)

			Expect(secretNames(keySecretsG(ns, name))).To(ConsistOf(
				dkim.SecretName(name, dkim.Selector("", dkim.RSA, env.clock.Now())),
				dkim.SecretName(name, dkim.Selector("", dkim.Ed25519, env.clock.Now())),
			))
			Expect(getMailDomainG(ns, name).Status.DKIM.Active).To(HaveLen(2))
		})

		It("honours the selector prefix", func() {
			createMailDomainG(ns, domain, withDKIM(func(d *cnmsv1alpha1.DKIM) { d.SelectorPrefix = new("mail") }))
			env.reconcile(ns, name)

			Expect(secretNames(keySecretsG(ns, name))).To(ConsistOf(
				dkim.SecretName(name, dkim.Selector("mail", dkim.RSA, env.clock.Now()))))
		})

		It("writes nothing when nothing changed", func() {
			createMailDomainG(ns, domain, nil)
			env.settle(ns, name)
			version := getMailDomainG(ns, name).ResourceVersion
			secrets := keySecretsG(ns, name)

			env.clock.Advance(time.Minute)
			env.reconcile(ns, name)
			Expect(getMailDomainG(ns, name).ResourceVersion).To(Equal(version))
			Expect(keySecretsG(ns, name)).To(Equal(secrets))
		})

		It("starts a rotation when the active key is older than the interval", func() {
			createMailDomainG(ns, domain, withDKIM(func(d *cnmsv1alpha1.DKIM) {
				d.Rotation = &cnmsv1alpha1.DKIMRotation{
					Interval: metav1.Duration{Duration: 24 * time.Hour},
					Overlap:  &metav1.Duration{Duration: time.Hour},
				}
			}))
			env.reconcile(ns, name)
			first := dkim.Selector("", dkim.RSA, env.clock.Now())
			d := getMailDomainG(ns, name)
			Expect(d.Status.DKIM.NextRotation.Time).To(BeTemporally("==", env.clock.Now().Add(24*time.Hour)))

			env.clock.Advance(25 * time.Hour)
			env.reconcile(ns, name)
			second := dkim.Selector("", dkim.RSA, env.clock.Now())
			Expect(second).ToNot(Equal(first))
			Expect(secretNames(keySecretsG(ns, name))).To(ConsistOf(
				dkim.SecretName(name, first), dkim.SecretName(name, second)))
			d = getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{first}))
			Expect(selectorsOf(d.Status.DKIM.Pending)).To(Equal([]string{second}))
			status, reason, _ := domainCondG(ns, name, ConditionDKIMRotation)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonAwaitingDNS))
		})

		It("keeps the keys and derives the state again after a restore", func() {
			createMailDomainG(ns, domain, withDKIM(func(d *cnmsv1alpha1.DKIM) { d.Ed25519 = new(true) }))
			env.reconcile(ns, name)
			secrets := keySecretsG(ns, name)
			want := getMailDomainG(ns, name)

			// A GitOps restore brings back the spec only: no annotation, no status.
			d := getMailDomainG(ns, name)
			d.Annotations = nil
			Expect(k8sClient.Update(ctx, d)).To(Succeed())
			d = getMailDomainG(ns, name)
			d.Status = cnmsv1alpha1.MailDomainStatus{}
			Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())

			env.clock.Advance(time.Hour)
			env.reconcile(ns, name)
			got := getMailDomainG(ns, name)
			Expect(keySecretsG(ns, name)).To(Equal(secrets))
			Expect(got.Annotations[dkim.AnnotationRotation]).To(Equal(want.Annotations[dkim.AnnotationRotation]))
			Expect(got.Status.DKIM).To(Equal(want.Status.DKIM))
		})

		It("adopts a key Secret that exists and never regenerates it", func() {
			createMailDomainG(ns, domain, nil)
			selector := dkim.Selector("", dkim.RSA, env.clock.Now())
			pemKey := pemKeyG(dkim.RSA)
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: dkim.SecretName(name, selector), Namespace: ns,
					Labels: keySecretLabels(&cnmsv1alpha1.MailDomain{ObjectMeta: metav1.ObjectMeta{Name: name}}),
					Annotations: map[string]string{
						annotationKeyCreated:   env.clock.Now().Add(-48 * time.Hour).Format(time.RFC3339),
						annotationKeyAlgorithm: "rsa",
					},
				},
				Data: map[string][]byte{keySecretDataKey: pemKey},
			})).To(Succeed())

			rec := events.NewFakeRecorder(10)
			env.r.Recorder = rec
			env.reconcile(ns, name)
			secrets := keySecretsG(ns, name)
			Expect(secrets).To(HaveLen(1))
			Expect(secrets[0].Data[keySecretDataKey]).To(Equal(pemKey))
			state, _ := rotationStateG(ns, name)
			Expect(state.Active).To(Equal([]string{selector}))
			Expect(drainEvents(rec)).ToNot(ContainElement(HavePrefix("Normal KeyCreated")))
		})

		It("emits KeyCreated for a key it created", func() {
			createMailDomainG(ns, domain, nil)
			rec := events.NewFakeRecorder(10)
			env.r.Recorder = rec
			env.reconcile(ns, name)
			Expect(drainEvents(rec)).To(ContainElement(HavePrefix("Normal KeyCreated")))
		})

		It("does not adopt a key Secret of another MailDomain", func() {
			createMailDomainG(ns, domain, nil)
			selector := dkim.Selector("", dkim.RSA, env.clock.Now())
			pemKey := pemKeyG(dkim.RSA)
			secretName := dkim.SecretName(name, selector)
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: secretName, Namespace: ns,
					Labels: keySecretLabels(&cnmsv1alpha1.MailDomain{ObjectMeta: metav1.ObjectMeta{Name: "other"}}),
				},
				Data: map[string][]byte{keySecretDataKey: pemKey},
			})).To(Succeed())
			rec := events.NewFakeRecorder(10)
			env.r.Recorder = rec

			env.settle(ns, name)
			status, reason, message := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonKeySecretConflict))
			Expect(message).To(ContainSubstring(`"other"`))
			Expect(keySecretsG(ns, name)).To(BeEmpty())
			s := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: secretName, Namespace: ns}, s)).To(Succeed())
			Expect(s.Data[keySecretDataKey]).To(Equal(pemKey))
			Expect(s.Labels).To(HaveKeyWithValue(labelMailDomain, "other"))
			// The state must not name a Secret that the domain does not own:
			// the data plane derives the Secret from the selector.
			state, _ := rotationStateG(ns, name)
			Expect(state.Active).To(BeEmpty())
			Expect(getMailDomainG(ns, name).Annotations[dkim.AnnotationRotation]).NotTo(ContainSubstring(selector))
			got := drainEvents(rec)
			Expect(got).To(ContainElement(HavePrefix("Warning " + ReasonKeySecretConflict)))
			Expect(got).ToNot(ContainElement(HavePrefix("Normal KeyCreated")))
		})

		It("derives the state again from a corrupt annotation", func() {
			createMailDomainG(ns, domain, nil)
			env.reconcile(ns, name)
			want := getMailDomainG(ns, name).Annotations[dkim.AnnotationRotation]

			d := getMailDomainG(ns, name)
			d.Annotations[dkim.AnnotationRotation] = "{not json"
			Expect(k8sClient.Update(ctx, d)).To(Succeed())
			env.reconcile(ns, name)
			Expect(getMailDomainG(ns, name).Annotations[dkim.AnnotationRotation]).To(Equal(want))
			Expect(keySecretsG(ns, name)).To(HaveLen(1))
		})

		It("reports a key Secret that does not parse", func() {
			createMailDomainG(ns, domain, nil)
			env.reconcile(ns, name)
			s := keySecretsG(ns, name)[0]
			// A Secret is immutable: replace it with broken content under the same name.
			Expect(k8sClient.Delete(ctx, &s)).To(Succeed())
			s.ResourceVersion, s.UID = "", ""
			s.Data = map[string][]byte{keySecretDataKey: []byte("garbage")}
			Expect(k8sClient.Create(ctx, &s)).To(Succeed())

			env.reconcile(ns, name)
			status, reason, message := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonInvalidKey))
			Expect(message).To(ContainSubstring(`key Secret "` + s.Name + `"`))
			Expect(keySecretsG(ns, name)).To(HaveLen(1))
		})

		It("creates no key for an unauthorized domain", func() {
			env = newDomainEnvG(AuthorizationAllowlist)
			createMailDomainG(ns, domain, nil)
			env.reconcile(ns, name)

			Expect(keySecretsG(ns, name)).To(BeEmpty())
			Expect(getMailDomainG(ns, name).Status.DKIM).To(BeNil())
		})

		It("keeps the key Secrets when the domain is revoked", func() {
			env = newDomainEnvG(AuthorizationAllowlist)
			createGrantG(domain, ns, false)
			createMailDomainG(ns, domain, nil)
			env.reconcile(ns, name)
			Expect(keySecretsG(ns, name)).To(HaveLen(1))

			Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domain}})).To(Succeed())
			env.reconcile(ns, name)
			Expect(keySecretsG(ns, name)).To(HaveLen(1))
			d := getMailDomainG(ns, name)
			Expect(d.Status.DKIM).To(BeNil())
			status, _, _ := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(BeEmpty())
		})
	})

	Describe("your own keys", func() {
		own := func(keys ...cnmsv1alpha1.DKIMKey) func(*cnmsv1alpha1.MailDomain) {
			return withDKIM(func(d *cnmsv1alpha1.DKIM) { d.Keys = keys })
		}
		ref := func(selector, secret, key string, signing bool) cnmsv1alpha1.DKIMKey {
			return cnmsv1alpha1.DKIMKey{
				Selector:  selector,
				SecretRef: cnmsv1alpha1.SecretKeyReference{Name: secret, Key: key},
				Signing:   &signing,
			}
		}

		It("publishes valid keys and generates nothing", func() {
			createSecretG(ns, "keys", map[string][]byte{"a.key": pemKeyG(dkim.RSA), "b.key": pemKeyG(dkim.Ed25519)})
			createMailDomainG(ns, domain, own(ref("own-rsa", "keys", "a.key", true), ref("own-ed", "keys", "b.key", false)))
			env.reconcile(ns, name)
			env.publish(getMailDomainG(ns, name))
			env.clock.Advance(time.Minute)
			res := env.reconcile(ns, name)

			d := getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{"own-rsa"}))
			Expect(selectorsOf(d.Status.DKIM.Pending)).To(Equal([]string{"own-ed"}))
			Expect(d.Status.DKIM.Pending[0].Algorithm).To(Equal("ed25519"))
			Expect(d.Status.DKIM.Active[0].KeyCreated).To(BeNil())
			Expect(keySecretsG(ns, name)).To(BeEmpty())
			Expect(d.Annotations).ToNot(HaveKey(dkim.AnnotationRotation))
			status, _, _ := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionTrue))
			_, reason, _ := domainCondG(ns, name, ConditionDKIMRotation)
			Expect(reason).To(Equal(ReasonUserManaged))
			// Secrets are not watched: the keys are read again on a timer.
			Expect(res.RequeueAfter).To(Equal(byoKeyRecheck))
		})

		It("reports each unusable key with its reason and keeps the others", func() {
			createSecretG(ns, "keys", map[string][]byte{
				"good.key": pemKeyG(dkim.RSA), "short.key": shortRSAKeyG(), "junk.key": []byte("junk"),
			})
			createMailDomainG(ns, domain, own(
				ref("good", "keys", "good.key", true),
				ref("short", "keys", "short.key", false),
				ref("junk", "keys", "junk.key", false),
				ref("nokey", "keys", "absent.key", false),
				ref("nosecret", "missing", "k", false),
			))
			res := env.reconcile(ns, name)

			d := getMailDomainG(ns, name)
			Expect(selectorsOf(d.Status.DKIM.Active)).To(Equal([]string{"good"}))
			Expect(d.Status.DKIM.Pending).To(BeEmpty())
			status, reason, message := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonInvalidKey))
			Expect(message).To(ContainSubstring("short: short.key in Secret \"keys\": RSA key has 1024 bits"))
			Expect(message).To(ContainSubstring("junk: junk.key"))
			Expect(message).To(ContainSubstring("nokey: Secret \"keys\" has no key \"absent.key\""))
			Expect(message).To(ContainSubstring("nosecret: Secret \"missing\" not found"))
			// A missing or unusable key is retried soon, without an error loop.
			Expect(res.RequeueAfter).To(Equal(ownKeyRetry))
		})

		It("reports a missing Secret and recovers once it exists", func() {
			createMailDomainG(ns, domain, own(ref("s1", "later", "k", true)))
			env.reconcile(ns, name)
			status, reason, _ := domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonKeySecretNotFound))

			createSecretG(ns, "later", map[string][]byte{"k": pemKeyG(dkim.RSA)})
			env.reconcile(ns, name)
			status, _, _ = domainCondG(ns, name, ConditionDKIMKeysValid)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(selectorsOf(getMailDomainG(ns, name).Status.DKIM.Active)).To(Equal([]string{"s1"}))
		})
	})
})
