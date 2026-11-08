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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

const (
	userCarol         = "carol"
	carolPasswordName = "carol-password"
)

// ADR 0015, section 4.7: over a cap or over the byte guard the new revision is
// not applied and the running one keeps serving; the live Dovecot files still
// follow the revocation-only rule. The caps are set small through
// MailServerReconciler.Limits (a zero field is the value of the ADR), so that
// the specs need not create a thousand objects.
// staleOnce shows a stale copy of the live Secret on its first read only:
// another writer changed the Secret afterwards.
type staleOnce struct {
	client.Reader
	name   string
	copy   *corev1.Secret
	served bool
}

func (s *staleOnce) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if sec, ok := obj.(*corev1.Secret); ok && key.Name == s.name && !s.served {
		s.served = true
		s.copy.DeepCopyInto(sec)
		return nil
	}
	return s.Reader.Get(ctx, key, obj, opts...)
}

var _ = Describe("MailServer limits", func() {
	var (
		ns         string
		domA, domB string
		refA, refB string
		r          *MailServerReconciler
		ur         *MailUserReconciler
		rec        *events.FakeRecorder
	)

	BeforeEach(func() {
		installCertificateCRD()
		ns = uniqueNamespace()
		createNamespaceG(ns)
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = nil
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		createTLSSecretG(ns)
		domA, domB = addGrantedDomainG(ns, testServerName), addGrantedDomainG(ns, testServerName)
		refA, refB = domainLabel(domA), domainLabel(domB)
		rec = events.NewFakeRecorder(100)
		r = newReconcilerG(AuthorizationAllowlist, true)
		r.Recorder = rec
		ur = &MailUserReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, DomainAuthorization: AuthorizationAllowlist,
		}
	})

	newUser := func(name string, mutate func(*cnmsv1alpha1.MailUser)) {
		u := &cnmsv1alpha1.MailUser{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: cnmsv1alpha1.MailUserSpec{
				ServerRef: cnmsv1alpha1.NameReference{Name: testServerName},
				DomainRef: cnmsv1alpha1.NameReference{Name: refA},
			},
		}
		if mutate != nil {
			mutate(u)
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		reconcileUser(ur, ns, name)
	}
	mutateUser := func(name string, fn func(*cnmsv1alpha1.MailUser)) {
		u := &cnmsv1alpha1.MailUser{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u)).To(Succeed())
		fn(u)
		Expect(k8sClient.Update(ctx, u)).To(Succeed())
		reconcileUser(ur, ns, name)
	}
	deleteUser := func(name string) {
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailUser{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})).To(Succeed())
	}
	live := func() *corev1.Secret {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: liveUsersName}, s)).To(Succeed())
		return s
	}
	passdb := func() string { return string(live().Data["passdb"]) }
	userdb := func() string { return string(live().Data["userdb"]) }
	table := func(file string) string {
		sts := getStatefulSetG(ns, postfixObjectName)
		for _, v := range sts.Spec.Template.Spec.Volumes {
			if v.Name == "config" && v.ConfigMap != nil {
				cm := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: v.ConfigMap.Name}, cm)).To(Succeed())
				return cm.Data[file]
			}
		}
		Fail("the Postfix StatefulSet mounts no ConfigMap")
		return ""
	}
	postfixHash := func() string { return podTemplateHash(getStatefulSetG(ns, postfixObjectName)) }
	ready := func() (string, string) {
		_, reason, message := serverConditionG(ns, testServerName, conditionReady)
		return reason, message
	}
	// restarted is a reconciler that has seen nothing: the state of a block is
	// in the cluster, not in the process.
	restarted := func() *MailServerReconciler {
		fresh := newReconcilerG(AuthorizationAllowlist, true)
		fresh.Recorder, fresh.Limits = rec, r.Limits
		return fresh
	}
	allEvents := func() string {
		var out []string
		for len(rec.Events) > 0 {
			out = append(out, <-rec.Events)
		}
		return strings.Join(out, "\n")
	}

	// fillToGuard creates accounts, half of them in each domain, until the live
	// Secret is the largest object of the revision (it grows by about 400 bytes
	// per account, the Postfix tables by about 100), rolls them out, and sets the
	// byte guard 150 bytes above the live Secret: one more account does not fit.
	fillToGuard := func() {
		for i := range 24 {
			domain := refA
			if i%2 == 1 {
				domain = refB
			}
			newUser(fmt.Sprintf("u%02d", i), func(m *cnmsv1alpha1.MailUser) { m.Spec.DomainRef.Name = domain })
		}
		deployG(r, ns, testServerName)
		others := 0
		for _, component := range dataPlaneComponents {
			for _, cm := range configMapsG(ns, component) {
				others = max(others, addresses.ObjectSize(cm.Data))
			}
			for _, s := range secretsG(ns, component) {
				if s.Name != liveUsersName {
					others = max(others, addresses.ObjectSize(s.Data))
				}
			}
		}
		r.Limits = addresses.Limits{ObjectBytes: addresses.ObjectSize(live().Data) + 150}
		Expect(others+200).To(BeNumerically("<", r.Limits.ObjectBytes), "the live Secret is the largest object")
	}

	It("holds back a revision over the cap of accounts, and still revokes", func() {
		r.Limits = addresses.Limits{Users: 3}
		source := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: carolPasswordName, Namespace: ns},
			Data:       map[string][]byte{keyPassword: []byte(userPW1)},
		}
		Expect(k8sClient.Create(ctx, source)).To(Succeed())
		newUser(apiUserName, func(m *cnmsv1alpha1.MailUser) { m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("team", refB)} })
		newUser(userBob, nil)
		newUser(userCarol, func(m *cnmsv1alpha1.MailUser) {
			m.Spec.PasswordSecretRef = &cnmsv1alpha1.SecretReference{Name: carolPasswordName}
		})
		deployG(r, ns, testServerName)
		reason, _ := ready()
		Expect(reason).NotTo(Equal(ReasonUserLimitExceeded), "three accounts are at the cap, not over it")
		running, version := postfixHash(), live().ResourceVersion
		oldCarol := string(credentialG(ns, userCarol).Data["passwordHash"])
		Expect(passdb()).To(ContainSubstring(oldCarol))
		allEvents()

		By("a fourth and a fifth account are held back")
		newUser("dave", nil)
		newUser("erin", nil)
		reconcileServerG(r, ns, testServerName)
		reason, message := ready()
		Expect(reason).To(Equal(ReasonUserLimitExceeded))
		Expect(message).To(And(ContainSubstring("5 accounts, at most 3"), ContainSubstring("2 account changes wait")))
		Expect(message).NotTo(ContainSubstring("@"), "counts, no address")
		Expect(postfixHash()).To(Equal(running), "the running revision keeps serving")
		Expect(jobsG(ns, testServerName, "")).To(HaveLen(4), "no validation Job for a revision that is not applied")
		for _, held := range []string{"dave", "erin"} {
			Expect(passdb()).NotTo(ContainSubstring(held), "no login is added")
			Expect(userdb()).NotTo(ContainSubstring(held), "no mailbox is added")
			Expect(table("vmailbox")).NotTo(ContainSubstring(held+"@"), "the Postfix maps are the running ones")
		}
		Expect(live().ResourceVersion).To(Equal(version), "nothing changed, nothing was written")
		Expect(allEvents()).To(And(ContainSubstring("UserLimitExceeded"), ContainSubstring("Warning")), "an Event on the transition")
		reconcileServerG(r, ns, testServerName)
		Expect(allEvents()).To(BeEmpty(), "no Event while the block stays the same")

		By("a new password applies, also in a process that has seen nothing before")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: carolPasswordName}, source)).To(Succeed())
		source.Data[keyPassword] = []byte(userPW2)
		Expect(k8sClient.Update(ctx, source)).To(Succeed())
		reconcileUser(ur, ns, userCarol)
		newCarol := string(credentialG(ns, userCarol).Data["passwordHash"])
		Expect(newCarol).NotTo(Equal(oldCarol))
		userdbBefore := userdb()
		reconcileServerG(restarted(), ns, testServerName)
		Expect(passdb()).To(And(ContainSubstring(newCarol), Not(ContainSubstring(oldCarol))))
		Expect(userdb()).To(Equal(userdbBefore), "the userdb is not touched")
		Expect(postfixHash()).To(Equal(running))

		By("a disabled account loses its login and keeps its mailbox")
		mutateUser(userBob, func(m *cnmsv1alpha1.MailUser) { m.Spec.Disabled = new(true) })
		reconcileServerG(restarted(), ns, testServerName)
		Expect(passdb()).NotTo(ContainSubstring("bob"))
		Expect(userdb()).To(ContainSubstring("bob@" + domA))
		Expect(table("vmailbox")).To(ContainSubstring("bob@"+domA), "Postfix still accepts the mail, Dovecot still delivers it")
		Expect(passdb()).To(And(ContainSubstring("\nalice:"), ContainSubstring("\ncarol:")), "the other logins stay")

		By("a removed account loses its login, and its mailbox stays until the block clears")
		deleteUser(userCarol)
		reconcileServerG(restarted(), ns, testServerName)
		reason, _ = ready()
		Expect(reason).To(Equal(ReasonUserLimitExceeded), "four accounts are over the cap of three")
		Expect(passdb()).NotTo(ContainSubstring("carol"))
		Expect(userdb()).To(ContainSubstring("carol@" + domA))
		Expect(table("vmailbox")).To(ContainSubstring("carol@" + domA))

		By("the block clears when the count falls to the cap, and the held changes are applied")
		deleteUser("dave")
		deployG(restarted(), ns, testServerName)
		reason, _ = ready()
		Expect(reason).NotTo(Equal(ReasonUserLimitExceeded))
		Expect(postfixHash()).NotTo(Equal(running), "the held revision is rolled out")
		Expect(table("vmailbox")).To(And(ContainSubstring("erin@"+domA), Not(ContainSubstring("carol@")), Not(ContainSubstring("dave@"))))
		Expect(userdb()).To(And(ContainSubstring("erin@"+domA), Not(ContainSubstring("carol@"))))
		Expect(passdb()).To(And(ContainSubstring("\nerin:"), ContainSubstring("\nalice:")))
		Expect(passdb()).NotTo(ContainSubstring("bob"), "a disabled account stays without a login")
	})

	It("holds back a revision over the cap of addresses", func() {
		r.Limits = addresses.Limits{Addresses: 5}
		newUser(apiUserName, func(m *cnmsv1alpha1.MailUser) {
			m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("one", refB), aliasOn("two", refB)}
		})
		newUser(userBob, nil)
		newUser(userCarol, nil)
		deployG(r, ns, testServerName)
		reason, _ := ready()
		Expect(reason).NotTo(Equal(ReasonAddressLimitExceeded), "five addresses are at the cap")
		running := postfixHash()

		mutateUser(userCarol, func(m *cnmsv1alpha1.MailUser) {
			m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("three", refB)}
		})
		reconcileServerG(r, ns, testServerName)
		reason, message := ready()
		Expect(reason).To(Equal(ReasonAddressLimitExceeded))
		Expect(message).To(ContainSubstring("6 addresses, at most 5"))
		Expect(postfixHash()).To(Equal(running))
		Expect(table("valias")).NotTo(ContainSubstring("three@"), "the new alias waits")

		mutateUser(userCarol, func(m *cnmsv1alpha1.MailUser) { m.Spec.Aliases = nil })
		deployG(r, ns, testServerName)
		reason, _ = ready()
		Expect(reason).NotTo(Equal(ReasonAddressLimitExceeded), "the block clears")
	})

	It("refuses an object over the byte guard before it is sent, and still revokes", func() {
		fillToGuard()

		// The next accounts do not fit.
		last := "u25"
		newUser("u24", nil)
		newUser(last, nil)
		running, version := postfixHash(), live().ResourceVersion
		deployG(r, ns, testServerName)
		reason, message := ready()
		Expect(reason).To(Equal(ReasonConfigTooLarge))
		Expect(message).To(And(ContainSubstring(liveUsersName), ContainSubstring("must stay under")), "the object is named")
		Expect(message).NotTo(ContainSubstring("@"))
		Expect(postfixHash()).To(Equal(running), "the running revision keeps serving")
		Expect(addresses.ObjectSize(live().Data)).To(BeNumerically("<", r.Limits.ObjectBytes), "an oversized live Secret was never written")
		Expect(live().ResourceVersion).To(Equal(version))
		Expect(passdb()).NotTo(ContainSubstring("\n" + last + ":"))
		Expect(passdb()).NotTo(ContainSubstring("\nu24:"))

		By("a removed account loses its login")
		deleteUser("u00")
		reconcileServerG(restarted(), ns, testServerName)
		reason, _ = ready()
		Expect(reason).To(Equal(ReasonConfigTooLarge), "one account less leaves 25, which still do not fit")
		Expect(passdb()).NotTo(ContainSubstring("\nu00:"))
		Expect(userdb()).To(ContainSubstring("u00@" + domA))

		By("the block clears when the revision fits again")
		r.Limits = addresses.Limits{}
		deployG(r, ns, testServerName)
		reason, _ = ready()
		Expect(reason).NotTo(Equal(ReasonConfigTooLarge))
		Expect(passdb()).To(ContainSubstring("\n" + last + ":"))
	})

	// A write that was computed from one read of the live Secret must not
	// overwrite what another writer applied since (a second replica during a
	// handover): the settled files are not written, and the pass is repeated.
	It("does not write settled files that were computed from a stale read (count block)", func() {
		r.Limits = addresses.Limits{Users: 3}
		newUser(apiUserName, nil)
		newUser(userBob, nil)
		newUser(userCarol, nil)
		deployG(r, ns, testServerName)
		newUser("dave", nil)
		reconcileServerG(r, ns, testServerName)
		reasonBefore, messageBefore := ready()
		Expect(reasonBefore).To(Equal(ReasonUserLimitExceeded))
		allEvents()

		// The reconciler reads the Secret as it was; another writer changes it
		// afterwards, and the settled files would drop the login of bob.
		stale := live().DeepCopy()
		other := live()
		other.Annotations = map[string]string{touchedKey: touchedValue}
		Expect(k8sClient.Update(ctx, other)).To(Succeed())
		mutateUser(userBob, func(m *cnmsv1alpha1.MailUser) { m.Spec.Disabled = new(true) })
		version := live().ResourceVersion

		raced := newReconcilerG(AuthorizationAllowlist, true)
		raced.Recorder, raced.Limits = rec, r.Limits
		raced.APIReader = &staleOnce{Reader: k8sClient, name: liveUsersName, copy: stale}
		res, err := raced.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: testServerName}})
		Expect(err).NotTo(HaveOccurred(), "a lost race is not an error")
		Expect(res.RequeueAfter).To(Equal(liveUsersRetry))
		reason, message := ready()
		Expect(reason).To(Equal(reasonBefore), "Ready is not touched")
		Expect(message).To(Equal(messageBefore))
		Expect(live().ResourceVersion).To(Equal(version), "nothing was written")
		Expect(passdb()).To(ContainSubstring("\nbob:"), "the settled files were not applied")
		Expect(allEvents()).To(BeEmpty(), "no ApplyFailed, no limit Event")

		reconcileServerG(r, ns, testServerName)
		Expect(passdb()).NotTo(ContainSubstring("bob"), "the next pass reads the Secret and revokes")
	})

	It("does not write settled files that were computed from a stale read (byte guard)", func() {
		fillToGuard()
		newUser("u24", nil)
		newUser("u25", nil)
		deployG(r, ns, testServerName)
		reasonBefore, messageBefore := ready()
		Expect(reasonBefore).To(Equal(ReasonConfigTooLarge))
		allEvents()

		stale := live().DeepCopy()
		other := live()
		other.Annotations = map[string]string{touchedKey: touchedValue}
		Expect(k8sClient.Update(ctx, other)).To(Succeed())
		mutateUser("u00", func(m *cnmsv1alpha1.MailUser) { m.Spec.Disabled = new(true) })
		version := live().ResourceVersion

		raced := newReconcilerG(AuthorizationAllowlist, true)
		raced.Recorder, raced.Limits = rec, r.Limits
		raced.APIReader = &staleOnce{Reader: k8sClient, name: liveUsersName, copy: stale}
		res, err := raced.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: testServerName}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(liveUsersRetry))
		reason, message := ready()
		Expect(reason).To(Equal(reasonBefore))
		Expect(message).To(Equal(messageBefore))
		Expect(live().ResourceVersion).To(Equal(version))
		Expect(passdb()).To(ContainSubstring("\nu00:"))
		Expect(allEvents()).To(BeEmpty())
	})

	// The revocation of a domain is not a blocked revision (ADR 0015, section 4.7):
	// the fail-closed render has no domains and no users, and replaces the
	// running configuration whatever limit the new revision hits.
	It("fails closed for a revoked domain although the count blocks the revision", func() {
		r.Limits = addresses.Limits{Users: 3}
		newUser(apiUserName, nil)
		newUser(userBob, func(m *cnmsv1alpha1.MailUser) { m.Spec.DomainRef.Name = refB })
		newUser(userCarol, nil)
		deployG(r, ns, testServerName)
		newUser("dave", nil)
		reconcileServerG(r, ns, testServerName)
		reason, _ := ready()
		Expect(reason).To(Equal(ReasonUserLimitExceeded))
		Expect(passdb()).To(ContainSubstring("\nbob:"))

		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domB}})).To(Succeed())
		reconcileServerG(r, ns, testServerName)
		for _, content := range []string{passdb(), userdb()} {
			for _, name := range []string{"alice", "bob", "carol"} {
				Expect(content).NotTo(ContainSubstring(name), "the fail-closed render has no users")
			}
		}
		deployG(r, ns, testServerName)
		cf := postfixMainCFG(ns)
		Expect(cf).NotTo(ContainSubstring(domA), "Postfix runs the fail-closed revision")
		Expect(cf).NotTo(ContainSubstring(domB))
		reason, _ = ready()
		Expect(reason).To(Equal(ReasonUserLimitExceeded), "the block is still reported")
	})

	It("fails closed for a revoked domain although the byte guard blocks the revision", func() {
		fillToGuard()
		// Accounts of the domain that stays, so many that the revision is still too
		// large when the accounts of the other domain are suspended (their passdb
		// lines go, their userdb lines stay).
		for i := 24; i < 34; i++ {
			newUser(fmt.Sprintf("u%02d", i), nil)
		}
		deployG(r, ns, testServerName)
		reason, _ := ready()
		Expect(reason).To(Equal(ReasonConfigTooLarge))
		Expect(passdb()).To(And(ContainSubstring("\nu00:"), ContainSubstring("\nu01:")))

		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domB}})).To(Succeed())
		deployG(r, ns, testServerName)
		cf := postfixMainCFG(ns)
		Expect(cf).NotTo(ContainSubstring(domA), "Postfix runs the fail-closed revision")
		Expect(cf).NotTo(ContainSubstring(domB))
		reason, _ = ready()
		Expect(reason).To(Equal(ReasonConfigTooLarge))
		// The accounts of the revoked domain (odd) are suspended: their logins
		// are gone; those of the domain that stays keep theirs.
		Expect(passdb()).To(ContainSubstring("\nu00:"))
		Expect(passdb()).NotTo(ContainSubstring("\nu01:"))
		Expect(passdb()).NotTo(ContainSubstring("\nu23:"))
		Expect(passdb()).To(ContainSubstring("\nu22:"))
	})

	It("keeps conditions and Events free of addresses and hashes", func() {
		r.Limits = addresses.Limits{Users: 1}
		newUser(apiUserName, nil)
		newUser(userBob, nil)
		reconcileServerG(r, ns, testServerName)
		reason, message := ready()
		Expect(reason).To(Equal(ReasonUserLimitExceeded))
		Expect(message).To(ContainSubstring("nothing is applied until it is fixed"), "no data plane runs yet")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: liveUsersName}, &corev1.Secret{})).NotTo(Succeed(), "no live Secret is made for a blocked first revision")
		seen := allEvents() + message
		Expect(seen).To(ContainSubstring(ReasonUserLimitExceeded))
		for _, leak := range []string{"@", "ARGON2ID", domA, domB, string(credentialG(ns, apiUserName).Data["passwordHash"])} {
			Expect(seen).NotTo(ContainSubstring(leak))
		}
	})
})

