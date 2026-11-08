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
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

// ADR 0015, sections 4.2, 4.4, 4.7 and 4.9: the MailServer serves the service
// accounts that it consents to, decides their senders with every other
// claimant, and renders a login, an owner of the sender and nothing else.
var _ = Describe("MailServer with service accounts", func() {
	var (
		ns, appNS string
		domA      string
		r         *MailServerReconciler
		sr        *MailServiceAccountReconciler
		looker    *fakeLooker
		rec       *events.FakeRecorder
	)

	BeforeEach(func() {
		installCertificateCRD()
		ns, appNS = uniqueNamespace(), uniqueNamespace()
		createNamespaceG(ns)
		createNamespaceG(appNS)
		createServerG(ns, testServerName, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = nil
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
			s.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{
				AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{{Name: appNS}},
			}
		})
		createTLSSecretG(ns)
		domA = addGrantedDomainG(ns, testServerName)
		rec = events.NewFakeRecorder(100)
		r = newReconcilerG(AuthorizationAllowlist, true)
		r.Recorder = rec
		looker = &fakeLooker{}
		sr = &MailServiceAccountReconciler{
			Client: newIndexedClient(k8sClient), Scheme: k8sClient.Scheme(), APIReader: k8sClient, Directory: looker,
			DomainAuthorization: AuthorizationAllowlist,
		}
	})

	sender := func(local string) string { return local + "@" + domA }
	// account makes a MailServiceAccount in the namespace of the application
	// and lets its controller admit it and make its credential.
	account := func(name, local string) {
		a := &cnmsv1alpha1.MailServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appNS},
			Spec: cnmsv1alpha1.MailServiceAccountSpec{
				ServerRef: cnmsv1alpha1.NamespacedNameReference{Name: testServerName, Namespace: ns},
				Sender:    sender(local),
			},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		reconcileAccount(sr, appNS, name)
	}
	login := func(name string) string { return serviceAccountLogin(appNS, name) }
	hashOf := func(name string) string {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: appNS, Name: accountCredentialName(name)}, s)).To(Succeed())
		return string(s.Data[keyPasswordHash])
	}
	deploy := func() { deployG(r, ns, testServerName) }
	reconcileServer := func() { reconcileServerG(r, ns, testServerName) }
	live := func() *corev1.Secret {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: liveUsersName}, s)).To(Succeed())
		return s
	}
	passdb := func() string { return string(live().Data["passdb"]) }
	userdb := func() string { return string(live().Data["userdb"]) }
	mountedConfig := func(sts *appsv1.StatefulSet) *corev1.ConfigMap {
		for _, v := range sts.Spec.Template.Spec.Volumes {
			if v.Name == volumeConfig && v.ConfigMap != nil {
				cm := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: v.ConfigMap.Name}, cm)).To(Succeed())
				return cm
			}
		}
		Fail("the StatefulSet mounts no ConfigMap")
		return nil
	}
	table := func(file string) string { return mountedConfig(getStatefulSetG(ns, postfixObjectName)).Data[file] }
	ready := func() (metav1.ConditionStatus, string, string) {
		return serverConditionG(ns, testServerName, conditionReady)
	}
	owners := func() string {
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: testServerName + "-rspamd"}, dep)).To(Succeed())
		for _, v := range dep.Spec.Template.Spec.Volumes {
			if v.Name == "data" && v.ConfigMap != nil {
				cm := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: v.ConfigMap.Name}, cm)).To(Succeed())
				return cm.Data["from_owners"]
			}
		}
		return ""
	}
	has := func(content, line string) bool { return strings.Contains("\n"+content, "\n"+line) }

	It("serves a server with service accounts alone as a send-only server", func() {
		account("notifications", "notifications")
		deploy()

		_, reason, _ := ready()
		Expect(reason).NotTo(Equal(ReasonNoUserSource), "a server whose only accounts are service accounts is a send-only server")
		l, hash := login("notifications"), hashOf("notifications")
		Expect(hash).To(HavePrefix("{ARGON2ID}"))

		// The login, by its name alone, with the hash and the user= field; the
		// userdb has no line for it: nothing is delivered to a service account.
		Expect(has(passdb(), l+":"+hash+"::::::user="+l+"\n")).To(BeTrue(), passdb())
		Expect(passdb()).NotTo(ContainSubstring("notifications@"), "a service account has no address to log in with")
		Expect(userdb()).NotTo(ContainSubstring(l))
		Expect(userdb()).NotTo(ContainSubstring("notifications@"))

		// The sender is owned by the login: the envelope and the header From.
		Expect(has(table("sender_login"), sender("notifications")+" "+l+"\n")).To(BeTrue(), table("sender_login"))
		Expect(has(owners(), sender("notifications")+" "+l+"\n")).To(BeTrue(), owners())
		// It receives nothing: no recipient, no alias, no deferral.
		for _, f := range []string{"vmailbox", "valias", "recipient_defer"} {
			Expect(table(f)).NotTo(ContainSubstring("notifications@"), f)
		}
		status, reason, message := serverConditionG(ns, testServerName, ConditionAddressesServed)
		Expect(status).To(Equal(metav1.ConditionTrue))
		Expect(reason).To(Equal(ReasonAllServed))
		Expect(message).To(ContainSubstring("1 service accounts"))
		Expect(message).NotTo(ContainSubstring(appNS), "no other namespace is named")
	})

	It("is blocked for lack of a user source while no account is accepted", func() {
		// Consented, but its controller has not admitted it.
		a := &cnmsv1alpha1.MailServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "late", Namespace: appNS},
			Spec: cnmsv1alpha1.MailServiceAccountSpec{
				ServerRef: cnmsv1alpha1.NamespacedNameReference{Name: testServerName, Namespace: ns}, Sender: sender("late"),
			},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		reconcileServer()
		status, reason, _ := ready()
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoUserSource))

		reconcileAccount(sr, appNS, "late")
		reconcileServer()
		_, reason, _ = ready()
		Expect(reason).NotTo(Equal(ReasonNoUserSource))
	})

	It("takes the login from the account and the hash only from a Secret it controls, in the operator's form", func() {
		account("notifications", "notifications")
		deploy()
		l, hash := login("notifications"), hashOf("notifications")
		Expect(has(passdb(), l+":"+hash)).To(BeTrue())

		secret := &corev1.Secret{}
		key := types.NamespacedName{Namespace: appNS, Name: accountCredentialName("notifications")}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())

		By("a username key that names another login is not read")
		secret.Data[keyUsername] = []byte(apiUserName)
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		reconcileServer()
		Expect(passdb()).NotTo(ContainSubstring(apiUserName))
		Expect(has(passdb(), l+":"+hash)).To(BeTrue())

		By("a hash that is not the operator's form gives no login, and the sender stays owned")
		secret.Data[keyPasswordHash] = []byte("{PLAIN}guess")
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		reconcileServer()
		Expect(passdb()).NotTo(ContainSubstring(l))
		Expect(passdb()).NotTo(ContainSubstring("PLAIN"))
		Expect(has(table("sender_login"), sender("notifications")+" "+l)).To(BeTrue())

		By("a Secret that the account does not control gives none either")
		Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: appNS, Labels: accountLabels(&cnmsv1alpha1.MailServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: "notifications", Namespace: appNS}}, getServerG(ns))},
			Data: map[string][]byte{keyPasswordHash: []byte(hash)},
		})).To(Succeed())
		reconcileServer()
		Expect(passdb()).NotTo(ContainSubstring(l), "a foreign Secret with a valid hash is not taken")
	})

	// The hash is written into a line of a passwd-file. Go's base64 decoder
	// ignores a newline, so a tenant can store a hash with the pinned
	// parameters and a newline inside its tag that decodes and verifies: it
	// must not reach the render, which refuses a control character and would
	// fail the reconcile of the whole server, revocations included.
	It("leaves an account whose hash holds a newline without a login, and fails nothing", func() {
		account("notifications", "notifications")
		account("other", "other")
		deploy()
		l, o := login("notifications"), login("other")
		hash := hashOf("notifications")
		Expect(has(passdb(), l+":")).To(BeTrue())

		secret := &corev1.Secret{}
		key := types.NamespacedName{Namespace: appNS, Name: accountCredentialName("notifications")}
		Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
		poisoned := hash[:len(hash)-10] + "\n" + hash[len(hash)-10:]
		Expect(poisoned).NotTo(Equal(hash))
		secret.Data[keyPasswordHash] = []byte(poisoned)
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())

		deploy() // reconcileServerG fails the spec on an error
		Expect(passdb()).NotTo(ContainSubstring(l), "no login for the poisoned hash")
		Expect(has(passdb(), o+":")).To(BeTrue(), "the other account is not affected")
		Expect(has(table("sender_login"), sender("notifications")+" "+l+"\n")).To(BeTrue(), "the sender stays owned")
		Expect(has(table("sender_login"), sender("other")+" "+o+"\n")).To(BeTrue())
		_, reason, _ := ready()
		Expect(reason).NotTo(Equal(ReasonApplyFailed))

		// The controller of the account writes the Secret again, with a hash
		// that verifies its password.
		reconcileAccount(sr, appNS, "notifications")
		fixed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, key, fixed)).To(Succeed())
		Expect(string(fixed.Data[keyPasswordHash])).NotTo(Equal(poisoned))
		Expect(validStoredHash(string(fixed.Data[keyPasswordHash]))).To(BeTrue())
		reconcileServer()
		Expect(has(passdb(), l+":"+string(fixed.Data[keyPasswordHash]))).To(BeTrue(), "the login is back")
	})

	It("treats a MailUser credential with a newline in its hash the same way", func() {
		ur := &MailUserReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, DomainAuthorization: AuthorizationAllowlist}
		for _, name := range []string{apiUserName, "bob"} {
			Expect(k8sClient.Create(ctx, &cnmsv1alpha1.MailUser{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: cnmsv1alpha1.MailUserSpec{
					ServerRef: cnmsv1alpha1.NameReference{Name: testServerName},
					DomainRef: cnmsv1alpha1.NameReference{Name: domainLabel(domA)},
				},
			})).To(Succeed())
			reconcileUser(ur, ns, name)
		}
		deploy()
		Expect(has(passdb(), apiUserName+":")).To(BeTrue())

		secret := credentialG(ns, apiUserName)
		hash := string(secret.Data[keyPasswordHash])
		secret.Data[keyPasswordHash] = []byte(hash[:len(hash)-10] + "\r\n" + hash[len(hash)-10:])
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		deploy()
		Expect(passdb()).NotTo(ContainSubstring(apiUserName+":"), "no login for the poisoned hash")
		Expect(has(passdb(), "bob:")).To(BeTrue(), "the other user is not affected")
		Expect(table("vmailbox")).To(ContainSubstring(apiUserName+"@"+domA), "it still receives")
		Expect(has(userdb(), apiUserName+"::")).To(BeTrue(), "and stays in the userdb")
	})

	It("decides the sender at render time, not from the admission in the status", func() {
		account("notifications", apiAccountName)
		deploy()
		l := login("notifications")
		Expect(has(passdb(), l+":")).To(BeTrue())

		// A MailUser takes the address after the account was admitted: the
		// account's status is not refreshed, the server decides alone.
		Expect(k8sClient.Create(ctx, &cnmsv1alpha1.MailUser{
			ObjectMeta: metav1.ObjectMeta{Name: apiAccountName, Namespace: ns},
			Spec: cnmsv1alpha1.MailUserSpec{
				ServerRef: cnmsv1alpha1.NameReference{Name: testServerName},
				DomainRef: cnmsv1alpha1.NameReference{Name: domainLabel(domA)},
			},
		})).To(Succeed())
		deploy() // the Postfix maps and the owners wait for their validation Jobs
		Expect(passdb()).NotTo(ContainSubstring(l), "a user's address is not a service account's")
		Expect(table("sender_login")).NotTo(ContainSubstring(" " + l))
		Expect(owners()).NotTo(ContainSubstring(l))
		Expect(has(table("sender_login"), sender(apiAccountName)+" "+apiAccountName+"\n")).To(BeTrue(), "the user owns it")
		status, reason, message := serverConditionG(ns, testServerName, ConditionAddressesServed)
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonPartiallyServed))
		Expect(message).To(ContainSubstring("1 senders of service accounts"))
		Expect(message).NotTo(ContainSubstring(l))
	})

	It("stops serving an account when its namespace loses the consent", func() {
		account("notifications", "notifications")
		deploy()
		l := login("notifications")
		Expect(has(passdb(), l+":")).To(BeTrue())

		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) { s.Spec.ServiceAccounts = nil })
		reconcileServer()
		Expect(passdb()).NotTo(ContainSubstring(l), "the login goes in the same pass, before the account controller runs")
		status, reason, _ := ready()
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonNoUserSource), "no account is accepted any more")
	})

	It("adds the logins and the senders to a server with MailUsers, and to the static users", func() {
		Expect(k8sClient.Create(ctx, &cnmsv1alpha1.MailUser{
			ObjectMeta: metav1.ObjectMeta{Name: apiUserName, Namespace: ns},
			Spec: cnmsv1alpha1.MailUserSpec{
				ServerRef: cnmsv1alpha1.NameReference{Name: testServerName},
				DomainRef: cnmsv1alpha1.NameReference{Name: domainLabel(domA)},
			},
		})).To(Succeed())
		ur := &MailUserReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, DomainAuthorization: AuthorizationAllowlist}
		reconcileUser(ur, ns, apiUserName)
		account("notifications", "notifications")
		deploy()
		l := login("notifications")
		Expect(has(passdb(), apiUserName+":")).To(BeTrue())
		Expect(has(passdb(), l+":")).To(BeTrue())
		Expect(table("vmailbox")).To(ContainSubstring(apiUserName + "@" + domA))
		Expect(table("vmailbox")).NotTo(ContainSubstring("notifications@"))
		Expect(has(table("sender_login"), sender(apiUserName)+" "+apiUserName+"\n")).To(BeTrue())
		Expect(has(table("sender_login"), sender("notifications")+" "+l+"\n")).To(BeTrue())
	})

	It("counts the senders of service accounts as addresses, and a foreign namespace can fill the limit", func() {
		r.Limits = addresses.Limits{Addresses: 2}
		for _, name := range []string{"one", "two", "three"} {
			account(name, name)
		}
		reconcileServer()
		status, reason, message := ready()
		Expect(status).To(Equal(metav1.ConditionFalse))
		Expect(reason).To(Equal(ReasonAddressLimitExceeded))
		Expect(message).To(ContainSubstring("3 addresses"))
		Expect(message).NotTo(ContainSubstring(appNS))
	})

	It("keeps the directory tables, unions the senders and asks the file first", func() {
		createDirectoryG(ns)
		mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.DirectoryRef = &cnmsv1alpha1.NameReference{Name: directoryName}
		})
		account("notifications", "notifications")
		Expect(getAccountG(appNS, "notifications").Status.AdmittedFor).To(Equal(ns + "/" + testServerName + "/" + directoryName))
		Expect(looker.calls()).To(Equal(1))
		deploy()
		l := login("notifications")

		mainCF := table("main.cf")
		Expect(mainCF).To(ContainSubstring("\nsmtpd_sender_login_maps = unionmap:{texthash:/etc/postfix/sender_login, ldap:/etc/postfix-secret/ldap-sender.cf}\n"))
		Expect(has(table("sender_login"), sender("notifications")+" "+l+"\n")).To(BeTrue())
		Expect(table("vmailbox")).NotTo(ContainSubstring("notifications@"))
		Expect(has(passdb(), l+":")).To(BeTrue(), "the file of the users Secret carries the login")
		conf := mountedConfig(getStatefulSetG(ns, "mail-dovecot")).Data["dovecot.conf"]
		file := strings.Index(conf, "passdb passwd-file {")
		dir := strings.Index(conf, "passdb ldap {")
		Expect(file).To(BeNumerically(">=", 0))
		Expect(file).To(BeNumerically("<", dir), "the file is asked before the directory")
		Expect(conf).NotTo(ContainSubstring("userdb passwd-file"))
		Expect(owners()).To(BeEmpty(), "check 2 is off for a directory, service accounts included")
	})

	It("maps an account to the server it names, in the namespace serverRef gives", func() {
		a := &cnmsv1alpha1.MailServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: appNS},
			Spec:       cnmsv1alpha1.MailServiceAccountSpec{ServerRef: cnmsv1alpha1.NamespacedNameReference{Name: testServerName, Namespace: ns}},
		}
		got := r.mapAccountServer(context.Background(), a)
		Expect(got).To(Equal([]ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: testServerName}}}))
		a.Spec.ServerRef.Namespace = ""
		got = r.mapAccountServer(context.Background(), a)
		Expect(got).To(Equal([]ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: appNS, Name: testServerName}}}))
	})
})

