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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// ADR 0015, section 5: the MailServer controller reads the MailDirectory
// and its Secrets, renders the Postfix and Dovecot maps, and copies the
// secret values into hash-named Secrets of the revision.
var _ = Describe("MailServer with a MailDirectory", func() {
	const (
		bindName     = "directory-bind"
		caName       = "directory-ca"
		bindPassword = "bind-secret-value-1"
		bindDN       = "cn=lookup,dc=example,dc=org"
	)
	var (
		ns, domain string
		r          *MailServerReconciler
	)

	secret := func(name string, data map[string][]byte) {
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: data})).To(Succeed())
	}
	setPassword := func(pw string) {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bindName, Namespace: ns}, s)).To(Succeed())
		s.Data = map[string][]byte{dirKeyPassword: []byte(pw)}
		Expect(k8sClient.Update(ctx, s)).To(Succeed())
	}
	updateDirectory := func(mutate func(*cnmsv1alpha1.LDAP)) {
		d := &cnmsv1alpha1.MailDirectory{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: directoryName, Namespace: ns}, d)).To(Succeed())
		mutate(d.Spec.LDAP)
		Expect(k8sClient.Update(ctx, d)).To(Succeed())
	}
	// withCredentials makes the directory bind with a password and verify
	// the server with a CA, and gives it an alias attribute.
	withCredentials := func(l *cnmsv1alpha1.LDAP) {
		l.BindDN = new(bindDN)
		l.BindSecretRef = &cnmsv1alpha1.SecretReference{Name: bindName}
		l.TLS = &cnmsv1alpha1.LDAPTLS{CASecretRef: &cnmsv1alpha1.SecretReference{Name: caName}}
		l.Attributes = &cnmsv1alpha1.LDAPAttributes{Aliases: new("mailAlias")}
	}
	// sts and the data of the Secret it mounts under the given volume.
	mountedSecret := func(component, volume string) map[string]string {
		sts := getStatefulSetG(ns, testServerName+"-"+component)
		for _, v := range sts.Spec.Template.Spec.Volumes {
			if v.Name == volume && v.Secret != nil {
				s := &corev1.Secret{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: v.Secret.SecretName, Namespace: ns}, s)).To(Succeed())
				out := map[string]string{}
				for k, val := range s.Data {
					out[k] = string(val)
				}
				return out
			}
		}
		return nil
	}
	postfixHash := func() string { return podTemplateHash(getStatefulSetG(ns, postfixObjectName)) }
	ready := func() (metav1.ConditionStatus, string, string) {
		return serverConditionG(ns, testServerName, conditionReady)
	}

	BeforeEach(func() {
		installCertificateCRD()
		ns, domain = grantFixture()
		secret(bindName, map[string][]byte{dirKeyPassword: []byte(bindPassword)})
		secret(caName, map[string][]byte{dirKeyCA: testCAPEM()})
		updateDirectory(withCredentials)
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		createTLSSecretG(ns)
		r = newReconcilerG(AuthorizationAllowlist, true)
	})

	It("renders the maps into the ConfigMap and the secret values into Secrets", func() {
		deployG(r, ns, testServerName)

		mainCF := postfixMainCFG(ns)
		Expect(mainCF).To(ContainSubstring("\nvirtual_mailbox_maps = ldap:/etc/postfix-secret/ldap-mailbox.cf\n"))
		Expect(mainCF).To(ContainSubstring("\nvirtual_alias_maps = ldap:/etc/postfix-secret/ldap-alias.cf\n"))
		Expect(mainCF).To(ContainSubstring("\nvirtual_alias_domains =\n"))

		postfixFiles := mountedSecret("postfix", "directory")
		Expect(postfixFiles).To(HaveKey("ldap-mailbox.cf"))
		Expect(postfixFiles["ldap-mailbox.cf"]).To(ContainSubstring("\nbind_pw = " + bindPassword + "\n"))
		Expect(postfixFiles).To(HaveKey("ca.crt"))
		dovecotFiles := mountedSecret("dovecot", "users")
		Expect(dovecotFiles).To(HaveKeyWithValue("ldap-password", bindPassword))
		Expect(dovecotFiles).NotTo(HaveKey("users"))

		// The secret values are in no ConfigMap.
		for _, component := range []string{"postfix", "dovecot", "rspamd", "valkey"} {
			for _, cm := range configMapsG(ns, component) {
				for k, v := range cm.Data {
					Expect(v).NotTo(ContainSubstring(bindPassword), "%s/%s", cm.Name, k)
				}
			}
		}
		// The validation Job of Postfix reads the tables too.
		var seen bool
		for _, j := range jobsG(ns, testServerName, "postfix") {
			for _, v := range j.Spec.Template.Spec.Volumes {
				seen = seen || v.Name == "directory"
			}
		}
		Expect(seen).To(BeTrue(), "the Postfix validation Job mounts the directory Secret")
	})

	It("rolls a changed bind password out as a new, validated revision", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()

		setPassword("bind-secret-value-2")
		reconcileServerG(r, ns, testServerName)
		// The running pods keep their revision until the Jobs passed.
		Expect(postfixHash()).To(Equal(before))
		status, reason, _ := serverConditionG(ns, testServerName, ConditionConfigValidated)
		Expect(status).To(Equal(metav1.ConditionUnknown))
		Expect(reason).To(Equal(ReasonValidatingConfig))

		finishJobsG(ns, testServerName, true)
		reconcileServerG(r, ns, testServerName)
		Expect(postfixHash()).NotTo(Equal(before))
		Expect(mountedSecret("postfix", "directory")["ldap-mailbox.cf"]).To(ContainSubstring("bind_pw = bind-secret-value-2\n"))
		Expect(mountedSecret("dovecot", "users")).To(HaveKeyWithValue("ldap-password", "bind-secret-value-2"))
	})

	It("rolls a changed MailDirectory out as a new revision", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()

		updateDirectory(func(l *cnmsv1alpha1.LDAP) { l.BaseDN = "ou=people,dc=example,dc=org" })
		deployG(r, ns, testServerName)
		Expect(postfixHash()).NotTo(Equal(before))
		Expect(mountedSecret("postfix", "directory")["ldap-mailbox.cf"]).To(ContainSubstring("\nsearch_base = ou=people,dc=example,dc=org\n"))
	})

	It("keeps the running revision while a Secret of the directory is missing", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()

		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bindName, Namespace: ns}})).To(Succeed())
		reconcileServerG(r, ns, testServerName)
		status, reason, msg := ready()
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonDirectoryInvalid))
		Expect(msg).To(ContainSubstring(bindName))
		Expect(msg).NotTo(ContainSubstring(bindPassword))
		Expect(postfixHash()).To(Equal(before))
		Expect(jobsG(ns, testServerName, "")).To(HaveLen(4), "no validation for a render that was refused")

		// The Secret comes back: the same revision is Ready again.
		secret(bindName, map[string][]byte{dirKeyPassword: []byte(bindPassword)})
		reconcileServerG(r, ns, testServerName)
		_, reason, _ = ready()
		Expect(reason).NotTo(Equal(ReasonDirectoryInvalid))
		Expect(postfixHash()).To(Equal(before))
	})

	It("refuses a bind password that would end the setting of a file", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()
		setPassword(bindPassword + "\n")
		reconcileServerG(r, ns, testServerName)
		_, reason, msg := ready()
		Expect(reason).To(Equal(ReasonDirectoryInvalid))
		Expect(msg).To(ContainSubstring("control character"))
		Expect(postfixHash()).To(Equal(before))
	})

	It("refuses a directory without TLS: Dovecot would bind with the password of every login", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()
		updateDirectory(func(l *cnmsv1alpha1.LDAP) {
			l.BindDN, l.BindSecretRef = nil, nil
			l.TLS = &cnmsv1alpha1.LDAPTLS{Mode: new("None")}
		})
		reconcileServerG(r, ns, testServerName)
		status, reason, msg := ready()
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonDirectoryInvalid))
		Expect(msg).To(ContainSubstring("clear text"))
		Expect(postfixHash()).To(Equal(before), "the running revision stays")
		Expect(jobsG(ns, testServerName, "")).To(HaveLen(4), "no validation for a render that was refused")

		// Back to StartTLS: the same revision is Ready again.
		updateDirectory(func(l *cnmsv1alpha1.LDAP) { l.TLS = &cnmsv1alpha1.LDAPTLS{} })
		reconcileServerG(r, ns, testServerName)
		_, reason, _ = ready()
		Expect(reason).NotTo(Equal(ReasonDirectoryInvalid))
	})

	It("refuses a variable in the bind DN and blanks around the base DN", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()
		for _, mutate := range []func(*cnmsv1alpha1.LDAP){
			func(l *cnmsv1alpha1.LDAP) { l.BindDN = new("cn=%{user},dc=example,dc=org") },
			func(l *cnmsv1alpha1.LDAP) { l.BaseDN = "dc=example,dc=org " },
		} {
			updateDirectory(withCredentials)
			updateDirectory(mutate)
			reconcileServerG(r, ns, testServerName)
			_, reason, msg := ready()
			Expect(reason).To(Equal(ReasonDirectoryInvalid))
			Expect(msg).NotTo(ContainSubstring(bindPassword))
			Expect(postfixHash()).To(Equal(before))
		}
	})

	It("keeps the running revision while the MailDirectory is gone", func() {
		deployG(r, ns, testServerName)
		before := postfixHash()
		Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDirectory{ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: ns}})).To(Succeed())
		reconcileServerG(r, ns, testServerName)
		_, reason, _ := ready()
		Expect(reason).To(Equal(ReasonDirectoryNotFound))
		Expect(postfixHash()).To(Equal(before))
	})

	// ADR 0015, section 3, point 7: one user source per server.
	Describe("with a second user source", func() {
		addStaticUsers := func() {
			createUsersSecretG(ns, "users:\n  - username: alice\n    passwordHash: '{SHA512-CRYPT}x'\n    mailbox: alice@"+domain+"\n")
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName}}
			})
		}

		It("is not Ready and keeps the running revision", func() {
			deployG(r, ns, testServerName)
			before := postfixHash()
			// The envtest has no Pods, so the server is not Ready yet:
			// make it Ready, as it would be, to see the transition.
			server := &cnmsv1alpha1.MailServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
			setCondition(&server.Status.Conditions, server.Generation, conditionReady, metav1.ConditionTrue, ReasonReady, "ready")
			Expect(k8sClient.Status().Update(ctx, server)).To(Succeed())
			rec := events.NewFakeRecorder(50)
			r.Recorder = rec
			addStaticUsers()
			jobs := len(jobsG(ns, testServerName, ""))

			reconcileServerG(r, ns, testServerName)
			Expect(strings.Join(drainEvents(rec), "\n")).To(ContainSubstring("UserSourceConflict"),
				"the change of Ready to False is an Event")
			status, reason, msg := ready()
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonUserSourceConflict))
			Expect(msg).To(ContainSubstring("directoryRef"))
			Expect(msg).To(ContainSubstring("submission.insecureStaticUsers"))
			Expect(postfixHash()).To(Equal(before))
			Expect(jobsG(ns, testServerName, "")).To(HaveLen(jobs), "no validation for a render that was refused")
			Expect(postfixMainCFG(ns)).To(ContainSubstring("virtual_mailbox_maps = ldap:"))
		})

		It("is resolved by removing either source", func() {
			deployG(r, ns, testServerName)
			before := postfixHash()
			addStaticUsers()
			reconcileServerG(r, ns, testServerName)
			_, reason, _ := ready()
			Expect(reason).To(Equal(ReasonUserSourceConflict))

			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) { s.Spec.Submission = nil })
			reconcileServerG(r, ns, testServerName)
			_, reason, _ = ready()
			Expect(reason).NotTo(Equal(ReasonUserSourceConflict))
			Expect(postfixHash()).To(Equal(before), "the directory render is unchanged")

			staticOnly := func(s *cnmsv1alpha1.MailServer) {
				s.Spec.DirectoryRef = nil
				s.Spec.Submission = &cnmsv1alpha1.Submission{InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName}}
			}
			mutateServerG(ns, staticOnly)
			reconcileServerG(r, ns, testServerName)
			_, reason, _ = ready()
			Expect(reason).NotTo(Equal(ReasonUserSourceConflict))
		})

		It("is reported before a missing directory", func() {
			addStaticUsers()
			Expect(k8sClient.Delete(ctx, &cnmsv1alpha1.MailDirectory{ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: ns}})).To(Succeed())
			reconcileServerG(r, ns, testServerName)
			_, reason, _ := ready()
			Expect(reason).To(Equal(ReasonUserSourceConflict))
		})
	})
})

