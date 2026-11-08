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
	"encoding/json"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// fakeLooker answers the directory question of a service account without a
// network: owned, or the outcome of a lookup that did not run.
type fakeLooker struct {
	mu      sync.Mutex
	owned   bool
	outcome directory.Outcome
	asked   []string
}

func (f *fakeLooker) answer(address string) (bool, directory.Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, address)
	if f.outcome == "" {
		return f.owned, directory.Reachable
	}
	return false, f.outcome
}

func (f *fakeLooker) set(owned bool, outcome directory.Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owned, f.outcome = owned, outcome
}

func (f *fakeLooker) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func (f *fakeLooker) OwnsLDAP(_ context.Context, _ directory.LDAPTarget, address string) (bool, directory.Outcome) {
	return f.answer(address)
}

func (f *fakeLooker) OwnsSQL(_ context.Context, _ directory.SQLTarget, address string) (bool, directory.Outcome) {
	return f.answer(address)
}

const (
	accName     = "app"
	accPassword = "password"
	goneServer  = "gone"
	peerAccount = "other"
)

var _ = Describe("MailServiceAccount Controller", func() {
	var (
		srvNS, appNS string
		dom, domRef  string
		rec          *events.FakeRecorder
		looker       *fakeLooker
		indexed      indexedClient
		verifyRuns   int
		r            *MailServiceAccountReconciler
	)

	BeforeEach(func() {
		srvNS, appNS = uniqueNamespace(), uniqueNamespace()
		createNamespaceG(srvNS)
		createNamespaceG(appNS)
		dom = uniqueDomain()
		domRef = domainLabel(dom)
		createServerG(srvNS, userServer, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = nil
			s.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{
				AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{{Name: appNS}},
			}
		})
		createDomainG(srvNS, dom, userServer)
		rec = events.NewFakeRecorder(50)
		looker = &fakeLooker{}
		indexed = newIndexedClient(k8sClient)
		verifyRuns = 0
		r = &MailServiceAccountReconciler{
			Client: indexed, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Recorder: rec, Directory: looker,
			DomainAuthorization: AuthorizationUnrestricted, onVerify: func() { verifyRuns++ },
		}
	})

	sender := func(local string) string { return local + "@" + dom }
	newAccountIn := func(ns, name string, mutate func(*cnmsv1alpha1.MailServiceAccount)) *cnmsv1alpha1.MailServiceAccount {
		a := &cnmsv1alpha1.MailServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: cnmsv1alpha1.MailServiceAccountSpec{
				ServerRef: cnmsv1alpha1.NamespacedNameReference{Name: userServer, Namespace: srvNS},
				Sender:    sender("app"),
			},
		}
		if mutate != nil {
			mutate(a)
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		return a
	}
	newAccount := func(mutate func(*cnmsv1alpha1.MailServiceAccount)) *cnmsv1alpha1.MailServiceAccount {
		return newAccountIn(appNS, accName, mutate)
	}
	reconcileIn := func(ns, name string) (ctrl.Result, *cnmsv1alpha1.MailServiceAccount) {
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		Expect(err).NotTo(HaveOccurred())
		a := &cnmsv1alpha1.MailServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, a)).To(Succeed())
		return res, a
	}
	reconcile := func() *cnmsv1alpha1.MailServiceAccount {
		_, a := reconcileIn(appNS, accName)
		return a
	}
	cond := func(a *cnmsv1alpha1.MailServiceAccount, t string) *metav1.Condition {
		return meta.FindStatusCondition(a.Status.Conditions, t)
	}
	secretIn := func(ns, name string) (*corev1.Secret, error) {
		s := &corev1.Secret{}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, s)
		return s, err
	}
	credential := func() *corev1.Secret {
		s, err := secretIn(appNS, accName+"-credential")
		Expect(err).NotTo(HaveOccurred())
		return s
	}
	noCredential := func(ns, name string) {
		_, err := secretIn(ns, name+"-credential")
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "no credential Secret")
		np := &networkingv1.NetworkPolicy{}
		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name + "-submission"}, np)
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "no NetworkPolicy")
	}
	notReady := func(a *cnmsv1alpha1.MailServiceAccount, reason string) {
		ExpectWithOffset(1, cond(a, conditionReady).Status).To(Equal(metav1.ConditionFalse))
		ExpectWithOffset(1, cond(a, conditionReady).Reason).To(Equal(reason))
	}
	verifies := func(password string, hash []byte) bool {
		ok, err := verifyHash(ctx, password, string(hash))
		Expect(err).NotTo(HaveOccurred())
		return ok
	}
	withDirectory := func() {
		createDirectoryG(srvNS)
		mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = &cnmsv1alpha1.NameReference{Name: directoryName}
		})
	}

	Describe("an account that the server accepts", func() {
		It("makes the credential and the egress policy, in its own namespace", func() {
			newAccount(nil)
			a := reconcile()

			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(cond(a, ConditionCredentialReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(cond(a, ConditionEgressPolicyReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(a.Status.ObservedGeneration).To(Equal(a.Generation))
			Expect(a.Status.AdmittedSender).To(Equal(sender("app")))

			s := credential()
			login := serviceAccountLogin(appNS, accName)
			Expect(login).To(MatchRegexp(`^sa\.[0-9a-f]{16}$`))
			Expect(metav1.IsControlledBy(s, a)).To(BeTrue(), "the account owns the Secret (no GC in envtest)")
			Expect(s.Labels).To(HaveKeyWithValue(manifests.ManagedByLabel, manifests.ManagedByValue))
			Expect(s.Labels).To(HaveKeyWithValue(manifests.ServerLabel, userServer))
			Expect(s.Labels).To(HaveKeyWithValue(labelServerNamespace, srvNS))
			Expect(s.Labels).To(HaveKeyWithValue(labelServiceAccount, accName))
			Expect(s.Data).To(HaveLen(7), "no ca.crt without a certificate")
			Expect(string(s.Data["username"])).To(Equal(login))
			Expect(string(s.Data["server"])).To(Equal("mail-postfix." + srvNS + ".svc.cluster.local"))
			Expect(string(s.Data["tls"])).To(Equal("mandatory"))
			Expect(string(s.Data["helo"])).To(Equal(serverHostnameG(srvNS, userServer)))
			Expect(string(s.Data["tlsServerName"])).To(Equal(serverHostnameG(srvNS, userServer)))
			Expect(s.Data["password"]).To(HaveLen(43))
			Expect(string(s.Data["passwordHash"])).To(HavePrefix("{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$"))
			Expect(verifies(string(s.Data["password"]), s.Data["passwordHash"])).To(BeTrue())
			Expect(a.Status.CredentialSecretName).To(Equal(accName + "-credential"))
			Expect(a.Status.CredentialRevision).To(Equal(s.ResourceVersion))

			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName + "-submission"}, np)).To(Succeed())
			Expect(metav1.IsControlledBy(np, a)).To(BeTrue())
			Expect(np.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{labelServiceAccount: accName}))
			Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeEgress))
			Expect(np.Spec.Egress).To(HaveLen(2))
			smtp := np.Spec.Egress[1]
			Expect(smtp.Ports).To(HaveLen(1))
			Expect(smtp.Ports[0].Port.IntValue()).To(Equal(587))
			Expect(smtp.To).To(HaveLen(1))
			Expect(smtp.To[0].NamespaceSelector.MatchLabels).To(Equal(map[string]string{"kubernetes.io/metadata.name": srvNS}))
			Expect(smtp.To[0].PodSelector.MatchLabels).To(HaveKeyWithValue(manifests.ServerLabel, userServer))
			Expect(smtp.To[0].PodSelector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/component", "postfix"))
		})

		It("accepts an account of the namespace of the server without consent", func() {
			newAccountIn(srvNS, accName, func(a *cnmsv1alpha1.MailServiceAccount) { a.Spec.ServerRef.Namespace = "" })
			_, a := reconcileIn(srvNS, accName)
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			_, err := secretIn(srvNS, accName+"-credential")
			Expect(err).NotTo(HaveOccurred())
		})

		It("makes a credential and a policy for a name of 63 characters", func() {
			name := strings.Repeat("a", 63)
			newAccountIn(appNS, name, nil)
			_, a := reconcileIn(appNS, name)
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			s, err := secretIn(appNS, name+"-credential")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Labels).To(HaveKeyWithValue(labelServiceAccount, name))
		})

		It("adds the CA chain of the server when its certificate has one", func() {
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: userServer + "-tls", Namespace: srvNS},
				Data:       map[string][]byte{keyCACert: []byte("CA PEM"), manifests.TLSKeyFile: []byte("never copied")},
			})).To(Succeed())
			newAccount(nil)
			a := reconcile()
			s := credential()
			Expect(string(s.Data["ca.crt"])).To(Equal("CA PEM"))
			Expect(s.Data).NotTo(HaveKey(manifests.TLSKeyFile))
			Expect(cond(a, ConditionCredentialReady).Message).NotTo(ContainSubstring("omitted"))
		})

		It("says that ca.crt is omitted without a CA, and not as a failure", func() {
			newAccount(nil)
			a := reconcile()
			Expect(cond(a, ConditionCredentialReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(cond(a, ConditionCredentialReady).Message).To(ContainSubstring("ca.crt is omitted"))
		})

		It("presents the name of the Service while the hostname is not authorized", func() {
			// A second server with the same hostname: both are blocked from it.
			dup := uniqueNamespace()
			createNamespaceG(dup)
			createServerG(dup, userServer, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.DirectoryRef = nil
				s.Spec.Hostname = serverHostnameG(srvNS, userServer)
			})
			newAccount(nil)
			a := reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue), "the hostname does not stop the account")
			s := credential()
			service := "mail-postfix." + srvNS + ".svc.cluster.local"
			Expect(string(s.Data["helo"])).To(Equal(service))
			Expect(string(s.Data["tlsServerName"])).To(Equal(service))
		})
	})

	Describe("the credential", func() {
		It("is made once, kept, and rewritten only when it does not verify", func() {
			newAccount(nil)
			a := reconcile()
			s := credential()
			revision := a.Status.CredentialRevision

			// Again, and again: nothing moves, the password stays.
			a2 := reconcile()
			reconcile()
			s2 := credential()
			Expect(s2.ResourceVersion).To(Equal(s.ResourceVersion))
			Expect(s2.Data).To(Equal(s.Data))
			Expect(a2.Status.CredentialRevision).To(Equal(revision))

			// A connection value that drifted is put back; the password and
			// the revision stay.
			s2.Data["helo"] = []byte("evil.example.net")
			Expect(k8sClient.Update(ctx, s2)).To(Succeed())
			a3 := reconcile()
			s3 := credential()
			Expect(string(s3.Data["helo"])).To(Equal(serverHostnameG(srvNS, userServer)))
			Expect(s3.Data["password"]).To(Equal(s.Data["password"]))
			Expect(s3.Data["passwordHash"]).To(Equal(s.Data["passwordHash"]))
			Expect(a3.Status.CredentialRevision).To(Equal(revision), "the revision moves only with the hash")

			// A hash that does not belong to the password (an edit by the
			// tenant) is replaced together with the password.
			other, err := hashPassword(ctx, "a-password-the-tenant-chose")
			Expect(err).NotTo(HaveOccurred())
			s3.Data["passwordHash"] = []byte(other)
			Expect(k8sClient.Update(ctx, s3)).To(Succeed())
			a4 := reconcile()
			s4 := credential()
			Expect(s4.Data["password"]).NotTo(Equal(s.Data["password"]))
			Expect(verifies(string(s4.Data["password"]), s4.Data["passwordHash"])).To(BeTrue())
			Expect(verifies("a-password-the-tenant-chose", s4.Data["passwordHash"])).To(BeFalse())
			Expect(a4.Status.CredentialRevision).To(Equal(s4.ResourceVersion))
			Expect(a4.Status.CredentialRevision).NotTo(Equal(revision))
		})

		It("reads the accounts of the server through the index", func() {
			newAccount(nil)
			reconcile()
			Expect(indexed.served.all()).To(ContainElement(mailServiceAccountServerIndex + "=" + srvNS + "/" + userServer))
		})

		It("verifies the hash only when the Secret has changed", func() {
			newAccount(nil)
			reconcile()
			Expect(verifyRuns).To(BeZero(), "a Secret that was just made needs no verification")
			reconcile()
			reconcile()
			Expect(verifyRuns).To(BeZero(), "an unchanged Secret is not verified again")

			s := credential()
			s.Labels["touched"] = touchedValue
			Expect(k8sClient.Update(ctx, s)).To(Succeed())
			reconcile()
			Expect(verifyRuns).To(Equal(1), "every edit is checked")
			reconcile()
			Expect(verifyRuns).To(Equal(1))

			// A restart forgets what was verified: once more.
			r.verified = verifiedSecrets{}
			reconcile()
			Expect(verifyRuns).To(Equal(2))
			reconcile()
			Expect(verifyRuns).To(Equal(2))

			// A deleted Secret is made again, with a new password.
			Expect(k8sClient.Delete(ctx, credential())).To(Succeed())
			reconcile()
			reconcile()
			Expect(verifyRuns).To(Equal(2))
		})

		It("keeps a consistent password and hash that the tenant wrote, for its own login only", func() {
			newAccount(nil)
			reconcile()
			s := credential()
			chosen := "a-password-the-tenant-chose"
			hash, err := hashPassword(ctx, chosen)
			Expect(err).NotTo(HaveOccurred())
			s.Data["password"], s.Data["passwordHash"] = []byte(chosen), []byte(hash)
			s.Data["username"] = []byte("alice")
			Expect(k8sClient.Update(ctx, s)).To(Succeed())
			reconcile()
			after := credential()
			Expect(string(after.Data["password"])).To(Equal(chosen), "a consistent pair is kept")
			Expect(string(after.Data["username"])).To(Equal(serviceAccountLogin(appNS, accName)), "the login is the account's, whatever the Secret says")
		})

		It("is made again, with a new password, when the Secret is deleted", func() {
			newAccount(nil)
			reconcile()
			first := credential()
			Expect(k8sClient.Delete(ctx, first)).To(Succeed())
			a := reconcile()
			again := credential()
			Expect(again.Data["password"]).NotTo(Equal(first.Data["password"]))
			Expect(a.Status.CredentialRevision).To(Equal(again.ResourceVersion))
		})

		It("does not take over a Secret of that name that the account does not own", func() {
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: accName + "-credential", Namespace: appNS},
				Data:       map[string][]byte{accPassword: []byte("theirs"), keyPasswordHash: []byte("their hash")},
			})).To(Succeed())
			newAccount(nil)
			a := reconcile()
			notReady(a, ReasonCredentialSecretConflict)
			Expect(cond(a, ConditionCredentialReady).Reason).To(Equal(ReasonCredentialSecretConflict))
			s := credential()
			Expect(string(s.Data[accPassword])).To(Equal("theirs"))
			Expect(s.Data).To(HaveLen(2))
			Expect(a.Status.CredentialSecretName).To(BeEmpty())
		})

		It("does not take over a NetworkPolicy of that name that the account does not own", func() {
			Expect(k8sClient.Create(ctx, &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: accName + "-submission", Namespace: appNS},
				Spec:       networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}},
			})).To(Succeed())
			newAccount(nil)
			a := reconcile()
			notReady(a, ReasonOwnershipConflict)
			Expect(cond(a, ConditionEgressPolicyReady).Reason).To(Equal(ReasonOwnershipConflict))
			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName + "-submission"}, np)).To(Succeed())
			Expect(np.Spec.Egress).To(BeEmpty(), "the policy of the tenant is untouched")
			Expect(np.Spec.PolicyTypes).NotTo(ContainElement(networkingv1.PolicyTypeEgress))
			_, err := secretIn(appNS, accName+"-credential")
			Expect(err).NotTo(HaveOccurred(), "the credential is made all the same")
		})

		It("carries no secret material into the status, the Events or the object names", func() {
			newAccount(nil)
			a := reconcile()
			s := credential()
			raw, err := json.Marshal(a.Status)
			Expect(err).NotTo(HaveOccurred())
			for _, secret := range []string{string(s.Data["password"]), string(s.Data["passwordHash"])} {
				Expect(string(raw)).NotTo(ContainSubstring(secret))
			}
			close(rec.Events)
			for e := range rec.Events {
				Expect(e).NotTo(ContainSubstring(string(s.Data["password"])))
				Expect(e).NotTo(ContainSubstring("ARGON2ID"))
			}
		})
	})

	Describe("the egress policy", func() {
		It("is put back when it was changed, and removed when the account is revoked", func() {
			newAccount(nil)
			reconcile()
			np := &networkingv1.NetworkPolicy{}
			key := types.NamespacedName{Namespace: appNS, Name: accName + "-submission"}
			Expect(k8sClient.Get(ctx, key, np)).To(Succeed())
			want := np.Spec.DeepCopy()
			np.Spec.Egress = nil
			Expect(k8sClient.Update(ctx, np)).To(Succeed())
			reconcile()
			Expect(k8sClient.Get(ctx, key, np)).To(Succeed())
			Expect(np.Spec).To(Equal(*want))

			// The server withdraws its consent: credential and policy go.
			mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) { s.Spec.ServiceAccounts = nil })
			a := reconcile()
			notReady(a, ReasonNamespaceNotAllowed)
			noCredential(appNS, accName)
			Expect(a.Status.CredentialSecretName).To(BeEmpty())
			Expect(a.Status.CredentialRevision).To(BeEmpty())
			Expect(a.Status.AdmittedSender).To(BeEmpty())
			Expect(cond(a, ConditionCredentialReady)).To(BeNil())
			Expect(cond(a, ConditionEgressPolicyReady)).To(BeNil())

			// Consent again: a new credential, with a new password.
			mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{
					AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{{Name: appNS}},
				}
			})
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
		})

		It("is not removed with a Secret or policy that another owner has", func() {
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: accName + "-credential", Namespace: appNS},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: accName + "-submission", Namespace: appNS},
			})).To(Succeed())
			newAccount(nil)
			mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) { s.Spec.ServiceAccounts = nil })
			a := reconcile()
			notReady(a, ReasonNamespaceNotAllowed)
			_, err := secretIn(appNS, accName+"-credential")
			Expect(err).NotTo(HaveOccurred(), "a Secret the account does not control is never deleted")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName + "-submission"}, &networkingv1.NetworkPolicy{})).To(Succeed())
		})
	})

	Describe("consent and the sender domain", func() {
		It("refuses a namespace the server does not list, and makes nothing", func() {
			other := uniqueNamespace()
			createNamespaceG(other)
			newAccountIn(other, accName, nil)
			_, a := reconcileIn(other, accName)
			notReady(a, ReasonNamespaceNotAllowed)
			noCredential(other, accName)
			Expect(a.Status.AdmittedSender).To(BeEmpty())
			Expect(cond(a, ConditionCredentialReady)).To(BeNil())
		})

		It("limits a namespace to its senderDomains", func() {
			second := uniqueDomain()
			createDomainG(srvNS, second, userServer)
			mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.ServiceAccounts.AllowedNamespaces[0].SenderDomains = []string{dom}
			})
			newAccount(nil)
			a := reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue), "a domain on the list")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName}, a)).To(Succeed())
			a.Spec.Sender = "app@" + second
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			a = reconcile()
			notReady(a, ReasonSenderNotAuthorized)
			Expect(cond(a, conditionReady).Message).To(ContainSubstring(second))
			noCredential(appNS, accName)
			Expect(a.Status.AdmittedSender).To(BeEmpty())
		})

		It("refuses a sender in a domain the server does not serve", func() {
			createDomainG(srvNS, "elsewhere.example.net", otherServer)
			newAccount(func(a *cnmsv1alpha1.MailServiceAccount) { a.Spec.Sender = "app@elsewhere.example.net" })
			a := reconcile()
			notReady(a, ReasonSenderNotAuthorized)
			Expect(cond(a, conditionReady).Message).To(ContainSubstring("another MailServer"))
			noCredential(appNS, accName)
		})

		It("refuses a sender in a domain that has no MailDomain", func() {
			newAccount(func(a *cnmsv1alpha1.MailServiceAccount) { a.Spec.Sender = "app@unknown.example.net" })
			a := reconcile()
			notReady(a, ReasonSenderNotAuthorized)
			Expect(cond(a, conditionReady).Message).To(ContainSubstring("no MailDomain"))
			noCredential(appNS, accName)
		})

		It("refuses a sender in a domain that is not authorized for the namespace of the server", func() {
			r.DomainAuthorization = AuthorizationAllowlist
			newAccount(nil)
			a := reconcile()
			notReady(a, ReasonSenderNotAuthorized)
			Expect(cond(a, conditionReady).Message).To(ContainSubstring("not authorized"))
			noCredential(appNS, accName)

			createGrantG(dom, srvNS, false)
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: dom}})
			})
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
		})

		It("reports a missing server of its own namespace, and keeps what it made", func() {
			ownAccount := func(mutate func(*cnmsv1alpha1.MailServiceAccount)) {
				newAccountIn(srvNS, accName, func(a *cnmsv1alpha1.MailServiceAccount) {
					a.Spec.ServerRef.Namespace = ""
					if mutate != nil {
						mutate(a)
					}
				})
			}
			ownAccount(func(a *cnmsv1alpha1.MailServiceAccount) { a.Spec.ServerRef.Name = goneServer })
			_, a := reconcileIn(srvNS, accName)
			notReady(a, ReasonServerNotFound)
			noCredential(srvNS, accName)

			// A server that goes away does not take the credential with it.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: srvNS, Name: accName}, a)).To(Succeed())
			a.Spec.ServerRef.Name = userServer
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			reconcileIn(srvNS, accName)
			first, err := secretIn(srvNS, accName+"-credential")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: srvNS, Name: accName}, a)).To(Succeed())
			a.Spec.ServerRef.Name = goneServer
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			_, a = reconcileIn(srvNS, accName)
			notReady(a, ReasonServerNotFound)
			again, err := secretIn(srvNS, accName+"-credential")
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Data["password"]).To(Equal(first.Data["password"]))
			Expect(a.Status.CredentialSecretName).To(Equal(accName + "-credential"))
		})

		It("does not tell a namespace whether a server of another namespace exists", func() {
			// A server that exists and does not consent, and one that does not
			// exist, give the same answer; only the name they were asked for
			// differs.
			other := uniqueNamespace()
			createNamespaceG(other)
			newAccountIn(other, accName, nil)
			newAccountIn(other, "second", func(a *cnmsv1alpha1.MailServiceAccount) { a.Spec.ServerRef.Name = goneServer })
			_, exists := reconcileIn(other, accName)
			_, missing := reconcileIn(other, "second")
			notReady(exists, ReasonNamespaceNotAllowed)
			notReady(missing, ReasonNamespaceNotAllowed)
			Expect(strings.ReplaceAll(cond(missing, conditionReady).Message, goneServer, userServer)).To(Equal(cond(exists, conditionReady).Message))
			Expect(cond(missing, conditionReady).Message).NotTo(ContainSubstring("exist"))
			noCredential(other, "second")
		})

		It("keeps the credential when the server of another namespace goes missing", func() {
			newAccount(nil)
			a := reconcile()
			first := credential()
			Expect(a.Status.AdmittedSender).NotTo(BeEmpty())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName}, a)).To(Succeed())
			a.Spec.ServerRef.Name = goneServer
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			a = reconcile()
			notReady(a, ReasonNamespaceNotAllowed)
			Expect(credential().Data["password"]).To(Equal(first.Data["password"]), "a missing server revokes nothing")
			Expect(a.Status.CredentialSecretName).To(Equal(accName + "-credential"))
			Expect(a.Status.AdmittedSender).NotTo(BeEmpty())
		})

		It("reports a server without submission", func() {
			mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) { s.Spec.Submission = &cnmsv1alpha1.Submission{Enabled: new(false)} })
			newAccount(nil)
			a := reconcile()
			notReady(a, ReasonSubmissionDisabled)
			noCredential(appNS, accName)
		})
	})

	Describe("who owns the sender", func() {
		It("takes the sender from both claimants when two accounts name it", func() {
			second := uniqueNamespace()
			createNamespaceG(second)
			mutateServerG(srvNS, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.ServiceAccounts.AllowedNamespaces = append(s.Spec.ServiceAccounts.AllowedNamespaces, cnmsv1alpha1.AllowedNamespace{Name: second})
			})
			newAccount(nil)
			newAccountIn(second, peerAccount, nil)

			a := reconcile()
			notReady(a, ReasonSenderConflict)
			noCredential(appNS, accName)
			_, b := reconcileIn(second, peerAccount)
			notReady(b, ReasonSenderConflict)
			noCredential(second, peerAccount)
			Expect(cond(a, conditionReady).Message).NotTo(ContainSubstring(second), "no other tenant is named")

			// One gives it up: the other has it.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: second, Name: peerAccount}, b)).To(Succeed())
			b.Spec.Sender = sender(peerAccount)
			Expect(k8sClient.Update(ctx, b)).To(Succeed())
			_, b = reconcileIn(second, peerAccount)
			Expect(cond(b, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
		})

		It("does not let a namespace the server does not consent to take the sender away", func() {
			stranger := uniqueNamespace()
			createNamespaceG(stranger)
			newAccount(nil)
			newAccountIn(stranger, "thief", nil)
			a := reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			_, thief := reconcileIn(stranger, "thief")
			notReady(thief, ReasonNamespaceNotAllowed)
		})

		It("gives the address of a MailUser to the MailUser", func() {
			Expect(k8sClient.Create(ctx, &cnmsv1alpha1.MailUser{
				ObjectMeta: metav1.ObjectMeta{Name: apiUserName, Namespace: srvNS},
				Spec: cnmsv1alpha1.MailUserSpec{
					ServerRef: cnmsv1alpha1.NameReference{Name: userServer},
					DomainRef: cnmsv1alpha1.NameReference{Name: domRef},
				},
			})).To(Succeed())
			newAccount(func(a *cnmsv1alpha1.MailServiceAccount) { a.Spec.Sender = sender(apiUserName) })
			a := reconcile()
			notReady(a, ReasonSenderConflict)
			Expect(cond(a, conditionReady).Message).To(ContainSubstring("primary address"))
			noCredential(appNS, accName)
		})

		It("moves the admitted sender with a changed sender", func() {
			newAccount(nil)
			a := reconcile()
			first := credential()
			Expect(a.Status.AdmittedSender).To(Equal(sender("app")))
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName}, a)).To(Succeed())
			a.Spec.Sender = sender("billing")
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			a = reconcile()
			Expect(a.Status.AdmittedSender).To(Equal(sender("billing")))
			Expect(credential().Data["password"]).To(Equal(first.Data["password"]), "the sender is not part of the credential")
		})
	})

	Describe("the admission against a directory", func() {
		moveDomainTo := func(server string) {
			d := &cnmsv1alpha1.MailDomain{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: srvNS, Name: domRef}, d)).To(Succeed())
			d.Spec.ServerRef.Name = server
			Expect(k8sClient.Update(ctx, d)).To(Succeed())
		}
		repoint := func(server string) {
			a := &cnmsv1alpha1.MailServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName}, a)).To(Succeed())
			a.Spec.ServerRef.Name = server
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
		}

		It("asks again when the server gains a directory, and takes the admission back on a hit", func() {
			newAccount(nil)
			a := reconcile()
			Expect(a.Status.AdmittedFor).To(Equal(srvNS + "/" + userServer + "/"))
			Expect(looker.calls()).To(BeZero(), "a server without a directory is not asked")

			withDirectory()
			looker.set(true, "")
			a = reconcile()
			notReady(a, ReasonSenderConflict)
			Expect(a.Status.AdmittedSender).To(BeEmpty())
			Expect(a.Status.AdmittedFor).To(BeEmpty())
			Expect(looker.calls()).To(Equal(1))
		})

		It("withholds an admitted account while the directory a server gained cannot be asked", func() {
			newAccount(nil)
			reconcile()
			first := credential()
			withDirectory()
			looker.set(false, directory.Unreachable)
			a := reconcile()
			notReady(a, ReasonDirectoryUnavailable)
			Expect(a.Status.AdmittedSender).To(Equal(sender("app")))
			Expect(a.Status.AdmittedFor).To(Equal(srvNS+"/"+userServer+"/"), "the old admission: the server does not serve the account")
			Expect(credential().Data["password"]).To(Equal(first.Data["password"]))

			looker.set(false, "")
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(a.Status.AdmittedFor).To(Equal(srvNS + "/" + userServer + "/" + directoryName))
		})

		It("asks again when the account names another server", func() {
			createDirectoryG(srvNS)
			createServerG(srvNS, otherServer, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{
					AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{{Name: appNS}},
				}
			})
			newAccount(nil)
			a := reconcile()
			Expect(a.Status.AdmittedSender).To(Equal(sender("app")))

			// The domain and the account move to a server with a directory
			// that cannot be asked: the same sender, but not admitted for it.
			moveDomainTo(otherServer)
			repoint(otherServer)
			looker.set(false, directory.Unreachable)
			a = reconcile()
			notReady(a, ReasonDirectoryUnavailable)
			Expect(a.Status.AdmittedFor).To(Equal(srvNS + "/" + userServer + "/"))

			looker.set(false, "")
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(a.Status.AdmittedFor).To(Equal(srvNS + "/" + otherServer + "/" + directoryName))
		})
	})

	Describe("a server with a directory", func() {
		BeforeEach(withDirectory)

		It("issues the credential after a lookup that finds nothing, and asks once per sender", func() {
			newAccount(nil)
			a := reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(looker.asked).To(ConsistOf(sender("app")))
			reconcile()
			reconcile()
			Expect(looker.calls()).To(Equal(1), "an admitted sender is not asked again")
		})

		It("marks a sender that belongs to the directory, and issues nothing", func() {
			looker.set(true, "")
			newAccount(nil)
			a := reconcile()
			notReady(a, ReasonSenderConflict)
			Expect(cond(a, conditionReady).Message).To(ContainSubstring("directory"))
			noCredential(appNS, accName)
			Expect(a.Status.AdmittedSender).To(BeEmpty())
		})

		It("withholds the credential of a new account while the directory cannot be asked", func() {
			looker.set(false, directory.Unreachable)
			newAccount(nil)
			res, a := reconcileIn(appNS, accName)
			notReady(a, ReasonDirectoryUnavailable)
			noCredential(appNS, accName)
			Expect(res.RequeueAfter).To(BeNumerically("<", 2*accountDirectoryRetry), "asked again soon")

			// It is issued as soon as the directory answers.
			looker.set(false, "")
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			_, err := secretIn(appNS, accName+"-credential")
			Expect(err).NotTo(HaveOccurred())
		})

		It("withholds a directory that is not usable like one that is down", func() {
			Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDirectory{ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: srvNS}})).To(Succeed())
			newAccount(nil)
			a := reconcile()
			notReady(a, ReasonDirectoryUnavailable)
			noCredential(appNS, accName)
		})

		It("keeps the credential of an existing account while the directory is down", func() {
			newAccount(nil)
			reconcile()
			first := credential()
			looker.set(false, directory.Unreachable)
			a := reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(credential().Data["password"]).To(Equal(first.Data["password"]))
			Expect(looker.calls()).To(Equal(1), "no lookup, so no failure to read")
		})

		It("does not admit a new sender while the directory is down, and keeps the old credential", func() {
			newAccount(nil)
			reconcile()
			first := credential()
			looker.set(false, directory.Unreachable)
			a := &cnmsv1alpha1.MailServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName}, a)).To(Succeed())
			a.Spec.Sender = sender("billing")
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			a = reconcile()
			notReady(a, ReasonDirectoryUnavailable)
			Expect(a.Status.AdmittedSender).To(Equal(sender("app")), "the server serves an account only while this equals spec.sender")
			Expect(a.Status.AdmittedSender).NotTo(Equal(a.Spec.Sender))
			Expect(credential().Data["password"]).To(Equal(first.Data["password"]), "what exists is not removed")

			looker.set(false, "")
			a = reconcile()
			Expect(cond(a, conditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(a.Status.AdmittedSender).To(Equal(sender("billing")))
		})

		It("marks a new sender that belongs to the directory and takes the admission back", func() {
			newAccount(nil)
			reconcile()
			looker.set(true, "")
			a := &cnmsv1alpha1.MailServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accName}, a)).To(Succeed())
			a.Spec.Sender = sender(apiUserName)
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			a = reconcile()
			notReady(a, ReasonSenderConflict)
			Expect(a.Status.AdmittedSender).To(BeEmpty())
		})

		It("does not ask the directory about an account the server refuses", func() {
			other := uniqueNamespace()
			createNamespaceG(other)
			newAccountIn(other, accName, nil)
			_, a := reconcileIn(other, accName)
			notReady(a, ReasonNamespaceNotAllowed)
			Expect(looker.calls()).To(BeZero())
		})
	})

	Describe("an account that is deleted", func() {
		It("is not an error, and leaves the objects to the owner reference", func() {
			a := newAccount(nil)
			reconcile()
			Expect(k8sClient.Delete(ctx, a)).To(Succeed())
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: appNS, Name: accName}})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