func reconcileAccount(r *MailServiceAccountReconciler, ns, name string) {
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	Expect(err).NotTo(HaveOccurred())
}

func getAccountG(ns, name string) *cnmsv1alpha1.MailServiceAccount {
	a := &cnmsv1alpha1.MailServiceAccount{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, a)).To(Succeed())
	return a
}

// The accounts that a server accepts are the consented ones whose admission
// matches the server and its directory; the others only claim their sender.
func TestAcceptedAccounts(t *testing.T) {
	server := serverIn("srv", testServerName)
	server.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{
		{Name: unitApp}, {Name: nsLimited, SenderDomains: []string{unitDomainA}},
	}}
	admit := func(a *cnmsv1alpha1.MailServiceAccount) *cnmsv1alpha1.MailServiceAccount {
		a.Status.AdmittedSender, a.Status.AdmittedFor = a.Spec.Sender, admissionKey(server)
		return a
	}
	accounts := []cnmsv1alpha1.MailServiceAccount{
		*admit(accountIn("srv", "own", "", testServerName, "own@a.example")),
		*admit(accountIn(unitApp, "ok", "srv", testServerName, "ok@a.example")),
		*accountIn(unitApp, "unadmitted", "srv", testServerName, "u@a.example"),
		*admit(accountIn("stranger", "x", "srv", testServerName, "x@a.example")),
		*admit(accountIn(nsLimited, "out", "srv", testServerName, "o@b.example")),
		func() cnmsv1alpha1.MailServiceAccount {
			a := admit(accountIn(unitApp, "moved", "srv", testServerName, "m@a.example"))
			a.Status.AdmittedFor = "srv/other/"
			return *a
		}(),
	}
	accepted := acceptedAccounts(server, accounts)
	got := make([]string, 0, len(accepted))
	for _, a := range accepted {
		got = append(got, a.Namespace+"/"+a.Name)
	}
	slices.Sort(got)
	if want := []string{"app/ok", "srv/own"}; !slices.Equal(got, want) {
		t.Errorf("accepted = %v, want %v", got, want)
	}
}