// ADR 0015, section 4.7: the byte guard of 1,000,000 bytes is a margin below the
// limit of the API server. Whether the API counts the keys is asked of it
// here: it does not, so counting them is the conservative reading.
var _ = Describe("the size limit of the API server", func() {
	var ns string
	BeforeEach(func() {
		ns = uniqueNamespace()
		createNamespaceG(ns)
	})

	secretOf := func(name string, valueBytes int) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data: map[string][]byte{"k": make([]byte, valueBytes)}}
	}

	It("accepts a Secret just under the guard, and refuses one over 1 MiB of values", func() {
		Expect(addresses.ObjectSize(secretOf("x", addresses.MaxObjectBytes-2).Data)).To(Equal(addresses.MaxObjectBytes - 1))
		Expect(k8sClient.Create(ctx, secretOf("under", addresses.MaxObjectBytes-2))).To(Succeed(), "999,999 bytes with the key")

		const mib = 1 << 20
		Expect(k8sClient.Create(ctx, secretOf("limit", mib))).To(Succeed(), "1 MiB of values")
		err := k8sClient.Create(ctx, secretOf("over", mib+1))
		Expect(apierrors.IsInvalid(err) || apierrors.IsRequestEntityTooLargeError(err)).To(BeTrue(), "1 MiB and one byte of values: %v", err)
		Expect(fmt.Sprint(err)).To(ContainSubstring("1048576"), "the limit named by the API server")
		AddReportEntry("the API server refuses 1 MiB and one byte of values", fmt.Sprint(err))
	})

	It("limits a ConfigMap the same way", func() {
		const mib = 1 << 20
		cm := func(name string, valueBytes int) *corev1.ConfigMap {
			return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Data: map[string]string{"k": strings.Repeat("x", valueBytes)}}
		}
		Expect(k8sClient.Create(ctx, cm("limit", mib))).To(Succeed())
		err := k8sClient.Create(ctx, cm("over", mib+1))
		Expect(apierrors.IsInvalid(err) || apierrors.IsRequestEntityTooLargeError(err)).To(BeTrue(), "%v", err)
	})
})
