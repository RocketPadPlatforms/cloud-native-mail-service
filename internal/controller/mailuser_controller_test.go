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
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

const (
	userServer      = testServerName
	aliceCredential = "alice-credential"
	userSales       = "sales"
	touchedValue    = "yes"
	touchedKey      = "touched"
	userPW1         = "first-password-123"
	userPW2         = "second-password-456"
	userSecret      = "alice-password"
)

// staleReader reads like the API server, except that it shows a Secret as
// it was (a copy), or as missing, so that a lost race can be replayed.
type staleReader struct {
	client.Reader
	name string
	// copy is returned instead of the real Secret; nil means NotFound.
	copy *corev1.Secret
}

func (s staleReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if sec, ok := obj.(*corev1.Secret); ok && key.Name == s.name {
		if s.copy == nil {
			return apierrors.NewNotFound(corev1.Resource("secrets"), key.Name)
		}
		s.copy.DeepCopyInto(sec)
		return nil
	}
	return s.Reader.Get(ctx, key, obj, opts...)
}

var _ = Describe("MailUser Controller", func() {
	var (
		ns         string
		domA, domB string
		refA, refB string
		rec        *events.FakeRecorder
		r          *MailUserReconciler
	)

	BeforeEach(func() {
		ns = uniqueNamespace()
		createNamespaceG(ns)
		createServerG(ns, userServer, func(s *cnmsv1alpha1.MailServer) { s.Spec.DirectoryRef = nil })
		domA, domB = uniqueDomain(), uniqueDomain()
		refA, refB = domainLabel(domA), domainLabel(domB)
		createDomainG(ns, domA, userServer)
		createDomainG(ns, domB, userServer)
		rec = events.NewFakeRecorder(50)
		r = &MailUserReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Recorder: rec,
			DomainAuthorization: AuthorizationUnrestricted,
		}
	})

	newUser := func(name string, mutate func(*cnmsv1alpha1.MailUser)) *cnmsv1alpha1.MailUser {
		u := &cnmsv1alpha1.MailUser{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: cnmsv1alpha1.MailUserSpec{
				ServerRef: cnmsv1alpha1.NameReference{Name: userServer},
				DomainRef: cnmsv1alpha1.NameReference{Name: refA},
			},
		}
		if mutate != nil {
			mutate(u)
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		return u
	}
	reconcileWith := func(rc context.Context, name string) (ctrl.Result, *cnmsv1alpha1.MailUser) {
		res, err := r.Reconcile(rc, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		Expect(err).NotTo(HaveOccurred())
		u := &cnmsv1alpha1.MailUser{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u)).To(Succeed())
		return res, u
	}
	reconcile := func(name string) *cnmsv1alpha1.MailUser {
		_, u := reconcileWith(ctx, name)
		return u
	}
	credential := func(name string) *corev1.Secret {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: credentialSecretName(name)}, s)).To(Succeed())
		return s
	}
	source := func(name, password string) {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string][]byte{keyPassword: []byte(password)},
		})).To(Succeed())
	}
	cond := func(u *cnmsv1alpha1.MailUser, t string) *metav1.Condition {
		return meta.FindStatusCondition(u.Status.Conditions, t)
	}
	withSource := func(u *cnmsv1alpha1.MailUser) {
		u.Spec.PasswordSecretRef = &cnmsv1alpha1.SecretReference{Name: userSecret}
	}
	row := func(u *cnmsv1alpha1.MailUser, address string) cnmsv1alpha1.MailUserAddressStatus {
		for _, a := range u.Status.Addresses {
			if a.Address == address {
				return a
			}
		}
		Fail("no row " + address + " in the status")
		return cnmsv1alpha1.MailUserAddressStatus{}
	}
	verifies := func(password string, hash []byte) bool {
		ok, err := verifyHash(ctx, password, string(hash))
		Expect(err).NotTo(HaveOccurred())
		return ok
	}

	Describe("the credential", func() {
		It("makes a generated password once and keeps it", func() {
			newUser(apiUserName, nil)
			u := reconcile(apiUserName)
			s := credential(apiUserName)

			Expect(metav1.IsControlledBy(s, u)).To(BeTrue(), "the MailUser owns the Secret (no GC in envtest)")
			Expect(s.Labels).To(HaveKeyWithValue(manifests.ManagedByLabel, manifests.ManagedByValue))
			Expect(s.Labels).To(HaveKeyWithValue(manifests.ServerLabel, userServer))
			Expect(s.Annotations).To(HaveKeyWithValue(annotationMailUser, apiUserName))
			Expect(s.Annotations).NotTo(HaveKey(annotationSourceVersion))
			Expect(string(s.Data["username"])).To(Equal(apiUserName))
			Expect(s.Data).To(HaveLen(3))
			Expect(string(s.Data["passwordHash"])).To(HavePrefix("{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$"))
			Expect(s.Data["password"]).To(HaveLen(43))
			Expect(verifies(string(s.Data["password"]), s.Data["passwordHash"])).To(BeTrue())

			Expect(u.Status.CredentialSecretName).To(Equal(aliceCredential))
			Expect(u.Status.CredentialRevision).To(Equal(s.ResourceVersion))
			Expect(cond(u, ConditionCredentialReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionTrue))

			// Again, and again: nothing moves.
			u2 := reconcile(apiUserName)
			reconcile(apiUserName)
			s2 := credential(apiUserName)
			Expect(s2.ResourceVersion).To(Equal(s.ResourceVersion))
			Expect(s2.Data).To(Equal(s.Data))
			Expect(u2.Status.CredentialRevision).To(Equal(u.Status.CredentialRevision))
		})

		It("hashes a provided password, and does not rehash after a write that changes no password", func() {
			source(userSecret, userPW1)
			newUser(apiUserName, withSource)
			u := reconcile(apiUserName)
			s := credential(apiUserName)
			Expect(s.Data).NotTo(HaveKey("password"), "a provided password is not copied")
			Expect(verifies(userPW1, s.Data["passwordHash"])).To(BeTrue())
			Expect(verifies(userPW2, s.Data["passwordHash"])).To(BeFalse())
			src := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userSecret}, src)).To(Succeed())
			Expect(s.Annotations).To(HaveKeyWithValue(annotationSourceVersion, src.ResourceVersion))
			revision := u.Status.CredentialRevision
			Expect(revision).To(Equal(s.ResourceVersion))

			// A label on the source (a GitOps tool, a secrets operator): the
			// resourceVersion moves, the password does not.
			src.Labels = map[string]string{touchedKey: touchedValue}
			Expect(k8sClient.Update(ctx, src)).To(Succeed())
			u = reconcile(apiUserName)
			after := credential(apiUserName)
			Expect(after.Data["passwordHash"]).To(Equal(s.Data["passwordHash"]), "same hash bytes, same salt")
			Expect(after.Annotations).To(HaveKeyWithValue(annotationSourceVersion, src.ResourceVersion))
			Expect(after.ResourceVersion).NotTo(Equal(s.ResourceVersion), "only the annotation was written")
			Expect(u.Status.CredentialRevision).To(Equal(revision), "the revision moves only with the hash")

			// A new password rewrites the hash and moves the revision.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userSecret}, src)).To(Succeed())
			src.Data["password"] = []byte(userPW2)
			Expect(k8sClient.Update(ctx, src)).To(Succeed())
			u = reconcile(apiUserName)
			changed := credential(apiUserName)
			Expect(changed.Data["passwordHash"]).NotTo(Equal(s.Data["passwordHash"]))
			Expect(verifies(userPW2, changed.Data["passwordHash"])).To(BeTrue())
			Expect(verifies(userPW1, changed.Data["passwordHash"])).To(BeFalse())
			Expect(u.Status.CredentialRevision).To(Equal(changed.ResourceVersion))
			Expect(u.Status.CredentialRevision).NotTo(Equal(revision))
		})

		It("polls a provided password and a generated one differently", func() {
			source(userSecret, userPW1)
			newUser(apiUserName, withSource)
			newUser("bob", nil)
			res, _ := reconcileWith(ctx, apiUserName)
			Expect(res.RequeueAfter).To(And(BeNumerically(">=", credentialPoll), BeNumerically("<", credentialPoll*12/10)), "Secrets are not watched; the delay is jittered")
			res, _ = reconcileWith(ctx, "bob")
			Expect(res.RequeueAfter).To(And(BeNumerically(">=", credentialRepair), BeNumerically("<", credentialRepair*12/10)))
		})

		It("switches between a generated and a provided password", func() {
			newUser(apiUserName, nil)
			reconcile(apiUserName)
			generated := credential(apiUserName)
			Expect(generated.Data).To(HaveKey("password"))

			source(userSecret, userPW1)
			u := &cnmsv1alpha1.MailUser{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, u)).To(Succeed())
			withSource(u)
			Expect(k8sClient.Update(ctx, u)).To(Succeed())
			reconcile(apiUserName)
			provided := credential(apiUserName)
			Expect(provided.Data).NotTo(HaveKey("password"), "the generated plaintext is gone")
			Expect(verifies(userPW1, provided.Data["passwordHash"])).To(BeTrue())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, u)).To(Succeed())
			u.Spec.PasswordSecretRef = nil
			Expect(k8sClient.Update(ctx, u)).To(Succeed())
			reconcile(apiUserName)
			again := credential(apiUserName)
			Expect(again.Data["password"]).To(HaveLen(43))
			Expect(again.Annotations).NotTo(HaveKey(annotationSourceVersion))
			Expect(verifies(string(again.Data["password"]), again.Data["passwordHash"])).To(BeTrue())
		})

		It("reports a missing source or key, a short password, and keeps a credential that exists", func() {
			newUser(apiUserName, withSource)
			u := reconcile(apiUserName)
			Expect(cond(u, ConditionCredentialReady).Reason).To(Equal(ReasonSecretNotFound))
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionFalse))
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonSecretNotFound))
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: aliceCredential}, &corev1.Secret{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no credential without a password")
			Expect(u.Status.CredentialSecretName).To(BeEmpty())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: userSecret, Namespace: ns}, Data: map[string][]byte{otherServer: []byte("x")},
			})).To(Succeed())
			u = reconcile(apiUserName)
			Expect(cond(u, ConditionCredentialReady).Reason).To(Equal(ReasonSecretNotFound))
			Expect(cond(u, ConditionCredentialReady).Message).To(ContainSubstring(`no key "password"`))

			src := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userSecret}, src)).To(Succeed())
			src.Data["password"] = []byte(userPW1)
			Expect(k8sClient.Update(ctx, src)).To(Succeed())
			u = reconcile(apiUserName)
			good := credential(apiUserName)
			revision := u.Status.CredentialRevision
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionTrue))

			By("a short password: PasswordInvalid, the working credential stays")
			src.Data["password"] = []byte("short")
			Expect(k8sClient.Update(ctx, src)).To(Succeed())
			u = reconcile(apiUserName)
			Expect(cond(u, ConditionCredentialReady).Reason).To(Equal(ReasonPasswordInvalid))
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonPasswordInvalid))
			Expect(credential(apiUserName).Data["passwordHash"]).To(Equal(good.Data["passwordHash"]))
			Expect(u.Status.CredentialRevision).To(Equal(revision))

			By("the source is deleted: SecretNotFound, the credential stays")
			Expect(k8sClient.Delete(ctx, src)).To(Succeed())
			u = reconcile(apiUserName)
			Expect(cond(u, ConditionCredentialReady).Reason).To(Equal(ReasonSecretNotFound))
			Expect(credential(apiUserName).Data["passwordHash"]).To(Equal(good.Data["passwordHash"]))
			Expect(u.Status.CredentialSecretName).To(Equal(aliceCredential))
		})

		It("does not take over a Secret it does not own", func() {
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: aliceCredential, Namespace: ns},
				Data:       map[string][]byte{"passwordHash": []byte("{ARGON2ID}chosen-by-someone-else")},
			})).To(Succeed())
			newUser(apiUserName, nil)
			u := reconcile(apiUserName)
			Expect(cond(u, ConditionCredentialReady).Reason).To(Equal(ReasonCredentialSecretConflict))
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionFalse))
			s := credential(apiUserName)
			Expect(string(s.Data["passwordHash"])).To(Equal("{ARGON2ID}chosen-by-someone-else"))
			Expect(s.OwnerReferences).To(BeEmpty())
			Expect(u.Status.CredentialSecretName).To(BeEmpty())
		})

		It("works for a user name of 64 characters", func() {
			name := strings.Repeat("a", 64)
			newUser(name, nil)
			u := reconcile(name)
			s := credential(name)
			Expect(s.Name).To(HaveLen(64 + len(credentialSuffix)))
			Expect(s.Annotations).To(HaveKeyWithValue(annotationMailUser, name))
			for k, v := range s.Labels {
				Expect(len(v)).To(BeNumerically("<=", 63), "label %s", k)
			}
			Expect(u.Status.CredentialSecretName).To(Equal(s.Name))
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(row(u, name+"@"+domA).Kind).To(Equal(cnmsv1alpha1.AddressKindPrimary))
		})

		It("is safe when two reconciles race, and a lost race changes nothing", func() {
			newUser(apiUserName, nil)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: apiUserName}})
					Expect(err).NotTo(HaveOccurred())
				}()
			}
			wg.Wait()
			list := &corev1.SecretList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{manifests.ManagedByLabel: manifests.ManagedByValue})).To(Succeed())
			Expect(list.Items).To(HaveLen(1))
			u := reconcile(apiUserName)
			s := credential(apiUserName)
			Expect(verifies(string(s.Data["password"]), s.Data["passwordHash"])).To(BeTrue())

			By("a create that finds the Secret (AlreadyExists) is read again, not overwritten")
			r2 := *r
			r2.APIReader = staleReader{Reader: k8sClient, name: aliceCredential} // shows it as missing
			res, err := r2.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: apiUserName}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(credentialRetry))
			Expect(credential(apiUserName).Data).To(Equal(s.Data))
			Expect(credential(apiUserName).ResourceVersion).To(Equal(s.ResourceVersion))
			Expect(u.Status.CredentialRevision).To(Equal(s.ResourceVersion))
		})

		It("reports nothing for a pass that lost the race on a new account", func() {
			u := newUser("alice", nil)
			// The Secret is there (somebody else was first), but this
			// reconcile still sees none.
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: aliceCredential, Namespace: ns},
				Data:       map[string][]byte{"passwordHash": []byte("x")},
			})).To(Succeed())
			s := credential(apiUserName)
			Expect(controllerutil.SetControllerReference(u, s, k8sClient.Scheme())).To(Succeed())
			Expect(k8sClient.Update(ctx, s)).To(Succeed())

			r2 := *r
			r2.APIReader = staleReader{Reader: k8sClient, name: aliceCredential}
			res, err := r2.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: apiUserName}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(credentialRetry))
			got := &cnmsv1alpha1.MailUser{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, got)).To(Succeed())
			Expect(got.Status.Conditions).To(BeEmpty(), "no Ready, no CredentialReady=Unknown for a pass that did not finish")
			Expect(drainEvents(rec)).To(BeEmpty())
		})

		It("replaces a hash with other parameters, once, at the pinned cost", func() {
			source(userSecret, katPassword)
			newUser(apiUserName, withSource)
			reconcile(apiUserName)
			s := credential(apiUserName)
			// Somebody edits the Secret: a hash that Dovecot accepts, with
			// m=65536 and t=3, which this verifier does not read.
			s.Data["passwordHash"] = []byte(dovecotHash)
			Expect(k8sClient.Update(ctx, s)).To(Succeed())
			src := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userSecret}, src)).To(Succeed())
			src.Labels = map[string]string{touchedKey: touchedValue}
			Expect(k8sClient.Update(ctx, src)).To(Succeed())

			u := reconcile(apiUserName)
			got := credential(apiUserName)
			Expect(string(got.Data["passwordHash"])).To(HavePrefix("{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$"))
			Expect(got.Data["passwordHash"]).NotTo(Equal([]byte(dovecotHash)))
			Expect(verifies(katPassword, got.Data["passwordHash"])).To(BeTrue())
			Expect(u.Status.CredentialRevision).To(Equal(got.ResourceVersion), "a new hash moves the revision")
		})

		It("lets a stale update lose (the resourceVersion precondition)", func() {
			source(userSecret, userPW1)
			newUser(apiUserName, withSource)
			reconcile(apiUserName)
			stale := credential(apiUserName)
			// Somebody writes the Secret after the copy was taken.
			current := credential(apiUserName)
			current.Annotations[touchedKey] = touchedValue
			Expect(k8sClient.Update(ctx, current)).To(Succeed())
			// The source changes, so the stale copy would be rehashed and written.
			src := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userSecret}, src)).To(Succeed())
			src.Data["password"] = []byte(userPW2)
			Expect(k8sClient.Update(ctx, src)).To(Succeed())

			r2 := *r
			r2.APIReader = staleReader{Reader: k8sClient, name: aliceCredential, copy: stale}
			res, err := r2.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: apiUserName}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(credentialRetry))
			Expect(verifies(userPW1, credential(apiUserName).Data["passwordHash"])).To(BeTrue(), "the stale write did not land")
			reconcile(apiUserName)
			Expect(verifies(userPW2, credential(apiUserName).Data["passwordHash"])).To(BeTrue(), "the next read does it")
		})
	})

	Describe("the addresses and the conditions", func() {
		It("reports the rows, and the aliases that are not served", func() {
			foreign := uniqueDomain()
			createDomainG(ns, foreign, otherServer)
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) {
				u.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{
					{LocalPart: userSales, DomainRef: cnmsv1alpha1.NameReference{Name: refB}},
					{LocalPart: otherServer, DomainRef: cnmsv1alpha1.NameReference{Name: domainLabel(foreign)}},
					{LocalPart: "gone", DomainRef: cnmsv1alpha1.NameReference{Name: "no-such-domain"}},
				}
			})
			u := reconcile(apiUserName)
			Expect(u.Status.ObservedGeneration).To(Equal(u.Generation))
			Expect(u.Status.Address).To(Equal("alice@" + domA))
			Expect(u.Status.Addresses).To(HaveLen(4))
			Expect(row(u, "alice@"+domA)).To(Equal(cnmsv1alpha1.MailUserAddressStatus{
				Address: "alice@" + domA, Kind: cnmsv1alpha1.AddressKindPrimary, Served: true, Reason: addresses.ReasonServed}))
			Expect(row(u, "sales@"+domB).Served).To(BeTrue())
			Expect(row(u, "sales@"+domB).Kind).To(Equal(cnmsv1alpha1.AddressKindAlias))
			Expect(row(u, "other@"+foreign).Reason).To(Equal("AliasDomainNotServed"))
			Expect(row(u, "gone@no-such-domain").Reason).To(Equal("AliasDomainNotFound"))
			c := cond(u, ConditionAddressesServed)
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(ReasonPartiallyServed))
			Expect(c.Message).To(And(ContainSubstring("other@"+foreign), ContainSubstring("AliasDomainNotServed")))
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionTrue), "the warning never feeds Ready")
		})

		It("suspends an account whose primary domain is not authorized", func() {
			r.DomainAuthorization = AuthorizationAllowlist
			createGrantG(domB, ns, false) // domA has no grant
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) {
				u.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{{LocalPart: userSales, DomainRef: cnmsv1alpha1.NameReference{Name: refB}}}
			})
			u := reconcile(apiUserName)
			ready := cond(u, conditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(ReasonDomainNotAuthorized))
			Expect(ready.Message).To(ContainSubstring("no MailDomainGrant"))
			Expect(row(u, "alice@"+domA).Reason).To(Equal(ReasonDomainNotAuthorized))
			sales := row(u, "sales@"+domB)
			Expect(sales.Served).To(BeFalse())
			Expect(sales.Reason).To(Equal(ReasonDomainNotAuthorized), "the cause on the primary domain")
			Expect(cond(u, ConditionAddressesServed).Status).To(Equal(metav1.ConditionFalse))
			Expect(u.Status.CredentialSecretName).NotTo(BeEmpty(), "the credential is made for a suspended account")
		})

		It("reports a primary domain that does not exist or belongs to another server", func() {
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) { u.Spec.DomainRef.Name = "missing-domain" })
			u := reconcile(apiUserName)
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonDomainNotFound))
			Expect(u.Status.Address).To(BeEmpty(), "unset while the domain is not resolved")

			foreign := uniqueDomain()
			createDomainG(ns, foreign, otherServer)
			newUser("bob", func(u *cnmsv1alpha1.MailUser) { u.Spec.DomainRef.Name = domainLabel(foreign) })
			u = reconcile("bob")
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonDomainNotServed))
			Expect(cond(u, conditionReady).Message).To(ContainSubstring(otherServer))
		})

		It("makes every claimant of one address lose it, and a primary win", func() {
			sales := func(local string) func(*cnmsv1alpha1.MailUser) {
				return func(u *cnmsv1alpha1.MailUser) {
					u.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{{LocalPart: local, DomainRef: cnmsv1alpha1.NameReference{Name: refB}}}
				}
			}
			newUser(apiUserName, sales("team"))
			newUser("bob", sales("team"))
			newUser("carol", func(u *cnmsv1alpha1.MailUser) {
				u.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{{LocalPart: apiUserName, DomainRef: cnmsv1alpha1.NameReference{Name: refA}}}
			})
			alice, bob, carol := reconcile(apiUserName), reconcile("bob"), reconcile("carol")
			Expect(row(alice, "team@"+domB).Reason).To(Equal("AddressConflict"))
			Expect(row(bob, "team@"+domB).Reason).To(Equal("AddressConflict"))
			Expect(row(alice, "alice@"+domA).Served).To(BeTrue(), "a primary always wins")
			Expect(row(carol, "alice@"+domA).Reason).To(Equal("AddressConflict"))
			Expect(cond(alice, ConditionAddressesServed).Reason).To(Equal(ReasonPartiallyServed))
			Expect(cond(bob, conditionReady).Status).To(Equal(metav1.ConditionTrue))

			By("bob gives the alias up: alice has it")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userBob}, bob)).To(Succeed())
			bob.Spec.Aliases = nil
			Expect(k8sClient.Update(ctx, bob)).To(Succeed())
			reconcile("bob")
			alice = reconcile(apiUserName)
			Expect(row(alice, "team@"+domB).Served).To(BeTrue())
			Expect(cond(alice, ConditionAddressesServed).Reason).To(Equal(ReasonAllServed))
		})

		It("is Ready with the reason Disabled for a disabled account", func() {
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) { u.Spec.Disabled = new(true) })
			u := reconcile(apiUserName)
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonDisabled))
			Expect(u.Status.CredentialSecretName).NotTo(BeEmpty(), "the hash is kept for the day it is enabled")
		})
	})

	Describe("the server", func() {
		It("is not Ready without the MailServer", func() {
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) { u.Spec.ServerRef.Name = "no-such-server" })
			u := reconcile(apiUserName)
			Expect(cond(u, conditionReady).Status).To(Equal(metav1.ConditionFalse))
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonServerNotFound))
			Expect(cond(u, ConditionAddressesServed).Status).To(Equal(metav1.ConditionUnknown), "nothing serves the account")
			Expect(cond(u, ConditionAddressesServed).Reason).To(Equal(ReasonServerNotFound))
			Expect(u.Status.Addresses).To(BeEmpty())
			Expect(u.Status.Address).To(BeEmpty())
		})

		It("reports the other user sources and makes no credential", func() {
			createServerG(ns, "withdir", nil) // the default test server has a directoryRef
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) { u.Spec.ServerRef.Name = "withdir" })
			u := reconcile(apiUserName)
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonDirectoryConfigured))
			Expect(cond(u, ConditionAddressesServed).Status).To(Equal(metav1.ConditionUnknown))
			Expect(cond(u, ConditionAddressesServed).Reason).To(Equal(ReasonDirectoryConfigured))
			Expect(u.Status.Addresses).To(BeEmpty(), "the rows would claim addresses that no server serves")
			Expect(cond(u, ConditionCredentialReady)).To(BeNil())
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: aliceCredential}, &corev1.Secret{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			createServerG(ns, "withstatic", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.DirectoryRef = nil
				s.Spec.Submission = &cnmsv1alpha1.Submission{InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: "users"}}
			})
			newUser("bob", func(u *cnmsv1alpha1.MailUser) { u.Spec.ServerRef.Name = "withstatic" })
			u = reconcile("bob")
			Expect(cond(u, conditionReady).Reason).To(Equal(ReasonUserSourceConflict))
			Expect(cond(u, ConditionAddressesServed).Status).To(Equal(metav1.ConditionUnknown))
			Expect(u.Status.Addresses).To(BeEmpty())
		})
	})

	Describe("what it says", func() {
		It("puts no password or hash into a condition, an Event or the log", func() {
			source(userSecret, userPW1)
			newUser(apiUserName, withSource)
			newUser("bob", nil)
			var logs bytes.Buffer
			logCtx := logf.IntoContext(ctx, zap.New(zap.WriteTo(&logs), zap.UseDevMode(true)))

			var statuses []string
			note := func(u *cnmsv1alpha1.MailUser) {
				b, err := json.Marshal(u.Status)
				Expect(err).NotTo(HaveOccurred())
				statuses = append(statuses, string(b))
			}
			_, u := reconcileWith(logCtx, apiUserName)
			note(u)
			_, u = reconcileWith(logCtx, "bob")
			note(u)
			generated := string(credential("bob").Data["password"])
			hashes := []string{string(credential(apiUserName).Data["passwordHash"]), string(credential("bob").Data["passwordHash"])}

			// The failures: a short password, a missing source, a conflict.
			src := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: userSecret}, src)).To(Succeed())
			src.Data["password"] = []byte("tiny")
			Expect(k8sClient.Update(ctx, src)).To(Succeed())
			_, u = reconcileWith(logCtx, apiUserName)
			note(u)
			Expect(k8sClient.Delete(ctx, src)).To(Succeed())
			_, u = reconcileWith(logCtx, apiUserName)
			note(u)

			all := strings.Join(append(statuses, drainEvents(rec)...), "\n") + logs.String()
			for _, secret := range append(hashes, generated, userPW1, "tiny", "$argon2id$") {
				Expect(all).NotTo(ContainSubstring(secret))
			}
			Expect(logs.String()).To(ContainSubstring("Updated MailUser status"))
		})

		It("emits an Event when the state turns bad and none while it stays", func() {
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) { u.Spec.ServerRef.Name = "no-such-server" })
			reconcile(apiUserName)
			first := drainEvents(rec)
			Expect(first).To(HaveLen(1))
			Expect(first[0]).To(ContainSubstring(ReasonServerNotFound))
			reconcile(apiUserName)
			Expect(drainEvents(rec)).To(BeEmpty())
		})
	})

	Describe("the cluster", func() {
		It("has no finalizer and owns its Secret through the owner reference", func() {
			newUser(apiUserName, nil)
			u := reconcile(apiUserName)
			Expect(u.Finalizers).To(BeEmpty())
			refs := credential(apiUserName).OwnerReferences
			Expect(refs).To(HaveLen(1))
			Expect(refs[0].UID).To(Equal(u.UID))
			Expect(*refs[0].Controller).To(BeTrue())
			Expect(refs[0].Kind).To(Equal("MailUser"))
		})

		It("ignores a user that is gone", func() {
			res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "nobody"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(ctrl.Result{}))
		})

		It("reconciles through the watches: a new account, a revoked domain, a given-up address", func() {
			startManagerG(func(m ctrl.Manager) error {
				return (&MailUserReconciler{
					Client: m.GetClient(), Scheme: m.GetScheme(), APIReader: m.GetAPIReader(),
					DomainAuthorization: AuthorizationUnrestricted,
				}).SetupWithManager(m)
			})
			newUser(apiUserName, func(u *cnmsv1alpha1.MailUser) {
				u.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{{LocalPart: "team", DomainRef: cnmsv1alpha1.NameReference{Name: refB}}}
			})
			Eventually(func(g Gomega) {
				u := &cnmsv1alpha1.MailUser{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, u)).To(Succeed())
				g.Expect(cond(u, conditionReady)).NotTo(BeNil())
				g.Expect(u.Status.CredentialSecretName).To(Equal(aliceCredential))
			}, "30s", "200ms").Should(Succeed())

			// A second account takes the same alias: alice is told, though
			// her own object did not change.
			bob := newUser("bob", func(u *cnmsv1alpha1.MailUser) {
				u.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{{LocalPart: "team", DomainRef: cnmsv1alpha1.NameReference{Name: refB}}}
			})
			Eventually(func(g Gomega) {
				u := &cnmsv1alpha1.MailUser{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, u)).To(Succeed())
				g.Expect(u.Status.Addresses).To(ContainElement(HaveField("Reason", "AddressConflict")))
			}, "30s", "200ms").Should(Succeed())

			// It gives the alias up (deleted): alice has it again.
			Expect(k8sClient.Delete(ctx, bob)).To(Succeed())
			Eventually(func(g Gomega) {
				u := &cnmsv1alpha1.MailUser{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, u)).To(Succeed())
				g.Expect(cond(u, ConditionAddressesServed).Reason).To(Equal(ReasonAllServed))
			}, "30s", "200ms").Should(Succeed())

			// The domain of the alias moves to another server: the alias is
			// no longer served, with no change to the account.
			md := &cnmsv1alpha1.MailDomain{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: refB}, md)).To(Succeed())
			md.Spec.ServerRef.Name = otherServer
			Expect(k8sClient.Update(ctx, md)).To(Succeed())
			Eventually(func(g Gomega) {
				u := &cnmsv1alpha1.MailUser{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiUserName}, u)).To(Succeed())
				g.Expect(u.Status.Addresses).To(ContainElement(HaveField("Reason", "AliasDomainNotServed")))
			}, "30s", "200ms").Should(Succeed())
		})
	})
})
