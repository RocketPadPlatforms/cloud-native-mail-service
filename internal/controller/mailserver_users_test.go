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
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

const (
	userBob       = "bob"
	liveUsersName = "mail-dovecot-users"
)

// ADR 0015, sections 3, 4.3, 4.4, 4.6 and 4.7: the MailServer reads the
// MailUsers and their credential hashes, renders the Postfix maps behind the
// validation gate, and writes the Dovecot passdb and userdb into one live
// Secret outside the config hash.
var _ = Describe("MailServer with MailUsers", func() {
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

	newUser := func(name, domainRef string, mutate func(*cnmsv1alpha1.MailUser)) *cnmsv1alpha1.MailUser {
		u := &cnmsv1alpha1.MailUser{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: cnmsv1alpha1.MailUserSpec{
				ServerRef: cnmsv1alpha1.NameReference{Name: testServerName},
				DomainRef: cnmsv1alpha1.NameReference{Name: domainRef},
			},
		}
		if mutate != nil {
			mutate(u)
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		reconcileUser(ur, ns, name)
		return u
	}
	reconcileServer := func() { reconcileServerG(r, ns, testServerName) }
	deploy := func() { deployG(r, ns, testServerName) }
	mutateUser := func(name string, fn func(*cnmsv1alpha1.MailUser)) {
		u := &cnmsv1alpha1.MailUser{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u)).To(Succeed())
		fn(u)
		Expect(k8sClient.Update(ctx, u)).To(Succeed())
		reconcileUser(ur, ns, name)
	}
	live := func() *corev1.Secret {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: liveUsersName}, s)).To(Succeed())
		return s
	}
	passdb := func() string { return string(live().Data["passdb"]) }
	userdb := func() string { return string(live().Data["userdb"]) }
	// table is a file of the Postfix ConfigMap that the running StatefulSet mounts.
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
	dovecotHash := func() string { return podTemplateHash(getStatefulSetG(ns, "mail-dovecot")) }
	postfixHash := func() string { return podTemplateHash(getStatefulSetG(ns, postfixObjectName)) }
	line := func(content, prefix string) bool {
		return strings.Contains("\n"+content, "\n"+prefix)
	}
	alice := func(m *cnmsv1alpha1.MailUser) {
		m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("alice", refB), aliasOn(userSales, refB)}
	}

	It("renders the accounts into the Postfix maps and the live Dovecot Secret", func() {
		newUser(apiUserName, refA, alice)
		deploy()

		Expect(table("vmailbox")).To(ContainSubstring("alice@" + domA + " OK"))
		Expect(table("valias")).To(And(
			ContainSubstring("alice@"+domB+" alice@"+domA), ContainSubstring("sales@"+domB+" alice@"+domA)))
		// Every owned address maps to the account name.
		senders := table("sender_login")
		Expect(senders).To(And(
			ContainSubstring("alice@"+domA+" alice"), ContainSubstring("alice@"+domB+" alice"), ContainSubstring("sales@"+domB+" alice")))

		s := live()
		Expect(metav1.IsControlledBy(s, getServerG(ns))).To(BeTrue(), "the server owns the live Secret")
		Expect(s.Immutable).To(BeNil(), "the live Secret is updated in place")
		Expect(s.Name).To(Equal(manifests.LiveUsersSecretName(getServerG(ns))))
		hash := string(credentialG(ns, apiUserName).Data["passwordHash"])
		Expect(hash).To(HavePrefix("{ARGON2ID}"))
		Expect(passdb()).To(And(
			ContainSubstring("\nalice@"+domA+":"+hash+"::::::user=alice\n"),
			ContainSubstring("\nalice:"+hash+"::::::user=alice\n")))
		Expect(passdb()).NotTo(ContainSubstring("sales@"), "an alias is no login")
		Expect(line(userdb(), "alice@"+domA+"::1000:1000::/srv/mail/alice:")).To(BeTrue())
		Expect(line(userdb(), "alice::1000:1000::/srv/mail/alice:")).To(BeTrue())

		// The pod mounts the object by name, as a whole directory.
		var mounted bool
		for _, v := range getStatefulSetG(ns, "mail-dovecot").Spec.Template.Spec.Volumes {
			mounted = mounted || (v.Name == "users" && v.Secret != nil && v.Secret.SecretName == liveUsersName)
		}
		Expect(mounted).To(BeTrue())

		// The hash is in no ConfigMap, no hash-named Secret and no Event.
		for _, component := range dataPlaneComponents {
			for _, cm := range configMapsG(ns, component) {
				for k, v := range cm.Data {
					Expect(v).NotTo(ContainSubstring(hash), "%s/%s", cm.Name, k)
				}
			}
			for _, sec := range secretsG(ns, component) {
				if sec.Name == liveUsersName {
					continue
				}
				for k, v := range sec.Data {
					Expect(string(v)).NotTo(ContainSubstring(hash), "%s/%s", sec.Name, k)
				}
			}
		}
		for len(rec.Events) > 0 {
			Expect(<-rec.Events).NotTo(ContainSubstring("ARGON2ID"))
		}
		// The accounts need no hash key: the live Secret is outside the hash.
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: manifests.HashKeySecretName(getServerG(ns))}, &corev1.Secret{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no hash key for a server with MailUsers only")
		status, reason, _ := serverConditionG(ns, testServerName, ConditionAddressesServed)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAllServed))
	})

	It("adds, changes and removes accounts with no new Dovecot revision", func() {
		source := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: bobPasswordName, Namespace: ns},
			Data:       map[string][]byte{keyPassword: []byte(userPW1)},
		}
		Expect(k8sClient.Create(ctx, source)).To(Succeed())
		newUser(apiUserName, refA, alice)
		deploy()
		dovecot, postfix := dovecotHash(), postfixHash()
		version := live().ResourceVersion
		jobs := len(jobsG(ns, testServerName, ""))

		// Add: the Dovecot Secret changes in this reconcile, the Postfix
		// revision waits for its validation Job.
		newUser(userBob, refA, func(m *cnmsv1alpha1.MailUser) {
			m.Spec.PasswordSecretRef = &cnmsv1alpha1.SecretReference{Name: bobPasswordName}
		})
		reconcileServer()
		Expect(passdb()).To(ContainSubstring("\nbob@" + domA + ":"))
		Expect(live().ResourceVersion).NotTo(Equal(version), "same Secret, new resourceVersion")
		Expect(live().Name).To(Equal(liveUsersName))
		Expect(postfixHash()).To(Equal(postfix), "the Postfix maps wait for the validation Job")
		Expect(table("vmailbox")).NotTo(ContainSubstring("bob@"))
		finishJobsG(ns, testServerName, true)
		reconcileServer()
		Expect(table("vmailbox")).To(ContainSubstring("bob@" + domA))
		Expect(postfixHash()).NotTo(Equal(postfix))
		Expect(dovecotHash()).To(Equal(dovecot), "a new user is no Dovecot revision")
		Expect(jobsG(ns, testServerName, "")).To(HaveLen(jobs+1), "one Job for the Postfix revision, none for Dovecot")

		// Change the password: the passdb changes, nothing else does.
		postfix, version, jobs = postfixHash(), live().ResourceVersion, len(jobsG(ns, testServerName, ""))
		before := passdb()
		src := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: bobPasswordName}, src)).To(Succeed())
		src.Data[keyPassword] = []byte(userPW2)
		Expect(k8sClient.Update(ctx, src)).To(Succeed())
		reconcileUser(ur, ns, userBob)
		reconcileServer()
		Expect(passdb()).NotTo(Equal(before))
		Expect(live().ResourceVersion).NotTo(Equal(version))
		reconcileServer()
		Expect(postfixHash()).To(Equal(postfix))
		Expect(dovecotHash()).To(Equal(dovecot))
		Expect(jobsG(ns, testServerName, "")).To(HaveLen(jobs), "a new password is no revision")

		// Remove: the login and the userdb line go at once; the Postfix
		// maps follow with the validated revision.
		version = live().ResourceVersion
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailUser{ObjectMeta: metav1.ObjectMeta{Name: userBob, Namespace: ns}})).To(Succeed())
		reconcileServer()
		Expect(passdb()).NotTo(ContainSubstring("bob"))
		Expect(userdb()).NotTo(ContainSubstring("bob"))
		Expect(live().ResourceVersion).NotTo(Equal(version))
		finishJobsG(ns, testServerName, true)
		reconcileServer()
		Expect(table("vmailbox")).NotTo(ContainSubstring("bob@"))
		Expect(dovecotHash()).To(Equal(dovecot))
	})

	It("keeps a disabled account in the userdb and out of the passdb", func() {
		newUser(apiUserName, refA, alice)
		newUser(userBob, refA, nil)
		deploy()
		Expect(passdb()).To(ContainSubstring("\nbob:"))

		mutateUser(userBob, func(m *cnmsv1alpha1.MailUser) { m.Spec.Disabled = new(true) })
		deploy()
		Expect(passdb()).NotTo(ContainSubstring("bob"), "no login")
		Expect(line(userdb(), "bob@"+domA+"::1000:1000::/srv/mail/bob:")).To(BeTrue(), "still receives")
		Expect(table("vmailbox")).To(ContainSubstring("bob@" + domA + " OK"))
		Expect(table("sender_login")).NotTo(ContainSubstring(" bob\n"), "no sending")
		Expect(passdb()).To(ContainSubstring("\nalice:"), "the others are untouched")
	})

	It("suspends an account whose primary domain is not served by the server", func() {
		other := uniqueDomain()
		createDomainG(ns, other, "another-server")
		createGrantG(other, ns, false)
		newUser("carol", domainLabel(other), func(m *cnmsv1alpha1.MailUser) {
			m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("carol", refB)}
		})
		newUser(apiUserName, refA, nil)
		deploy()

		Expect(passdb()).NotTo(ContainSubstring("carol"), "suspended: no login")
		Expect(userdb()).To(ContainSubstring("carol@"+other+"::1000:1000::/srv/mail/carol:"), "suspended: no bounce for accepted mail")
		Expect(table("vmailbox")).NotTo(ContainSubstring("carol@"))
		Expect(table("valias")).NotTo(ContainSubstring("carol@"), "its alias would be rewritten to a domain that is not served")
		Expect(table("recipient_defer")).To(ContainSubstring("carol@"+domB), "deferred, not refused")
		Expect(table("sender_login")).NotTo(ContainSubstring("carol"))
	})

	It("defers an address that two accounts claim and serves neither", func() {
		newUser(apiUserName, refA, func(m *cnmsv1alpha1.MailUser) { m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("team", refB)} })
		newUser(userBob, refA, func(m *cnmsv1alpha1.MailUser) { m.Spec.Aliases = []cnmsv1alpha1.MailUserAlias{aliasOn("team", refB)} })
		deploy()

		Expect(table("recipient_defer")).To(ContainSubstring("team@" + domB))
		Expect(table("valias")).NotTo(ContainSubstring("team@"))
		Expect(table("sender_login")).NotTo(ContainSubstring("team@"))
		status, reason, msg := serverConditionG(ns, testServerName, ConditionAddressesServed)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonPartiallyServed))
		Expect(msg).To(ContainSubstring("team@" + domB + " (AddressConflict)"))
		// A warning only: it never feeds into Ready.
		_, readyReason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(readyReason).NotTo(Equal(ReasonPartiallyServed))
	})

	It("skips the login of an account without a credential and serves the others", func() {
		newUser(apiUserName, refA, alice)
		newUser(userBob, refA, nil)
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alice-credential", Namespace: ns}})).To(Succeed())
		deploy()

		Expect(passdb()).NotTo(ContainSubstring("alice"))
		Expect(passdb()).To(ContainSubstring("\nbob:"))
		Expect(userdb()).To(ContainSubstring("alice@"+domA), "mail is still delivered")
		Expect(table("vmailbox")).To(ContainSubstring("alice@" + domA + " OK"))
	})

	It("does not render a hash it did not write", func() {
		newUser(apiUserName, refA, nil)
		newUser(userBob, refA, nil)
		for name, hash := range map[string]string{
			apiUserName: "{PLAIN}secret",                                      // not the scheme and the parameters of the operator
			userBob:     "{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$x$y\nroot", // a line of its own
		} {
			s := credentialG(ns, name)
			s.Data["passwordHash"] = []byte(hash)
			Expect(k8sClient.Update(ctx, s)).To(Succeed())
		}
		deploy()
		Expect(passdb()).NotTo(ContainSubstring("alice:"))
		Expect(passdb()).NotTo(ContainSubstring("root"))
		Expect(passdb()).NotTo(ContainSubstring("bob:"))
		Expect(userdb()).To(ContainSubstring("\nalice:"))
	})

	It("refuses a second user source and keeps the running revision", func() {
		newUser(apiUserName, refA, nil)
		deploy()
		postfix, version := postfixHash(), live().ResourceVersion

		createDirectoryG(ns)
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = &cnmsv1alpha1.NameReference{Name: directoryName}
		})
		reconcileServer()
		status, reason, msg := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonUserSourceConflict))
		Expect(msg).To(ContainSubstring("MailUsers"))
		Expect(postfixHash()).To(Equal(postfix))
		Expect(live().ResourceVersion).To(Equal(version), "the running revision is not touched")
		Expect(passdb()).To(ContainSubstring("\nalice:"))

		// Static test users are a source of their own.
		createUsersSecretG(ns, "users: []\n")
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = nil
			s.Spec.Submission = &cnmsv1alpha1.Submission{InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName}}
		})
		reconcileServer()
		_, reason, msg = serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(ReasonUserSourceConflict))
		Expect(msg).To(And(ContainSubstring("MailUsers"), ContainSubstring("insecureStaticUsers")))
	})

	It("takes the logins away when the last account is deleted", func() {
		newUser(apiUserName, refA, nil)
		deploy()
		Expect(passdb()).To(ContainSubstring("\nalice:"))
		postfix := postfixHash()

		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailUser{ObjectMeta: metav1.ObjectMeta{Name: apiUserName, Namespace: ns}})).To(Succeed())
		reconcileServer()
		status, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoUserSource))
		Expect(passdb()).NotTo(ContainSubstring("alice"))
		Expect(userdb()).NotTo(ContainSubstring("alice"))
		Expect(postfixHash()).To(Equal(postfix), "the running revision stays")
		// A server with no data plane gets none from this path.
		Expect(live().Name).To(Equal(liveUsersName))
	})

	It("keeps the logins of the other domains when one domain is revoked", func() {
		newUser(apiUserName, refA, alice)
		newUser(userBob, refA, nil)
		deploy()
		Expect(passdb()).To(ContainSubstring("\nalice:"))

		// The alias domain loses its grant. The running revision still serves
		// it, so the fail-closed render of ADR 0003 is applied to Postfix
		// while the narrowed revision validates. The accounts of the domain
		// that is still authorized keep their logins and their mailboxes: the
		// live Secret is written from the narrowed render only (ADR 0015,
		// section 4.6: per address, not per account).
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domB}})).To(Succeed())
		reconcileServer()
		for _, name := range []string{"alice", userBob} {
			Expect(passdb()).To(ContainSubstring("\n"+name+":"), name)
			Expect(userdb()).To(ContainSubstring("\n"+name+"::1000:1000"), name)
		}

		// The revision without the domain passes its Jobs: the alias is gone.
		deploy()
		Expect(passdb()).To(ContainSubstring("\nalice:"))
		Expect(passdb()).To(ContainSubstring("\nbob:"))
		Expect(table("valias")).NotTo(ContainSubstring("@" + domB))
		Expect(table("sender_login")).NotTo(ContainSubstring(domB))
		Expect(table("vmailbox")).To(ContainSubstring("alice@" + domA))
	})

	It("empties the live files when no authorized domain is left", func() {
		newUser(apiUserName, refA, alice)
		newUser(userBob, refA, nil)
		deploy()
		Expect(passdb()).To(ContainSubstring("\nalice:"))

		// No domain: the fail-closed render replaces the running one (ADR
		// 0003, 0015 section 4.6), users included.
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domA}})).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: domB}})).To(Succeed())
		reconcileServer()
		for _, content := range []string{passdb(), userdb()} {
			Expect(content).NotTo(ContainSubstring("alice"))
			Expect(content).NotTo(ContainSubstring("bob"))
		}
	})

	It("reads again and leaves Ready alone when somebody wrote the live Secret first", func() {
		newUser(apiUserName, refA, nil)
		deploy()
		stale := live().DeepCopy()
		// Another writer moves the resourceVersion on.
		s := live()
		s.Annotations = map[string]string{touchedKey: touchedValue}
		Expect(k8sClient.Update(ctx, s)).To(Succeed())
		// The operator reads the old copy, and its desired content differs.
		newUser(userBob, refA, nil)
		_, reasonBefore, messageBefore := serverConditionG(ns, testServerName, conditionReady)

		raced := newReconcilerG(AuthorizationAllowlist, true)
		raced.APIReader = staleReader{Reader: k8sClient, name: liveUsersName, copy: stale}
		res, err := raced.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: testServerName}})
		Expect(err).NotTo(HaveOccurred(), "a lost race is not an error")
		Expect(res.RequeueAfter).To(Equal(liveUsersRetry))
		_, reason, message := serverConditionG(ns, testServerName, conditionReady)
		Expect(reason).To(Equal(reasonBefore), "Ready is not touched")
		Expect(message).To(Equal(messageBefore))
		Expect(passdb()).NotTo(ContainSubstring("\nbob:"), "nothing was overwritten")

		// The next pass reads the real Secret and writes.
		reconcileServer()
		Expect(passdb()).To(ContainSubstring("\nbob:"))
	})

	It("removes the revisioned users Secret of the earlier scheme and keeps the live one", func() {
		newUser(apiUserName, refA, nil)
		deploy()
		old := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mail-dovecot-0badc0de", Namespace: ns,
				Labels: map[string]string{
					manifests.InstanceLabelKey: testServerName, manifests.ManagedByLabel: manifests.ManagedByValue,
					manifests.ServerLabel: testServerName, "app.kubernetes.io/component": "dovecot",
				},
			},
			Data: map[string][]byte{"passdb": []byte("old")},
		}
		Expect(ctrl.SetControllerReference(getServerG(ns), old, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Create(ctx, old)).To(Succeed())

		convergeWorkloadsG(ns)
		reconcileServer()
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(old), &corev1.Secret{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the superseded Secret is cleaned up")
		live()
	})

	It("reaches the server when a changed password moves the credential revision", func() {
		src := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: userSecret, Namespace: ns},
			Data:       map[string][]byte{keyPassword: []byte(userPW1)},
		}
		Expect(k8sClient.Create(ctx, src)).To(Succeed())
		newUser(apiUserName, refA, func(m *cnmsv1alpha1.MailUser) {
			m.Spec.PasswordSecretRef = &cnmsv1alpha1.SecretReference{Name: userSecret}
		})

		startManagerG(mailServerSetup(AuthorizationAllowlist))
		// The server is not ready and re-checks every 30s: only the watch
		// on the MailUser (status writes included) can be this fast.
		Eventually(func() string { return secretText(ns, liveUsersName, "passdb") }, "20s", "200ms").
			Should(ContainSubstring("\nalice:{ARGON2ID}"))
		before := secretText(ns, liveUsersName, "passdb")

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(src), src)).To(Succeed())
		src.Data[keyPassword] = []byte(userPW2)
		Expect(k8sClient.Update(ctx, src)).To(Succeed())
		// The MailUser controller writes status.credentialRevision.
		reconcileUser(ur, ns, apiUserName)
		Eventually(func() string { return secretText(ns, liveUsersName, "passdb") }, "20s", "200ms").
			ShouldNot(Equal(before))
	})
})

func credentialG(ns, user string) *corev1.Secret {
	s := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: credentialSecretName(user)}, s)).To(Succeed())
	return s
}

// secretText is one key of a Secret, or "" while the Secret is missing.
func secretText(ns, name, key string) string {
	s := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, s); err != nil {
		return ""
	}
	return string(s.Data[key])
}

func reconcileUser(r *MailUserReconciler, ns, name string) {
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	Expect(err).NotTo(HaveOccurred())
}

// Values several specs share.
const (
	dovecotComponent = "dovecot"
	bobPasswordName  = "bob-password"
	exampleBaseDN    = "dc=example,dc=org"
)

// dataPlaneComponents are the components whose objects the specs search.
var dataPlaneComponents = []string{"postfix", dovecotComponent, "rspamd", "valkey"}
