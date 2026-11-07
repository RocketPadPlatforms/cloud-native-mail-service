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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
)

// fakeProber answers every probe with a fixed outcome and records what it
// was asked, so no spec runs a real connection.
type fakeProber struct {
	outcome directory.Outcome
	ldap    []directory.LDAPTarget
	sql     []directory.SQLTarget
}

func (f *fakeProber) ProbeLDAP(_ context.Context, t directory.LDAPTarget) directory.Outcome {
	f.ldap = append(f.ldap, t)
	return f.outcome
}

func (f *fakeProber) ProbeSQL(_ context.Context, t directory.SQLTarget) directory.Outcome {
	f.sql = append(f.sql, t)
	return f.outcome
}

// testCAPEM is a self-signed certificate, the CA bundle of the specs.
func testCAPEM() []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "directory test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// Names shared by the specs of the directory controller and the API cases.
const (
	dirBindSecret      = "bind"
	dirKeyPassword     = "password"
	dirKeyCA           = "ca.crt"
	dirAliasesRelation = "mail_aliases"
	dirSQLUser         = "lookup"
)

var dirCounter int

var _ = Describe("MailDirectory Controller", func() {
	const (
		bindPassword = "bind-secret-value"
		sqlPassword  = "sql-secret-value"
		bindDN       = "cn=lookup,dc=example,dc=org"
	)
	var (
		ns     string
		prober *fakeProber
		rec    *events.FakeRecorder
		r      *MailDirectoryReconciler
	)

	BeforeEach(func() {
		dirCounter++
		ns = fmt.Sprintf("cnms-dir-%d", dirCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		prober = &fakeProber{outcome: directory.Reachable}
		rec = events.NewFakeRecorder(20)
		r = &MailDirectoryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Prober: prober, Recorder: rec}
	})

	secret := func(name string, data map[string][]byte) {
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: data})).To(Succeed())
	}
	create := func(spec cnmsv1alpha1.MailDirectorySpec) types.NamespacedName {
		key := types.NamespacedName{Namespace: ns, Name: "dir"}
		Expect(k8sClient.Create(ctx, &cnmsv1alpha1.MailDirectory{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: ns}, Spec: spec})).To(Succeed())
		return key
	}
	ldapSpec := func(mutate func(*cnmsv1alpha1.LDAP)) cnmsv1alpha1.MailDirectorySpec {
		l := &cnmsv1alpha1.LDAP{Host: ldapTestHost, BaseDN: ldapTestBaseDN}
		mutate(l)
		return cnmsv1alpha1.MailDirectorySpec{Type: cnmsv1alpha1.DirectoryTypeLDAP, LDAP: l}
	}
	sqlSpec := func(mutate func(*cnmsv1alpha1.SQL)) cnmsv1alpha1.MailDirectorySpec {
		s := &cnmsv1alpha1.SQL{
			Host: sqlTestHost, Database: testServerName, Username: dirSQLUser,
			PasswordSecretRef: cnmsv1alpha1.SecretReference{Name: "db-password"},
		}
		mutate(s)
		return cnmsv1alpha1.MailDirectorySpec{Type: cnmsv1alpha1.DirectoryTypeSQL, SQL: s}
	}
	reconcile := func(key types.NamespacedName) (ctrl.Result, *cnmsv1alpha1.MailDirectory) {
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		dir := &cnmsv1alpha1.MailDirectory{}
		Expect(k8sClient.Get(ctx, key, dir)).To(Succeed())
		return res, dir
	}
	cond := func(dir *cnmsv1alpha1.MailDirectory, t string) metav1.Condition {
		c := meta.FindStatusCondition(dir.Status.Conditions, t)
		Expect(c).NotTo(BeNil(), "condition %s", t)
		return *c
	}

	It("ignores a directory that is gone", func() {
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "gone"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(ctrl.Result{}))
	})

	Context("with LDAP", func() {
		It("probes with the defaults and reports Ready and Reachable", func() {
			key := create(ldapSpec(func(*cnmsv1alpha1.LDAP) {}))
			res, dir := reconcile(key)

			Expect(res.RequeueAfter).To(Equal(5 * time.Minute))
			Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(cond(dir, "Ready")).To(HaveField("Reason", "Ready"))
			Expect(cond(dir, "Reachable")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(cond(dir, "Reachable")).To(HaveField("Reason", "Reachable"))
			Expect(dir.Status.ObservedGeneration).To(Equal(dir.Generation))
			Expect(prober.ldap).To(HaveLen(1))
			Expect(prober.ldap[0]).To(Equal(directory.LDAPTarget{
				Host: ldapTestHost, TLSMode: directory.LDAPStartTLS, BaseDN: ldapTestBaseDN,
				Attributes: directory.Attributes{Login: directory.DefaultLogin, Mail: directory.DefaultMail},
			}))
		})

		It("hands the settings and the secret values to the probe", func() {
			ca := testCAPEM()
			secret(dirBindSecret, map[string][]byte{dirKeyPassword: []byte(bindPassword)})
			secret("ca", map[string][]byte{dirKeyCA: ca})
			key := create(ldapSpec(func(l *cnmsv1alpha1.LDAP) {
				l.Port = new(int32(636))
				l.TLS = &cnmsv1alpha1.LDAPTLS{Mode: new("LDAPS"), CASecretRef: &cnmsv1alpha1.SecretReference{Name: "ca"}}
				l.BindDN, l.BindSecretRef = new(bindDN), &cnmsv1alpha1.SecretReference{Name: dirBindSecret}
				l.Attributes = &cnmsv1alpha1.LDAPAttributes{Login: new("sAMAccountName"), Mail: new(directory.DefaultMail), Aliases: new("mailAlias")}
				l.ActiveFilter = new("(mailEnabled=TRUE)")
			}))
			_, dir := reconcile(key)

			Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(prober.ldap).To(ConsistOf(directory.LDAPTarget{
				Host: ldapTestHost, Port: 636, TLSMode: "LDAPS", CA: ca,
				BaseDN: ldapTestBaseDN, BindDN: bindDN, BindPassword: bindPassword,
				Attributes:   directory.Attributes{Login: "sAMAccountName", Mail: directory.DefaultMail, Aliases: "mailAlias"},
				ActiveFilter: "(mailEnabled=TRUE)",
			}))
		})

		It("is not Ready without the bind Secret, and does not probe", func() {
			key := create(ldapSpec(func(l *cnmsv1alpha1.LDAP) {
				l.BindDN, l.BindSecretRef = new(bindDN), &cnmsv1alpha1.SecretReference{Name: dirBindSecret}
			}))
			res, dir := reconcile(key)

			Expect(cond(dir, "Ready")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "SecretNotFound")))
			Expect(cond(dir, "Reachable")).To(SatisfyAll(HaveField("Status", metav1.ConditionUnknown), HaveField("Reason", "SecretNotFound")))
			Expect(res.RequeueAfter).To(Equal(time.Minute)) // Secrets are not watched
			Expect(prober.ldap).To(BeEmpty())
			Expect(drainEvents(rec)).To(ConsistOf(ContainSubstring("SecretNotFound")))

			By("becoming Ready once the Secret exists")
			secret(dirBindSecret, map[string][]byte{dirKeyPassword: []byte(bindPassword)})
			_, dir = reconcile(key)
			Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(prober.ldap).To(HaveLen(1))
		})

		It("is not Ready with a Secret that lacks the key", func() {
			secret(dirBindSecret, map[string][]byte{"unrelated": []byte("x")})
			key := create(ldapSpec(func(l *cnmsv1alpha1.LDAP) {
				l.BindDN, l.BindSecretRef = new(bindDN), &cnmsv1alpha1.SecretReference{Name: dirBindSecret}
			}))
			_, dir := reconcile(key)
			Expect(cond(dir, "Ready")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "SecretIncomplete")))
			Expect(cond(dir, "Ready").Message).To(Equal("Secret bind has no key password"))
			Expect(prober.ldap).To(BeEmpty())
		})

		It("is not Ready with a CA bundle that holds no certificate", func() {
			secret("ca", map[string][]byte{dirKeyCA: []byte("not a certificate")})
			key := create(ldapSpec(func(l *cnmsv1alpha1.LDAP) {
				l.TLS = &cnmsv1alpha1.LDAPTLS{CASecretRef: &cnmsv1alpha1.SecretReference{Name: "ca"}}
			}))
			_, dir := reconcile(key)
			Expect(cond(dir, "Ready")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "SecretIncomplete")))
			Expect(prober.ldap).To(BeEmpty())
		})

		DescribeTable("is not Ready with an active filter that does not compile",
			func(filter string) {
				key := create(ldapSpec(func(l *cnmsv1alpha1.LDAP) { l.ActiveFilter = &filter }))
				res, dir := reconcile(key)
				Expect(cond(dir, "Ready")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "InvalidFilter")))
				Expect(cond(dir, "Reachable")).To(HaveField("Status", metav1.ConditionUnknown))
				Expect(res.RequeueAfter).To(BeZero()) // only a spec change can fix it
				Expect(prober.ldap).To(BeEmpty())
			},
			Entry("no parentheses", "mailEnabled=TRUE"),
			Entry("unbalanced", "(mailEnabled=TRUE"),
			Entry("a second filter appended through a closed first", "(a=b))(|(c=d)"),
		)

		It("keeps Ready when the directory is down, and says why in Reachable", func() {
			key := create(ldapSpec(func(*cnmsv1alpha1.LDAP) {}))
			reconcile(key)
			Expect(drainEvents(rec)).To(BeEmpty())

			prober.outcome = directory.Unreachable
			res, dir := reconcile(key)
			Expect(res.RequeueAfter).To(Equal(5 * time.Minute))
			Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(cond(dir, "Reachable")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "Unreachable")))
			Expect(drainEvents(rec)).To(ConsistOf(And(ContainSubstring("Warning"), ContainSubstring("Unreachable"))))

			By("emitting no second Event for the same state")
			reconcile(key)
			Expect(drainEvents(rec)).To(BeEmpty())

			By("recovering")
			prober.outcome = directory.Reachable
			_, dir = reconcile(key)
			Expect(cond(dir, "Reachable")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(drainEvents(rec)).To(ConsistOf(And(ContainSubstring("Normal"), ContainSubstring("Reachable"))))
		})

		DescribeTable("maps the outcome of the probe to the reason of Reachable",
			func(outcome directory.Outcome, reason string) {
				prober.outcome = outcome
				_, dir := reconcile(create(ldapSpec(func(*cnmsv1alpha1.LDAP) {})))
				Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
				Expect(cond(dir, "Reachable")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", reason)))
			},
			Entry("a rejected bind", directory.BindFailed, "BindFailed"),
			Entry("TLS", directory.TLSFailed, "TLSFailed"),
			Entry("a lookup that finds no account", directory.NoMatchingEntries, "NoMatchingEntries"),
			Entry("unreachable", directory.Unreachable, "Unreachable"),
		)

		It("does not write the status again when nothing changed", func() {
			key := create(ldapSpec(func(*cnmsv1alpha1.LDAP) {}))
			_, first := reconcile(key)
			_, second := reconcile(key)
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("puts no secret value or bind DN into a condition or an Event", func() {
			secret(dirBindSecret, map[string][]byte{dirKeyPassword: []byte(bindPassword)})
			key := create(ldapSpec(func(l *cnmsv1alpha1.LDAP) {
				l.BindDN, l.BindSecretRef = new(bindDN), &cnmsv1alpha1.SecretReference{Name: dirBindSecret}
			}))
			var logs bytes.Buffer
			logCtx := logf.IntoContext(ctx, zap.New(zap.WriteTo(&logs), zap.UseDevMode(true)))
			for _, o := range []directory.Outcome{directory.Unreachable, directory.BindFailed, directory.TLSFailed, directory.NoMatchingEntries} {
				prober.outcome = o
				_, err := r.Reconcile(logCtx, ctrl.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
				dir := &cnmsv1alpha1.MailDirectory{}
				Expect(k8sClient.Get(ctx, key, dir)).To(Succeed())
				for _, c := range dir.Status.Conditions {
					Expect(c.Message).NotTo(Or(ContainSubstring(bindPassword), ContainSubstring(bindDN)))
				}
			}
			for _, e := range drainEvents(rec) {
				Expect(e).NotTo(Or(ContainSubstring(bindPassword), ContainSubstring(bindDN)))
			}
			Expect(logs.String()).To(ContainSubstring("Probed directory"))
			Expect(logs.String()).NotTo(Or(ContainSubstring(bindPassword), ContainSubstring(bindDN)))
		})

		It("logs the periodic probe of an unchanged directory only at debug level", func() {
			key := create(ldapSpec(func(*cnmsv1alpha1.LDAP) {}))
			var logs bytes.Buffer
			logCtx := logf.IntoContext(ctx, zap.New(zap.WriteTo(&logs))) // the default level is Info
			for range 2 {
				_, err := r.Reconcile(logCtx, ctrl.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
			}
			// The first probe changed the condition; the second did not.
			Expect(strings.Count(logs.String(), "Probed directory")).To(Equal(1))
		})
	})

	Context("with SQL", func() {
		It("probes with the default relation and the password of the Secret", func() {
			secret(sqlTestSecret, map[string][]byte{dirKeyPassword: []byte(sqlPassword)})
			key := create(sqlSpec(func(*cnmsv1alpha1.SQL) {}))
			res, dir := reconcile(key)

			Expect(res.RequeueAfter).To(Equal(5 * time.Minute))
			Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(cond(dir, "Reachable")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(prober.sql).To(ConsistOf(directory.SQLTarget{
				Host: sqlTestHost, Port: 5432, TLSMode: "verify-full", Database: testServerName, Username: dirSQLUser,
				Password: sqlPassword, Users: directory.Relation{Schema: "public", Name: "cnms_users"},
			}))
		})

		It("hands the relations and the CA to the probe", func() {
			ca := testCAPEM()
			secret(sqlTestSecret, map[string][]byte{dirKeyPassword: []byte(sqlPassword)})
			secret("db-ca", map[string][]byte{dirKeyCA: ca})
			key := create(sqlSpec(func(s *cnmsv1alpha1.SQL) {
				s.TLS = &cnmsv1alpha1.SQLTLS{Mode: new("require"), CASecretRef: &cnmsv1alpha1.SecretReference{Name: "db-ca"}}
				s.Users = &cnmsv1alpha1.SQLRelation{Name: "mail_users", Schema: new("directory")}
				s.Aliases = &cnmsv1alpha1.SQLRelation{Name: dirAliasesRelation}
			}))
			reconcile(key)
			Expect(prober.sql).To(ConsistOf(directory.SQLTarget{
				Host: sqlTestHost, Port: 5432, TLSMode: "require", CA: ca, Database: testServerName, Username: dirSQLUser,
				Password: sqlPassword, Users: directory.Relation{Schema: "directory", Name: "mail_users"},
				Aliases: &directory.Relation{Schema: "public", Name: dirAliasesRelation},
			}))
		})

		It("is not Ready without the password Secret", func() {
			key := create(sqlSpec(func(*cnmsv1alpha1.SQL) {}))
			res, dir := reconcile(key)
			Expect(cond(dir, "Ready")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "SecretNotFound")))
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(prober.sql).To(BeEmpty())
		})

		It("reports a missing relation in Reachable and keeps Ready", func() {
			secret(sqlTestSecret, map[string][]byte{dirKeyPassword: []byte(sqlPassword)})
			prober.outcome = directory.RelationMissing
			_, dir := reconcile(create(sqlSpec(func(*cnmsv1alpha1.SQL) {})))
			Expect(cond(dir, "Ready")).To(HaveField("Status", metav1.ConditionTrue))
			Expect(cond(dir, "Reachable")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "RelationMissing")))
			Expect(cond(dir, "Reachable").Message).NotTo(ContainSubstring(sqlPassword))
		})

		It("says that an empty users relation has no account", func() {
			secret(sqlTestSecret, map[string][]byte{dirKeyPassword: []byte(sqlPassword)})
			prober.outcome = directory.NoMatchingEntries
			_, dir := reconcile(create(sqlSpec(func(*cnmsv1alpha1.SQL) {})))
			Expect(cond(dir, "Reachable")).To(SatisfyAll(HaveField("Status", metav1.ConditionFalse), HaveField("Reason", "NoMatchingEntries")))
			Expect(cond(dir, "Reachable").Message).To(ContainSubstring("login guard"))
		})
	})

	It("probes again after a spec change", func() {
		key := create(ldapSpec(func(*cnmsv1alpha1.LDAP) {}))
		reconcile(key)
		dir := &cnmsv1alpha1.MailDirectory{}
		Expect(k8sClient.Get(ctx, key, dir)).To(Succeed())
		dir.Spec.LDAP.ActiveFilter = new("(mailEnabled=TRUE)")
		Expect(k8sClient.Update(ctx, dir)).To(Succeed())
		_, dir = reconcile(key)
		Expect(prober.ldap).To(HaveLen(2))
		Expect(prober.ldap[1].ActiveFilter).To(Equal("(mailEnabled=TRUE)"))
		Expect(dir.Status.ObservedGeneration).To(Equal(dir.Generation))
	})
})