// A change of the MailDirectory reaches the servers of its namespace
// through the watch (the 30 s re-check of a not-ready server is far
// outside the wait).
var _ = Describe("MailServer watch of the MailDirectory", func() {
	It("re-evaluates a server whose directory is fixed", func() {
		ns, _ := grantFixture()
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "late-bind", Namespace: ns},
			Data: map[string][]byte{dirKeyPassword: []byte("pw")}})).To(Succeed())
		d := &cnmsv1alpha1.MailDirectory{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: directoryName, Namespace: ns}, d)).To(Succeed())
		// The Secret named for the bind password does not exist.
		d.Spec.LDAP.BindDN = new("cn=lookup,dc=example,dc=org")
		d.Spec.LDAP.BindSecretRef = &cnmsv1alpha1.SecretReference{Name: "missing-bind"}
		Expect(k8sClient.Update(ctx, d)).To(Succeed())
		createServerG(ns, testServerName, nil)
		startManagerG(mailServerSetup(AuthorizationAllowlist))
		reason := func() (string, error) {
			_, r, _ := serverConditionG(ns, testServerName, conditionReady)
			return r, nil
		}
		Eventually(reason, "20s", "200ms").Should(Equal(ReasonDirectoryInvalid))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: directoryName, Namespace: ns}, d)).To(Succeed())
		d.Spec.LDAP.BindSecretRef = &cnmsv1alpha1.SecretReference{Name: "late-bind"}
		Expect(k8sClient.Update(ctx, d)).To(Succeed())
		Eventually(reason, "20s", "200ms").ShouldNot(Equal(ReasonDirectoryInvalid))
	})
})