// An account whose login collides with another's, or whose sender is not
// served, is not rendered; the others are, sorted, with the login of the object.
func TestServiceAccountAccounts(t *testing.T) {
	res := addresses.Result{Senders: []addresses.SenderResult{
		{Owner: "a/one", Row: addresses.Row{Address: "one@a.example", Served: true, Reason: addresses.ReasonServed}},
		{Owner: "a/two", Row: addresses.Row{Address: "two@a.example", Served: false, Reason: addresses.ReasonSenderConflict}},
		{Owner: "a/three", Row: addresses.Row{Address: "three@a.example", Served: true, Reason: addresses.ReasonServed}},
	}}
	accepted := []cnmsv1alpha1.MailServiceAccount{
		*accountIn("a", "one", "", testServerName, "one@a.example"),
		*accountIn("a", "two", "", testServerName, "two@a.example"),
		*accountIn("a", "three", "", testServerName, "three@a.example"),
	}
	got := serviceAccountAccounts(res, accepted, map[string]string{"a/one": "{ARGON2ID}x"})
	if len(got) != 2 {
		t.Fatalf("accounts = %+v, want two", got)
	}
	for _, a := range got {
		if !a.SendOnly || !strings.HasPrefix(a.Name, "sa.") || a.Verdict.Name != a.Name || len(a.Verdict.Rows) != 1 || !a.Verdict.Rows[0].MaySend {
			t.Errorf("account = %+v", a)
		}
	}
	if got[0].Name > got[1].Name {
		t.Error("the accounts are not sorted")
	}
	if got[0].PasswordHash == "" && got[1].PasswordHash == "" {
		t.Error("no hash was carried")
	}
}

// volumeConfig is the name of the volume of the rendered configuration.
const volumeConfig = "config"