// ADR 0015, section 6: the digest of the secret files is keyed.
var _ = Describe("MailServer hash key", func() {
	var ns string
	keyName := func() types.NamespacedName {
		return types.NamespacedName{Name: testServerName + "-hash-key", Namespace: ns}
	}

	BeforeEach(func() {
		installCertificateCRD()
		ns, _ = grantFixture()
		// Mailboxes: the directory tables, which carry settings, are rendered.
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		})
		createTLSSecretG(ns)
	})

	It("creates one random key, owned by the server, and keeps it", func() {
		r := newReconcilerG(AuthorizationAllowlist, true)
		deployG(r, ns, testServerName)
		key := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, keyName(), key)).To(Succeed())
		Expect(key.Data["key"]).To(HaveLen(32))
		Expect(key.Immutable).To(HaveValue(BeTrue()))
		Expect(metav1.IsControlledBy(key, getServerG(ns))).To(BeTrue())
		first := string(key.Data["key"])

		reconcileServerG(r, ns, testServerName)
		Expect(k8sClient.Get(ctx, keyName(), key)).To(Succeed())
		Expect(string(key.Data["key"])).To(Equal(first), "never regenerated")

		// The cleanup of superseded objects runs once the rollout is ready;
		// a stale object of the server that nothing references proves that
		// it ran, and the key, which carries the same labels, has to
		// survive it.
		stale := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: "stale-config", Namespace: ns,
			Labels: map[string]string{manifests.InstanceLabelKey: testServerName, manifests.ManagedByLabel: manifests.ManagedByValue},
		}}
		Expect(controllerutil.SetControllerReference(getServerG(ns), stale, scheme.Scheme)).To(Succeed())
		Expect(k8sClient.Create(ctx, stale)).To(Succeed())
		convergeWorkloadsG(ns)
		reconcileServerG(r, ns, testServerName)
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(stale), &corev1.ConfigMap{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the cleanup ran")
		Expect(k8sClient.Get(ctx, keyName(), key)).To(Succeed(), "and left the key alone")
		Expect(string(key.Data["key"])).To(Equal(first))
	})

	It("creates no key for a server without secret files", func() {
		// No mailboxes, no submission and an anonymous directory: nothing
		// to key. (With submission, Postfix reads the sender table of the
		// directory, a Secret with the directory settings.)
		other, _ := grantFixture()
		createServerG(other, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Submission = &cnmsv1alpha1.Submission{Enabled: new(false)}
		})
		createTLSSecretG(other)
		deployG(newReconcilerG(AuthorizationAllowlist, true), other, testServerName)
		err := k8sClient.Get(ctx, types.NamespacedName{Name: testServerName + "-hash-key", Namespace: other}, &corev1.Secret{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no hash key Secret")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: other}, &appsv1.StatefulSet{})).To(Succeed(), "but the data plane is deployed")
	})

	It("does not take a key a tenant created", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: keyName().Name, Namespace: ns},
			Data:       map[string][]byte{"key": make([]byte, 32)},
		})).To(Succeed())
		reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, testServerName)
		status, reason, _ := serverConditionG(ns, testServerName, conditionReady)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonOwnershipConflict))
		err := k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns}, &appsv1.StatefulSet{})
		Expect(err).To(HaveOccurred(), "nothing is deployed under a key the server does not own")
	})
})
