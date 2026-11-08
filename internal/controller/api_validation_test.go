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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// These tests send objects to the API server to check the schema and
// CEL rules of the CRDs: a valid object is accepted, an invalid one is
// rejected with a message naming the field or the rule.

var apiNSCounter int

// Names shared by the cases of this file.
const (
	apiDomainName     = "dom"
	apiServerName     = "srv"
	apiDirectoryName  = "dir"
	mtaStsName        = "mta-sts." + testDomain
	sqlTestHost       = "db.example.org"
	sqlTestSecret     = "db-password"
	ldapHostField     = "spec.ldap.host"
	dmarcRuaField     = "spec.dmarc.rua"
	domainField       = "spec.domain"
	serverRefField    = "spec.serverRef.name"
	dkimSelectorField = "spec.dkim.keys[0].selector"
	spfIncludeField   = "spec.spf.include"
	algRSA            = "rsa"
	spfIP4Sample      = "203.0.113.1"
	spfIP6Sample      = "2001:db8::1"
)

// apiCase is one create attempt: an empty wantErr means the object
// must be accepted.
type apiCase struct {
	name    string
	mutate  func(ns string) error
	wantErr string
}

func runAPICases(cases []apiCase) {
	for _, c := range cases {
		It(c.name, func() {
			apiNSCounter++
			ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
			err := c.mutate(ns)
			if c.wantErr == "" {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(ContainSubstring(c.wantErr)))
		})
	}
}

// createServer creates a valid MailServer in ns after applying mutate.
func createServer(mutate func(*cnmsv1alpha1.MailServer)) func(string) error {
	return func(ns string) error {
		ms := validMailServer(apiServerName)
		ms.Namespace = ns
		mutate(ms)
		return k8sClient.Create(ctx, ms)
	}
}

// apiDomainMeta is the metadata of the MailDomain of the unstructured cases.
func apiDomainMeta(ns string) map[string]any {
	return map[string]any{"name": apiDomainName, "namespace": ns}
}

// createDomain creates a valid MailDomain "dom" for example.org on the
// server "srv" in ns after applying mutate, and returns it with the
// create error.
func createDomain(ns string, mutate func(*cnmsv1alpha1.MailDomain)) (*cnmsv1alpha1.MailDomain, error) {
	d := &cnmsv1alpha1.MailDomain{
		ObjectMeta: metav1.ObjectMeta{Name: apiDomainName, Namespace: ns},
		Spec: cnmsv1alpha1.MailDomainSpec{
			Domain:    testDomain,
			ServerRef: cnmsv1alpha1.NameReference{Name: apiServerName},
		},
	}
	if mutate != nil {
		mutate(d)
	}
	return d, k8sClient.Create(ctx, d)
}

var _ = Describe("API validation", func() {
	Describe("MailDomain status", func() {
		runAPICases([]apiCase{{
			name: "accepts several records with the same name and type",
			mutate: func(ns string) error {
				d, err := createDomain(ns, nil)
				Expect(err).NotTo(HaveOccurred())
				d.Status.Records = []cnmsv1alpha1.DNSRecord{
					{Name: testDomain, Type: "TXT", Value: "v=spf1 -all"},
					{Name: testDomain, Type: "MX", Value: "10 mail.example.org."},
					{Name: testDomain, Type: "MX", Value: "20 mail2.example.org."},
					{Name: mtaStsName, Type: "A", Value: sendIP4b},
					{Name: mtaStsName, Type: "A", Value: "203.0.113.11"},
					{Name: mtaStsName, Type: "AAAA", Value: "2001:db8::10"},
				}
				return k8sClient.Status().Update(ctx, d)
			},
		}})
	})

	Describe("MailServer", func() {
		minAvailable := func(v intstr.IntOrString) func(string) error {
			return createServer(func(ms *cnmsv1alpha1.MailServer) {
				ms.Spec.Availability = &cnmsv1alpha1.Availability{PostfixMinAvailable: &v}
			})
		}
		const pdbMsg = "non-negative integer or a percentage"
		runAPICases([]apiCase{
			{name: "accepts an integer postfixMinAvailable", mutate: minAvailable(intstr.FromInt32(2))},
			{name: "accepts a percentage postfixMinAvailable", mutate: minAvailable(intstr.FromString("50%"))},
			{name: "rejects a string without a percent sign", mutate: minAvailable(intstr.FromString("1")), wantErr: pdbMsg},
			{name: "rejects a percentage above 100", mutate: minAvailable(intstr.FromString("101%")), wantErr: pdbMsg},
			{name: "rejects a negative postfixMinAvailable", mutate: minAvailable(intstr.FromInt32(-1)), wantErr: pdbMsg},
		})
	})
})

// createLDAP and createSQL create a MailDirectory "dir" after
// applying mutate to a valid spec.
func createLDAP(mutate func(*cnmsv1alpha1.LDAP)) func(string) error {
	return func(ns string) error {
		l := &cnmsv1alpha1.LDAP{Host: ldapTestHost, BaseDN: ldapTestBaseDN}
		mutate(l)
		return k8sClient.Create(ctx, &cnmsv1alpha1.MailDirectory{
			ObjectMeta: metav1.ObjectMeta{Name: apiDirectoryName, Namespace: ns},
			Spec:       cnmsv1alpha1.MailDirectorySpec{Type: cnmsv1alpha1.DirectoryTypeLDAP, LDAP: l},
		})
	}
}

func createSQL(mutate func(*cnmsv1alpha1.SQL)) func(string) error {
	return func(ns string) error {
		s := &cnmsv1alpha1.SQL{
			Host: sqlTestHost, Database: testServerName, Username: testServerName,
			PasswordSecretRef: cnmsv1alpha1.SecretReference{Name: sqlTestSecret},
		}
		mutate(s)
		return k8sClient.Create(ctx, &cnmsv1alpha1.MailDirectory{
			ObjectMeta: metav1.ObjectMeta{Name: apiDirectoryName, Namespace: ns},
			Spec:       cnmsv1alpha1.MailDirectorySpec{Type: cnmsv1alpha1.DirectoryTypeSQL, SQL: s},
		})
	}
}

var _ = Describe("API validation of MailDirectory", func() {
	filter := func(f string) func(string) error {
		return createLDAP(func(l *cnmsv1alpha1.LDAP) { l.ActiveFilter = &f })
	}
	attrs := func(a cnmsv1alpha1.LDAPAttributes) func(string) error {
		return createLDAP(func(l *cnmsv1alpha1.LDAP) { l.Attributes = &a })
	}
	relations := func(users, aliases *cnmsv1alpha1.SQLRelation) func(string) error {
		return createSQL(func(s *cnmsv1alpha1.SQL) { s.Users, s.Aliases = users, aliases })
	}
	rel := func(name, schema string) *cnmsv1alpha1.SQLRelation {
		r := &cnmsv1alpha1.SQLRelation{Name: name}
		if schema != "" {
			r.Schema = &schema
		}
		return r
	}
	ca := func(withCA bool) *cnmsv1alpha1.SecretReference {
		if !withCA {
			return nil
		}
		return &cnmsv1alpha1.SecretReference{Name: "ca"}
	}
	ldapTLS := func(mode string, withCA bool) func(string) error {
		return createLDAP(func(l *cnmsv1alpha1.LDAP) {
			l.TLS = &cnmsv1alpha1.LDAPTLS{Mode: &mode, CASecretRef: ca(withCA)}
		})
	}
	sqlTLS := func(mode string, withCA bool) func(string) error {
		return createSQL(func(s *cnmsv1alpha1.SQL) {
			s.TLS = &cnmsv1alpha1.SQLTLS{Mode: &mode, CASecretRef: ca(withCA)}
		})
	}
	const (
		attrLoginField   = "spec.ldap.attributes.login"
		attrMailField    = "spec.ldap.attributes.mail"
		attrAliasesField = "spec.ldap.attributes.aliases"
		filterField      = "spec.ldap.activeFilter"
		usersNameField   = "spec.sql.users.name"
		usersSchemaField = "spec.sql.users.schema"
		aliasesNameField = "spec.sql.aliases.name"
	)
	runAPICases([]apiCase{
		{name: "accepts a host name with a dash", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) { l.Host = "ldap-1.example.org" })},
		{name: "accepts an IPv4 host", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) { l.Host = "203.0.113.5" })},
		{name: "rejects an LDAP host with a port", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) { l.Host = "ldap.example.org:389" }), wantErr: ldapHostField},
		{name: "rejects an LDAP host with a space", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) { l.Host = "ldap.example.org x" }), wantErr: ldapHostField},
		{name: "rejects an over-long host label", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) { l.Host = strings.Repeat("a", 64) + ".example.org" }), wantErr: ldapHostField},
		{name: "rejects an SQL host with a newline", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Host = "db.example.org\nx" }), wantErr: "spec.sql.host"},
		{name: "rejects a base DN with a newline", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) { l.BaseDN = "dc=example\nmaps = x" }), wantErr: "spec.ldap.baseDN"},
		{name: "rejects a bind DN with a control character", mutate: createLDAP(func(l *cnmsv1alpha1.LDAP) {
			dn := "cn=a\x01"
			l.BindDN = &dn
			l.BindSecretRef = &cnmsv1alpha1.SecretReference{Name: dirBindSecret}
		}), wantErr: "spec.ldap.bindDN"},
		{name: "accepts an LDAP CA with StartTLS", mutate: ldapTLS("StartTLS", true)},
		{name: "accepts an LDAP CA with LDAPS", mutate: ldapTLS("LDAPS", true)},
		{name: "accepts LDAP mode None without a CA", mutate: ldapTLS("None", false)},
		{name: "rejects an LDAP CA with mode None", mutate: ldapTLS("None", true), wantErr: "caSecretRef must not be set when mode is None"},
		{name: "accepts an SQL CA with verify-full", mutate: sqlTLS("verify-full", true)},
		{name: "accepts an SQL CA with require", mutate: sqlTLS("require", true)},
		{name: "accepts SQL mode disable without a CA", mutate: sqlTLS("disable", false)},
		{name: "rejects an SQL CA with mode disable", mutate: sqlTLS("disable", true), wantErr: "caSecretRef must not be set when mode is disable"},
		{name: "accepts an active filter", mutate: filter("(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))")},
		{name: "rejects an active filter with the old placeholder", mutate: filter("(mail={user})"), wantErr: filterField},
		{name: "rejects an active filter with Postfix syntax", mutate: filter("(mail=%s)"), wantErr: filterField},
		{name: "rejects an active filter with Dovecot syntax", mutate: filter("(mail=%{user})"), wantErr: filterField},
		{name: "rejects an active filter with a newline", mutate: filter("(mailEnabled=TRUE)\nresult_attribute = x"), wantErr: filterField},
		{name: "rejects an empty active filter", mutate: filter(""), wantErr: filterField},
		{name: "rejects the removed userFilter field", mutate: func(ns string) error {
			dir := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: apiDirectoryName, Namespace: ns},
				Spec: cnmsv1alpha1.MailDirectorySpec{Type: cnmsv1alpha1.DirectoryTypeLDAP,
					LDAP: &cnmsv1alpha1.LDAP{Host: ldapTestHost, BaseDN: ldapTestBaseDN}},
			}
			obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(dir)
			Expect(err).NotTo(HaveOccurred())
			u := &unstructured.Unstructured{Object: obj}
			u.SetGroupVersionKind(cnmsv1alpha1.GroupVersion.WithKind("MailDirectory"))
			Expect(unstructured.SetNestedField(u.Object, "(mail={user})", "spec", "ldap", "userFilter")).To(Succeed())
			return k8sClient.Create(ctx, u, client.FieldValidation("Strict"))
		}, wantErr: "unknown field \"spec.ldap.userFilter\""},
		{name: "accepts LDAP attributes with aliases", mutate: attrs(cnmsv1alpha1.LDAPAttributes{
			Login: new("uid"), Mail: new("mail"), Aliases: new("mailAlias"),
		})},
		{name: "accepts LDAP attributes without aliases", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Login: new("sAMAccountName")})},
		{name: "rejects an attribute name with a filter fragment", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Login: new("uid)(objectClass=*")}), wantErr: attrLoginField},
		{name: "rejects an attribute name with an equals sign", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Mail: new("mail=x")}), wantErr: attrMailField},
		{name: "rejects an attribute name with a space", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Aliases: new("mail alias")}), wantErr: attrAliasesField},
		{name: "rejects an attribute name that starts with a digit", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Login: new("1uid")}), wantErr: attrLoginField},
		{name: "rejects an attribute name with a semicolon", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Mail: new("mail;binary")}), wantErr: attrMailField},
		{name: "rejects an empty attribute name", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Aliases: new("")}), wantErr: attrAliasesField},
		{name: "rejects an over-long attribute name", mutate: attrs(cnmsv1alpha1.LDAPAttributes{Login: new("a" + strings.Repeat("b", 64))}), wantErr: attrLoginField},
		{name: "accepts SQL relations with schemas", mutate: relations(rel("mail_users", "directory"), rel(dirAliasesRelation, "directory"))},
		{name: "accepts SQL relations without schemas", mutate: relations(rel("v_users", ""), rel("_aliases", ""))},
		{name: "accepts no SQL relations", mutate: relations(nil, nil)},
		{name: "rejects an SQL relation name with a quote", mutate: relations(rel("users'--", ""), nil), wantErr: usersNameField},
		{name: "rejects an SQL relation name with a semicolon", mutate: relations(nil, rel("aliases;drop", "")), wantErr: aliasesNameField},
		{name: "rejects an SQL relation name with a dot", mutate: relations(rel("other.users", ""), nil), wantErr: usersNameField},
		{name: "rejects an SQL relation name in upper case", mutate: relations(rel("Users", ""), nil), wantErr: usersNameField},
		{name: "rejects an SQL relation name with a dash", mutate: relations(rel("mail-users", ""), nil), wantErr: usersNameField},
		{name: "rejects an SQL relation name with a leading digit", mutate: relations(rel("1users", ""), nil), wantErr: usersNameField},
		{name: "rejects an empty SQL relation name", mutate: relations(rel("", ""), nil), wantErr: usersNameField},
		{name: "rejects an over-long SQL relation name", mutate: relations(rel("a"+strings.Repeat("b", 63), ""), nil), wantErr: usersNameField},
		{name: "rejects an SQL schema with a dot", mutate: relations(rel("users", "a.b"), nil), wantErr: usersSchemaField},
		{name: "rejects an SQL schema with a space", mutate: relations(rel("users", "a b"), nil), wantErr: usersSchemaField},
		{name: "accepts an SQL database name with a dash", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Database = "mail-db_1" })},
		{name: "rejects an SQL database name with a space", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Database = "mail sslmode=disable" }), wantErr: "spec.sql.database"},
		{name: "rejects an SQL user with a quote", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Username = "mail'x" }), wantErr: "spec.sql.username"},
	})
})

var _ = Describe("Defaults of MailDirectory", func() {
	// apply creates the directory in a fresh namespace and reads it back,
	// so that the defaults of the API server show.
	apply := func(create func(string) error) *cnmsv1alpha1.MailDirectory {
		apiNSCounter++
		ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(create(ns)).To(Succeed())
		dir := &cnmsv1alpha1.MailDirectory{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: apiDirectoryName}, dir)).To(Succeed())
		return dir
	}

	It("defaults the login and mail attributes when attributes is set", func() {
		dir := apply(createLDAP(func(l *cnmsv1alpha1.LDAP) {
			l.Attributes = &cnmsv1alpha1.LDAPAttributes{Aliases: new("mailAlias")}
		}))
		attrs := dir.Spec.LDAP.Attributes
		Expect(attrs.Login).To(HaveValue(Equal("uid")))
		Expect(attrs.Mail).To(HaveValue(Equal("mail")))
		Expect(attrs.Aliases).To(HaveValue(Equal("mailAlias")))
		Expect(dir.Spec.LDAP.ActiveFilter).To(BeNil())
	})

	It("defaults the users relation and leaves aliases unset", func() {
		dir := apply(createSQL(func(*cnmsv1alpha1.SQL) {}))
		Expect(dir.Spec.SQL.Users).To(Equal(&cnmsv1alpha1.SQLRelation{Name: "cnms_users", Schema: new("public")}))
		Expect(dir.Spec.SQL.Aliases).To(BeNil())
	})

	It("defaults the schema of a relation", func() {
		dir := apply(createSQL(func(s *cnmsv1alpha1.SQL) {
			s.Aliases = &cnmsv1alpha1.SQLRelation{Name: dirAliasesRelation}
		}))
		Expect(dir.Spec.SQL.Aliases).To(Equal(&cnmsv1alpha1.SQLRelation{Name: dirAliasesRelation, Schema: new("public")}))
	})
})

var _ = Describe("API validation of MailDomain report addresses", func() {
	dmarcRua := func(addr string) func(string) error {
		return func(ns string) error {
			_, err := createDomain(ns, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.DMARC = &cnmsv1alpha1.DMARC{Rua: &addr}
			})
			return err
		}
	}
	tlsRptRua := func(addr string) func(string) error {
		return func(ns string) error {
			_, err := createDomain(ns, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.TLSRPT = &cnmsv1alpha1.TLSRPT{Rua: &addr}
			})
			return err
		}
	}
	runAPICases([]apiCase{
		{name: "accepts a plain DMARC rua address", mutate: dmarcRua("dmarc+reports@example.org")},
		{name: "rejects a DMARC rua with a mailto scheme", mutate: dmarcRua("mailto:a@example.org"), wantErr: dmarcRuaField},
		{name: "rejects a DMARC rua that injects a tag", mutate: dmarcRua("a@example.org; p=reject"), wantErr: dmarcRuaField},
		{name: "rejects a DMARC rua list", mutate: dmarcRua("a@example.org,b@example.org"), wantErr: dmarcRuaField},
		{name: "rejects a DMARC rua with a size limit", mutate: dmarcRua("a@example.org!10m"), wantErr: dmarcRuaField},
		{name: "rejects a DMARC ruf with a quote", mutate: func(ns string) error {
			addr := `a"@example.org`
			_, err := createDomain(ns, func(d *cnmsv1alpha1.MailDomain) {
				d.Spec.DMARC = &cnmsv1alpha1.DMARC{Ruf: &addr}
			})
			return err
		}, wantErr: "spec.dmarc.ruf"},
		{name: "accepts a TLS-RPT address", mutate: tlsRptRua("tls-rpt@example.org")},
		{name: "rejects a TLS-RPT address without a domain", mutate: tlsRptRua("tls-rpt"), wantErr: "spec.tlsRpt.rua"},
	})
})

var _ = Describe("API validation of domain names", func() {
	domain := func(name string) func(string) error {
		return func(ns string) error {
			_, err := createDomain(ns, func(d *cnmsv1alpha1.MailDomain) { d.Spec.Domain = name })
			return err
		}
	}
	// Grants are cluster-scoped and live beyond the namespace; the
	// accepted names are deleted again.
	grant := func(name string) func(string) error {
		return func(ns string) error {
			g := &cnmsv1alpha1.MailDomainGrant{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: ns},
			}
			err := k8sClient.Create(ctx, g)
			if err == nil {
				Expect(k8sClient.Delete(ctx, g)).To(Succeed())
			}
			return err
		}
	}
	longLabel := strings.Repeat("a", 64)
	okLabel := strings.Repeat("a", 63)
	runAPICases([]apiCase{
		{name: "accepts a MailDomain with a 63 character label", mutate: domain(okLabel + ".example.org")},
		{name: "accepts a MailDomain with an A-label top level domain", mutate: domain("example.xn--p1ai")},
		{name: "rejects a MailDomain that is an IPv4 address", mutate: domain(spfIP4Sample), wantErr: domainField},
		{name: "rejects a MailDomain with a 64 character label", mutate: domain(longLabel + ".example.org"), wantErr: domainField},
		{name: "rejects a MailDomain with an underscore", mutate: domain("a_b.example.org"), wantErr: domainField},
		{name: "accepts a MailDomainGrant with a 63 character label", mutate: grant(okLabel + ".example.org")},
		{name: "rejects a MailDomainGrant named like an IPv4 address", mutate: grant(spfIP4Sample), wantErr: "at least two labels"},
		{name: "rejects a MailDomainGrant with a 64 character label", mutate: grant(longLabel + ".example.org"), wantErr: "at least two labels"},
	})
})

var _ = Describe("API validation of DKIM rotation", func() {
	rotation := func(interval string, overlap *string) func(string) error {
		return func(ns string) error {
			// metav1.Duration cannot hold an invalid value, so the
			// object is sent as unstructured data.
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": cnmsv1alpha1.GroupVersion.String(),
				"kind":       "MailDomain",
				"metadata":   apiDomainMeta(ns),
				"spec": map[string]any{
					"domain":    testDomain,
					"serverRef": map[string]any{"name": apiServerName},
				},
			}}
			r := map[string]any{"interval": interval}
			if overlap != nil {
				r["overlap"] = *overlap
			}
			Expect(unstructured.SetNestedMap(u.Object, r, "spec", "dkim", "rotation")).To(Succeed())
			return k8sClient.Create(ctx, u)
		}
	}
	str := func(s string) *string { return &s }
	const rotationMsg = "interval must be at least 24h"
	runAPICases([]apiCase{
		{name: "accepts a 90 day interval with the default overlap", mutate: rotation("2160h", nil)},
		{name: "accepts an overlap shorter than the interval", mutate: rotation("720h", str("48h"))},
		{name: "rejects an interval below 24h", mutate: rotation("1h", str("1m")), wantErr: rotationMsg},
		{name: "rejects a negative interval", mutate: rotation("-48h", str("1h")), wantErr: rotationMsg},
		{name: "rejects an overlap equal to the interval", mutate: rotation("48h", str("48h")), wantErr: rotationMsg},
		{name: "rejects a zero overlap", mutate: rotation("720h", str("0s")), wantErr: "overlap must be positive"},
		{name: "rejects a negative overlap", mutate: rotation("720h", str("-1h")), wantErr: "overlap must be positive"},
		{name: "rejects the default overlap for a 24h interval", mutate: rotation("24h", nil), wantErr: rotationMsg},
		{name: "rejects the default overlap for a 5 day interval", mutate: rotation("120h", nil), wantErr: "set overlap explicitly"},
		{name: "rejects the default overlap for a 4 day interval", mutate: rotation("96h", nil), wantErr: "set overlap explicitly"},
		{name: "accepts a 4 day interval with an explicit overlap", mutate: rotation("96h", str("48h"))},
		{name: "accepts a 6 day interval with the default overlap", mutate: rotation("144h", nil)},
		{name: "rejects an interval that is not a duration", mutate: rotation("soon", nil), wantErr: "spec.dkim.rotation"},
	})

	It("defaults the overlap to 120h", func() {
		apiNSCounter++
		ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(rotation("720h", nil)(ns)).To(Succeed())
		d := &cnmsv1alpha1.MailDomain{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: apiDomainName}, d)).To(Succeed())
		Expect(d.Spec.DKIM.Rotation.Overlap.Duration).To(Equal(120 * time.Hour))
	})
})

var _ = Describe("API validation of DKIM keys", func() {
	key := func(selector string, signing *bool) cnmsv1alpha1.DKIMKey {
		return cnmsv1alpha1.DKIMKey{
			Selector:  selector,
			SecretRef: cnmsv1alpha1.SecretKeyReference{Name: "example-dkim", Key: selector + ".key"},
			Signing:   signing,
		}
	}
	dkim := func(d cnmsv1alpha1.DKIM) func(string) error {
		return func(ns string) error {
			_, err := createDomain(ns, func(md *cnmsv1alpha1.MailDomain) { md.Spec.DKIM = &d })
			return err
		}
	}
	keys := func(k ...cnmsv1alpha1.DKIMKey) func(string) error {
		return dkim(cnmsv1alpha1.DKIM{Keys: k})
	}
	rotation := &cnmsv1alpha1.DKIMRotation{Interval: metav1.Duration{Duration: 720 * time.Hour}}
	many := make([]cnmsv1alpha1.DKIMKey, 9)
	for i := range many {
		many[i] = key(fmt.Sprintf("s%d", i), nil)
	}
	const (
		excludesMsg = "keys excludes rotation, ed25519 and selectorPrefix"
		signingMsg  = "at least one key must have signing set to true"
		keysField   = "spec.dkim.keys"
	)
	runAPICases([]apiCase{
		{name: "accepts generated keys with Ed25519 and rotation", mutate: dkim(cnmsv1alpha1.DKIM{Ed25519: new(true), Rotation: rotation})},
		{name: "accepts one key that signs by default", mutate: keys(key("s2026a", nil))},
		{name: "accepts a signing key and a published one", mutate: keys(key("s2026a", new(true)), key("s2027a", new(false)))},
		{name: "accepts eight keys", mutate: keys(many[:8]...)},
		{name: "rejects nine keys", mutate: keys(many...), wantErr: keysField},
		{name: "rejects duplicate selectors", mutate: keys(key("s2026a", nil), key("s2026a", new(false))), wantErr: "Duplicate value"},
		{name: "rejects keys that do not sign", mutate: keys(key("s2026a", new(false)), key("s2027a", new(false))), wantErr: signingMsg},
		{name: "rejects keys with rotation", mutate: dkim(cnmsv1alpha1.DKIM{Keys: []cnmsv1alpha1.DKIMKey{key("s2026a", nil)}, Rotation: rotation}), wantErr: excludesMsg},
		{name: "rejects keys with ed25519 false", mutate: dkim(cnmsv1alpha1.DKIM{Keys: []cnmsv1alpha1.DKIMKey{key("s2026a", nil)}, Ed25519: new(false)}), wantErr: excludesMsg},
		{name: "rejects keys with a selector prefix", mutate: dkim(cnmsv1alpha1.DKIM{Keys: []cnmsv1alpha1.DKIMKey{key("s2026a", nil)}, SelectorPrefix: new("cnms")}), wantErr: excludesMsg},
		{name: "rejects a selector with an upper-case letter", mutate: keys(key("S2026", nil)), wantErr: dkimSelectorField},
		{name: "rejects a selector with a dot", mutate: keys(key("a.b", nil)), wantErr: dkimSelectorField},
		{name: "rejects a 64 character selector", mutate: keys(key(strings.Repeat("a", 64), nil)), wantErr: dkimSelectorField},
		{name: "rejects a Secret key with a slash", mutate: keys(cnmsv1alpha1.DKIMKey{
			Selector: "s2026a", SecretRef: cnmsv1alpha1.SecretKeyReference{Name: "example-dkim", Key: "../key"},
		}), wantErr: "spec.dkim.keys[0].secretRef.key"},
		{name: "rejects a Secret name with an upper-case letter", mutate: keys(cnmsv1alpha1.DKIMKey{
			Selector: "s2026a", SecretRef: cnmsv1alpha1.SecretKeyReference{Name: "Example", Key: "k"},
		}), wantErr: "spec.dkim.keys[0].secretRef.name"},
	})

	It("signs with a listed key by default", func() {
		apiNSCounter++
		ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(keys(key("s2026a", nil))(ns)).To(Succeed())
		d := &cnmsv1alpha1.MailDomain{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: apiDomainName}, d)).To(Succeed())
		Expect(d.Spec.DKIM.Keys[0].Signing).To(HaveValue(BeTrue()))
		Expect(d.Spec.DKIM.SelectorPrefix).To(BeNil())
	})
})

var _ = Describe("API validation of SPF, DMARC, MTA-STS and TLS-RPT", func() {
	spec := func(mutate func(*cnmsv1alpha1.MailDomainSpec)) func(string) error {
		return func(ns string) error {
			_, err := createDomain(ns, func(d *cnmsv1alpha1.MailDomain) { mutate(&d.Spec) })
			return err
		}
	}
	spf := func(s cnmsv1alpha1.SPF) func(string) error {
		return spec(func(ms *cnmsv1alpha1.MailDomainSpec) { ms.SPF = &s })
	}
	dmarc := func(d cnmsv1alpha1.DMARC) func(string) error {
		return spec(func(ms *cnmsv1alpha1.MailDomainSpec) { ms.DMARC = &d })
	}
	tlsRpt := func(rua, dmarcRua *string) func(string) error {
		return spec(func(ms *cnmsv1alpha1.MailDomainSpec) {
			ms.TLSRPT = &cnmsv1alpha1.TLSRPT{Enabled: new(true), Rua: rua}
			if dmarcRua != nil {
				ms.DMARC = &cnmsv1alpha1.DMARC{Rua: dmarcRua}
			}
		})
	}
	many := func(n int, f func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	v4 := "must be an IPv4 address or CIDR"
	v6 := "must be an IPv6 address or CIDR"
	runAPICases([]apiCase{
		{name: "accepts SPF sources and a qualifier", mutate: spf(cnmsv1alpha1.SPF{
			IP4: []string{sendIP4b, "198.51.100.0/24"}, IP6: []string{spfIP6Sample, "2001:db8::/32"},
			Include: []string{"_spf.example.net", "spf.example.com"}, Qualifier: new("fail"),
		})},
		{name: "rejects an IPv6 address in ip4", mutate: spf(cnmsv1alpha1.SPF{IP4: []string{spfIP6Sample}}), wantErr: v4},
		{name: "rejects a host name in ip4", mutate: spf(cnmsv1alpha1.SPF{IP4: []string{"host.example.net"}}), wantErr: v4},
		{name: "rejects an IPv4 prefix length above 32", mutate: spf(cnmsv1alpha1.SPF{IP4: []string{"203.0.113.0/33"}}), wantErr: v4},
		{name: "rejects an ip4 value that injects a mechanism", mutate: spf(cnmsv1alpha1.SPF{IP4: []string{"203.0.113.1 include:evil.example"}}), wantErr: v4},
		{name: "rejects an IPv4 address in ip6", mutate: spf(cnmsv1alpha1.SPF{IP6: []string{spfIP4Sample}}), wantErr: v6},
		{name: "rejects an IPv6 prefix length above 128", mutate: spf(cnmsv1alpha1.SPF{IP6: []string{"2001:db8::/129"}}), wantErr: v6},
		{name: "accepts 16 ip4 entries", mutate: spf(cnmsv1alpha1.SPF{IP4: many(16, func(i int) string { return fmt.Sprintf("203.0.113.%d", i) })})},
		{name: "rejects 17 ip4 entries", mutate: spf(cnmsv1alpha1.SPF{IP4: many(17, func(i int) string { return fmt.Sprintf("203.0.113.%d", i) })}), wantErr: "spec.spf.ip4"},
		{name: "accepts 5 includes", mutate: spf(cnmsv1alpha1.SPF{Include: many(5, func(i int) string { return fmt.Sprintf("spf%d.example.net", i) })})},
		{name: "rejects 6 includes", mutate: spf(cnmsv1alpha1.SPF{Include: many(6, func(i int) string { return fmt.Sprintf("spf%d.example.net", i) })}), wantErr: spfIncludeField},
		{name: "rejects an include with a space", mutate: spf(cnmsv1alpha1.SPF{Include: []string{"a.example.net -all"}}), wantErr: spfIncludeField},
		{name: "rejects an include with a macro", mutate: spf(cnmsv1alpha1.SPF{Include: []string{"%{i}.example.net"}}), wantErr: spfIncludeField},
		{name: "rejects an include without a dot", mutate: spf(cnmsv1alpha1.SPF{Include: []string{"example"}}), wantErr: spfIncludeField},
		{name: "rejects an unknown qualifier", mutate: spf(cnmsv1alpha1.SPF{Qualifier: new("pass")}), wantErr: "spec.spf.qualifier"},

		{name: "accepts subdomain policies", mutate: dmarc(cnmsv1alpha1.DMARC{SubdomainPolicy: new("reject"), NonExistentSubdomainPolicy: new("quarantine")})},
		{name: "rejects an unknown subdomain policy", mutate: dmarc(cnmsv1alpha1.DMARC{SubdomainPolicy: new("block")}), wantErr: "spec.dmarc.subdomainPolicy"},
		{name: "rejects an unknown non-existent subdomain policy", mutate: dmarc(cnmsv1alpha1.DMARC{NonExistentSubdomainPolicy: new("")}), wantErr: "spec.dmarc.nonExistentSubdomainPolicy"},

		{name: "accepts an MTA-STS mode", mutate: spec(func(ms *cnmsv1alpha1.MailDomainSpec) {
			ms.MTAStS = &cnmsv1alpha1.MTAStS{Enabled: new(true), Mode: new("enforce")}
		})},
		{name: "rejects an unknown MTA-STS mode", mutate: spec(func(ms *cnmsv1alpha1.MailDomainSpec) {
			ms.MTAStS = &cnmsv1alpha1.MTAStS{Mode: new("report")}
		}), wantErr: "spec.mtaSts.mode"},

		{name: "accepts enabled TLS-RPT with its own address", mutate: tlsRpt(new("tls@example.org"), nil)},
		{name: "accepts enabled TLS-RPT with the DMARC address", mutate: tlsRpt(nil, new("dmarc@example.org"))},
		{name: "rejects enabled TLS-RPT without an address", mutate: tlsRpt(nil, nil), wantErr: "tlsRpt.enabled requires a report address"},
	})

	It("applies the defaults of SPF and MTA-STS", func() {
		apiNSCounter++
		ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(spec(func(ms *cnmsv1alpha1.MailDomainSpec) {
			ms.SPF = &cnmsv1alpha1.SPF{IP4: []string{spfIP4Sample}}
			ms.MTAStS = &cnmsv1alpha1.MTAStS{}
		})(ns)).To(Succeed())
		d := &cnmsv1alpha1.MailDomain{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: apiDomainName}, d)).To(Succeed())
		Expect(d.Spec.SPF.Qualifier).To(HaveValue(Equal("softfail")))
		Expect(d.Spec.MTAStS.Mode).To(HaveValue(Equal("testing")))
	})
})

var _ = Describe("API validation of the MailDomain name", func() {
	named := func(name string) func(string) error {
		return func(ns string) error {
			d := &cnmsv1alpha1.MailDomain{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: cnmsv1alpha1.MailDomainSpec{
					Domain:    testDomain,
					ServerRef: cnmsv1alpha1.NameReference{Name: apiServerName},
				},
			}
			return k8sClient.Create(ctx, d)
		}
	}
	runAPICases([]apiCase{
		{name: "accepts a 63 character name", mutate: named(strings.Repeat("a", 63))},
		{name: "rejects a 64 character name", mutate: named(strings.Repeat("a", 64)), wantErr: "at most 63 characters"},
	})
})

var _ = Describe("API validation of MailDomain DKIM status", func() {
	runAPICases([]apiCase{{
		name: "accepts active, pending and retiring keys",
		mutate: func(ns string) error {
			d, err := createDomain(ns, nil)
			Expect(err).NotTo(HaveOccurred())
			now := metav1.Now()
			d.Status.DKIM = &cnmsv1alpha1.DKIMStatus{
				Active: []cnmsv1alpha1.DKIMKeyStatus{
					{Selector: "cnms-20261103", Algorithm: algRSA, KeyCreated: &now},
					{Selector: "cnms-ed25519-20261103", Algorithm: "ed25519", KeyCreated: &now},
				},
				Pending:      []cnmsv1alpha1.DKIMKeyStatus{{Selector: "cnms-20261203", Algorithm: algRSA, KeyCreated: &now}},
				Retiring:     []cnmsv1alpha1.DKIMKeyStatus{{Selector: "cnms-20260803", Algorithm: algRSA, KeyCreated: &now, Until: &now}},
				NextRotation: &now,
			}
			return k8sClient.Status().Update(ctx, d)
		},
	}, {
		name: "rejects a key with an unknown algorithm",
		mutate: func(ns string) error {
			d, err := createDomain(ns, nil)
			Expect(err).NotTo(HaveOccurred())
			d.Status.DKIM = &cnmsv1alpha1.DKIMStatus{
				Active: []cnmsv1alpha1.DKIMKeyStatus{{Selector: "cnms-20261103", Algorithm: "ecdsa"}},
			}
			return k8sClient.Status().Update(ctx, d)
		},
		wantErr: "status.dkim.active[0].algorithm",
	}})
})

var _ = Describe("API validation of storage and topology spread", func() {
	const positiveMsg = "must be greater than zero"
	qty := func(s string) *resource.Quantity { return new(resource.MustParse(s)) }
	mailboxes := func(storage, quota string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true), Storage: qty(storage)}
			if quota != "" {
				ms.Spec.Mailboxes.Quota = qty(quota)
			}
		})
	}
	queue := func(size string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Postfix = &cnmsv1alpha1.Postfix{QueueStorage: qty(size)}
		})
	}
	valkey := func(size string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Valkey = &cnmsv1alpha1.Valkey{Persistence: new(true), Storage: qty(size)}
		})
	}
	spread := func(skew int32, key string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Availability = &cnmsv1alpha1.Availability{
				TopologySpread: &cnmsv1alpha1.TopologySpread{MaxSkew: &skew, TopologyKey: &key},
			}
		})
	}
	runAPICases([]apiCase{
		{name: "accepts positive mailbox storage and quota", mutate: mailboxes("20Gi", "1Gi")},
		{name: "rejects zero mailbox storage", mutate: mailboxes("0", ""), wantErr: positiveMsg},
		{name: "rejects a zero quota", mutate: mailboxes("10Gi", "0"), wantErr: positiveMsg},
		{name: "accepts a positive queue size", mutate: queue("2Gi")},
		{name: "rejects a zero queue size", mutate: queue("0"), wantErr: positiveMsg},
		{name: "accepts a positive valkey size", mutate: valkey("500Mi")},
		{name: "rejects a zero valkey size", mutate: valkey("0Gi"), wantErr: positiveMsg},
		{name: "accepts a zone topology key", mutate: spread(2, "topology.kubernetes.io/zone")},
		{name: "rejects a zero maxSkew", mutate: spread(0, "kubernetes.io/hostname"), wantErr: "spec.availability.topologySpread.maxSkew"},
		{name: "rejects a topology key with a space", mutate: spread(1, "my zone"), wantErr: "spec.availability.topologySpread.topologyKey"},
		{name: "rejects a topology key ending in a slash", mutate: spread(1, "example.org/"), wantErr: "spec.availability.topologySpread.topologyKey"},
	})
})

var _ = Describe("API validation of references", func() {
	serverRef := func(name string) func(string) error {
		return func(ns string) error {
			_, err := createDomain(ns, func(d *cnmsv1alpha1.MailDomain) { d.Spec.ServerRef.Name = name })
			return err
		}
	}
	noServerRef := func(ns string) error {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": cnmsv1alpha1.GroupVersion.String(),
			"kind":       "MailDomain",
			"metadata":   apiDomainMeta(ns),
			"spec":       map[string]any{"domain": testDomain},
		}}
		return k8sClient.Create(ctx, u)
	}
	tlsSecret := func(name string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) { ms.Spec.TLS.SecretName = &name })
	}
	bindSecret := func(name string) func(string) error {
		return createLDAP(func(l *cnmsv1alpha1.LDAP) {
			dn := "cn=lookup,dc=example,dc=org"
			l.BindDN = &dn
			l.BindSecretRef = &cnmsv1alpha1.SecretReference{Name: name}
		})
	}
	runAPICases([]apiCase{
		{name: "accepts a server reference with dots", mutate: serverRef("mail.example")},
		{name: "rejects a MailDomain without a server reference", mutate: noServerRef, wantErr: "spec.serverRef"},
		{name: "rejects a server reference with an upper-case letter", mutate: serverRef("Mail"), wantErr: serverRefField},
		{name: "rejects a server reference with an empty label", mutate: serverRef("mail..example"), wantErr: serverRefField},
		{name: "rejects a server reference ending in a dot", mutate: serverRef("mail."), wantErr: serverRefField},
		{name: "rejects an empty server reference", mutate: serverRef(""), wantErr: serverRefField},
		{name: "accepts a TLS secret name with dots", mutate: tlsSecret("example.org-tls")},
		{name: "rejects a TLS secret name with an underscore", mutate: tlsSecret("example_org"), wantErr: "spec.tls.secretName"},
		{name: "accepts a bind secret name with dots", mutate: bindSecret("ldap.bind")},
		{name: "rejects a bind secret name starting with a dot", mutate: bindSecret(".ldap"), wantErr: "spec.ldap.bindSecretRef.name"},
	})
})

var _ = Describe("API validation of the MailServer hostname and SPF sources", func() {
	hostname := func(name string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) { ms.Spec.Hostname = name })
	}
	spf := func(s cnmsv1alpha1.ServerSPF) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) { ms.Spec.SPF = &s })
	}
	rename := func(to string) func(string) error {
		return func(ns string) error {
			ms := validMailServer(apiServerName)
			ms.Namespace = ns
			Expect(k8sClient.Create(ctx, ms)).To(Succeed())
			ms.Spec.Hostname = to
			return k8sClient.Update(ctx, ms)
		}
	}
	const hostnameField = "spec.hostname"
	longLabel := strings.Repeat("a", 64)
	runAPICases([]apiCase{
		{name: "accepts a hostname with a 63 character label", mutate: hostname(strings.Repeat("a", 63) + ".example.org")},
		{name: "rejects a MailServer without a hostname", mutate: hostname(""), wantErr: hostnameField},
		{name: "rejects a single label hostname", mutate: hostname("mail"), wantErr: hostnameField},
		{name: "rejects an IPv4 address as hostname", mutate: hostname(spfIP4Sample), wantErr: hostnameField},
		{name: "rejects an upper case hostname", mutate: hostname("Mail.example.org"), wantErr: hostnameField},
		{name: "rejects a hostname with a trailing dot", mutate: hostname("mail.example.org."), wantErr: hostnameField},
		{name: "rejects a hostname with a 64 character label", mutate: hostname(longLabel + ".example.org"), wantErr: hostnameField},
		{name: "rejects a hostname with an underscore", mutate: hostname("a_b.example.org"), wantErr: hostnameField},
		{name: "accepts renaming the hostname", mutate: rename("mail2.example.org")},
		{name: "rejects renaming the hostname to an invalid name", mutate: rename("mail"), wantErr: hostnameField},
		{name: "accepts SPF sources", mutate: spf(cnmsv1alpha1.ServerSPF{
			IP4: []string{sendIP4, "198.51.100.0/24"}, IP6: []string{"2001:db8::/32"},
		})},
		{name: "rejects an IPv6 address in ip4", mutate: spf(cnmsv1alpha1.ServerSPF{IP4: []string{spfIP6Sample}}), wantErr: "IPv4 address or CIDR"},
		{name: "rejects an IPv4 address in ip6", mutate: spf(cnmsv1alpha1.ServerSPF{IP6: []string{spfIP4Sample}}), wantErr: "IPv6 address or CIDR"},
		{name: "rejects an ip4 value that injects a mechanism", mutate: spf(cnmsv1alpha1.ServerSPF{IP4: []string{"203.0.113.1 include:evil.example"}}), wantErr: "IPv4 address or CIDR"},
		{name: "rejects 17 ip4 entries", mutate: spf(cnmsv1alpha1.ServerSPF{IP4: func() []string {
			out := make([]string, 17)
			for i := range out {
				out[i] = fmt.Sprintf("203.0.113.%d", i)
			}
			return out
		}()}), wantErr: "spec.spf.ip4"},
	})
})

var _ = Describe("API validation of MailDomain updates", func() {
	update := func(domain string) func(string) error {
		return func(ns string) error {
			d, err := createDomain(ns, nil)
			Expect(err).NotTo(HaveOccurred())
			d.Spec.Domain = domain
			return k8sClient.Update(ctx, d)
		}
	}
	move := func(ns string) error {
		d, err := createDomain(ns, nil)
		Expect(err).NotTo(HaveOccurred())
		d.Spec.ServerRef.Name = otherServer
		return k8sClient.Update(ctx, d)
	}
	runAPICases([]apiCase{
		{name: "accepts an update that keeps the domain", mutate: update(testDomain)},
		{name: "rejects changing the domain", mutate: update("example.net"), wantErr: "domain is immutable"},
		{name: "accepts moving the domain to another server", mutate: move},
	})
})

var _ = Describe("API validation of Valkey persistence", func() {
	valkey := func(persistence *bool, storage *resource.Quantity) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Valkey = &cnmsv1alpha1.Valkey{Persistence: persistence, Storage: storage}
		})
	}
	size := new(resource.MustParse("1Gi"))
	runAPICases([]apiCase{
		{name: "accepts persistence with storage", mutate: valkey(new(true), size)},
		{name: "accepts no persistence and no storage", mutate: valkey(new(false), nil)},
		{name: "rejects persistence without storage", mutate: valkey(new(true), nil), wantErr: "storage is required"},
		{name: "rejects storage without persistence", mutate: valkey(new(false), size), wantErr: "storage is only used"},
		{name: "rejects storage with the default persistence", mutate: valkey(nil, size), wantErr: "storage is only used"},
	})
})

// printerCell returns the cell of a printer column of one object, as
// kubectl get shows it.
func printerCell(ns, plural, name, column string) any {
	c := rest.CopyConfig(cfg)
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	restClient, err := rest.UnversionedRESTClientFor(c)
	Expect(err).NotTo(HaveOccurred())
	raw, err := restClient.Get().
		AbsPath("/apis/cnms.rpp.gmbh/v1alpha1/namespaces/"+ns+"/"+plural+"/"+name).
		SetHeader("Accept", "application/json;as=Table;g=meta.k8s.io;v=v1").
		DoRaw(ctx)
	Expect(err).NotTo(HaveOccurred())

	// Only the parts of the Table the check needs.
	var table struct {
		ColumnDefinitions []struct{ Name string }
		Rows              []struct{ Cells []any }
	}
	Expect(json.Unmarshal(raw, &table)).To(Succeed())
	Expect(table.Rows).To(HaveLen(1))
	for i, col := range table.ColumnDefinitions {
		if col.Name == column {
			return table.Rows[0].Cells[i]
		}
	}
	Fail("no " + column + " column")
	return nil
}

func newAPINamespace() string {
	apiNSCounter++
	ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
	return ns
}

var _ = Describe("printer columns", func() {
	It("shows the relay of a relay MailServer", func() {
		ns := newAPINamespace()
		Expect(createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Outbound = cnmsv1alpha1.Outbound{
				Relay: &cnmsv1alpha1.RelayOutbound{RelayRef: cnmsv1alpha1.NameReference{Name: "smarthost"}},
			}
		})(ns)).To(Succeed())
		Expect(printerCell(ns, "mailservers", apiServerName, "Relay")).To(Equal("smarthost"))
	})

	It("shows the server of a MailDomain", func() {
		ns := newAPINamespace()
		_, err := createDomain(ns, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(printerCell(ns, "maildomains", apiDomainName, "Server")).To(Equal(apiServerName))
	})
})

// Names shared by the MailUser cases.
const (
	apiUserName    = "alice"
	apiUserAddress = "alice@example.org"
	apiNetDomain   = "dom-net"
	userAliasField = "spec.aliases[0].localPart"
	userNameField  = "metadata.name"
	specKey        = "spec"
	serverRefKey   = "serverRef"
	domainRefKey   = "domainRef"
	refNameKey     = "name"
	refNSKey       = "namespace"
)

// validUser is a MailUser called name in ns that the API server accepts.
func validUser(ns, name string) *cnmsv1alpha1.MailUser {
	return &cnmsv1alpha1.MailUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cnmsv1alpha1.MailUserSpec{
			ServerRef: cnmsv1alpha1.NameReference{Name: apiServerName},
			DomainRef: cnmsv1alpha1.NameReference{Name: apiDomainName},
		},
	}
}

// createUserObject is the unstructured form of validUser(ns, apiUserName).
func createUserObject(ns string) (map[string]any, error) {
	return runtime.DefaultUnstructuredConverter.ToUnstructured(validUser(ns, apiUserName))
}

// aliasOf is an alias in the MailDomain dom (example.org).
func aliasOf(localPart string) cnmsv1alpha1.MailUserAlias {
	return cnmsv1alpha1.MailUserAlias{LocalPart: localPart, DomainRef: cnmsv1alpha1.NameReference{Name: apiDomainName}}
}

// createUser creates a valid MailUser called name in ns after applying
// mutate, and returns it with the create error.
func createUser(ns, name string, mutate func(*cnmsv1alpha1.MailUser)) (*cnmsv1alpha1.MailUser, error) {
	u := validUser(ns, name)
	if mutate != nil {
		mutate(u)
	}
	return u, k8sClient.Create(ctx, u)
}

var _ = Describe("API validation of MailUser", func() {
	named := func(name string) func(string) error {
		return func(ns string) error {
			_, err := createUser(ns, name, nil)
			return err
		}
	}
	aliases := func(a ...cnmsv1alpha1.MailUserAlias) func(string) error {
		return func(ns string) error {
			_, err := createUser(ns, apiUserName, func(u *cnmsv1alpha1.MailUser) { u.Spec.Aliases = a })
			return err
		}
	}
	localPart := func(lp string) func(string) error { return aliases(aliasOf(lp)) }
	many := func(n int) func(string) error {
		list := make([]cnmsv1alpha1.MailUserAlias, n)
		for i := range list {
			list[i] = aliasOf(fmt.Sprintf("a%d", i))
		}
		return aliases(list...)
	}
	// raw sends a valid user as unstructured data after edit changed
	// its spec, so a missing or unknown field reaches the API server;
	// strict validation rejects the unknown ones that pruning would
	// drop silently.
	raw := func(edit func(spec map[string]any)) func(string) error {
		return func(ns string) error {
			typed, err := createUserObject(ns)
			Expect(err).NotTo(HaveOccurred())
			u := &unstructured.Unstructured{Object: typed}
			u.SetGroupVersionKind(cnmsv1alpha1.GroupVersion.WithKind("MailUser"))
			edit(u.Object[specKey].(map[string]any))
			return k8sClient.Create(ctx, u, client.FieldValidation("Strict"))
		}
	}
	netAlias := cnmsv1alpha1.MailUserAlias{LocalPart: apiUserName, DomainRef: cnmsv1alpha1.NameReference{Name: apiNetDomain}}

	runAPICases([]apiCase{
		{name: "accepts a user without aliases", mutate: named(apiUserName)},
		{name: "accepts aliases in two domains", mutate: aliases(aliasOf("sales"), netAlias)},
		{name: "accepts the primary local part as an alias in another domain", mutate: aliases(netAlias)},
		{name: "accepts 32 aliases", mutate: many(32)},
		{name: "accepts a name with dots", mutate: named("alice.smith")},
		{name: "accepts the name sa", mutate: named("sa")},
		{name: "accepts a 64 character name", mutate: named(strings.Repeat("a", 64))},
		{name: "accepts a password Secret", mutate: func(ns string) error {
			_, err := createUser(ns, apiUserName, func(u *cnmsv1alpha1.MailUser) {
				u.Spec.PasswordSecretRef = &cnmsv1alpha1.SecretReference{Name: userSecret}
			})
			return err
		}},
		{name: "rejects a duplicate alias", mutate: aliases(aliasOf("sales"), aliasOf("sales")), wantErr: "aliases must be unique"},
		{name: "rejects an alias equal to the primary address", mutate: aliases(aliasOf(apiUserName)), wantErr: "must not be the primary address"},
		{name: "rejects more than 32 aliases", mutate: many(33), wantErr: "spec.aliases"},
		{name: "rejects a name that starts with sa.", mutate: named("sa.abc"), wantErr: "reserved for service accounts"},
		{name: "rejects a 65 character name", mutate: named(strings.Repeat("a", 65)), wantErr: "at most 64 characters"},
		{name: "rejects an upper-case name", mutate: named("Alice"), wantErr: userNameField},
		{name: "rejects a name with an underscore", mutate: named("alice_smith"), wantErr: userNameField},
		{name: "rejects a name with a colon", mutate: named("alice:smith"), wantErr: userNameField},
		{name: "rejects a name with a slash", mutate: named("alice/smith"), wantErr: userNameField},
		{name: "rejects a name starting with a dot", mutate: named(".alice"), wantErr: userNameField},
		{name: "rejects a name with two dots", mutate: named("alice..smith"), wantErr: userNameField},
		{name: "rejects a name ending with a hyphen", mutate: named("alice-"), wantErr: userNameField},
		{name: "accepts a 64 character local part", mutate: localPart(strings.Repeat("a", 64))},
		{name: "accepts dot, underscore and hyphen inside a local part", mutate: localPart("a.b_c-d")},
		{name: "rejects a 65 character local part", mutate: localPart(strings.Repeat("a", 65)), wantErr: userAliasField},
		{name: "rejects an empty local part", mutate: localPart(""), wantErr: userAliasField},
		{name: "rejects an upper-case local part", mutate: localPart("Sales"), wantErr: userAliasField},
		{name: "rejects a local part with two dots", mutate: localPart("a..b"), wantErr: "two consecutive dots"},
		{name: "rejects a local part starting with a dot", mutate: localPart(".a"), wantErr: userAliasField},
		{name: "rejects a local part ending with a dot", mutate: localPart("a."), wantErr: userAliasField},
		{name: "rejects a local part starting with a hyphen", mutate: localPart("-a"), wantErr: userAliasField},
		{name: "rejects a local part with a plus", mutate: localPart("a+b"), wantErr: userAliasField},
		{name: "rejects a local part with an at sign", mutate: localPart("a@b"), wantErr: userAliasField},
		{name: "rejects a local part with a colon", mutate: localPart("a:b"), wantErr: userAliasField},
		{name: "rejects a local part with a slash", mutate: localPart("a/b"), wantErr: userAliasField},
		{name: "rejects a local part with a blank", mutate: localPart("a b"), wantErr: userAliasField},
		{name: "rejects a local part that is not ASCII", mutate: localPart("jürgen"), wantErr: userAliasField},
		{name: "rejects a user without a server reference", mutate: raw(func(s map[string]any) {
			delete(s, serverRefKey)
		}), wantErr: "spec.serverRef"},
		{name: "rejects a user without a domain reference", mutate: raw(func(s map[string]any) {
			delete(s, domainRefKey)
		}), wantErr: "spec.domainRef"},
		{name: "rejects an alias without a domain reference", mutate: raw(func(s map[string]any) {
			s["aliases"] = []any{map[string]any{"localPart": "sales"}}
		}), wantErr: "spec.aliases[0].domainRef"},
		{name: "rejects a domain reference with a namespace prefix", mutate: raw(func(s map[string]any) {
			s[domainRefKey] = map[string]any{refNameKey: "elsewhere/" + apiDomainName}
		}), wantErr: "spec.domainRef.name"},
		{name: "rejects a namespace field in a reference", mutate: raw(func(s map[string]any) {
			s[domainRefKey] = map[string]any{refNameKey: apiDomainName, refNSKey: "elsewhere"}
		}), wantErr: "namespace"},
		{name: "rejects an unknown field", mutate: raw(func(s map[string]any) {
			s["quota"] = "1Gi"
		}), wantErr: "quota"},
	})
})

var _ = Describe("API validation of MailUser status", func() {
	status := func(addrs ...cnmsv1alpha1.MailUserAddressStatus) func(string) error {
		return func(ns string) error {
			u, err := createUser(ns, apiUserName, nil)
			Expect(err).NotTo(HaveOccurred())
			u.Status.Address = apiUserAddress
			u.Status.Addresses = addrs
			u.Status.CredentialSecretName = "alice-credential"
			u.Status.CredentialRevision = "12345"
			return k8sClient.Status().Update(ctx, u)
		}
	}
	primary := cnmsv1alpha1.MailUserAddressStatus{
		Address: apiUserAddress, Kind: cnmsv1alpha1.AddressKindPrimary, Served: true, Reason: "Served",
	}
	runAPICases([]apiCase{
		{name: "accepts the addresses of a user", mutate: status(primary, cnmsv1alpha1.MailUserAddressStatus{
			Address: "sales@example.net", Kind: cnmsv1alpha1.AddressKindAlias, Reason: "AliasDomainNotFound",
		})},
		{name: "rejects an address of an unknown kind", mutate: status(cnmsv1alpha1.MailUserAddressStatus{
			Address: apiUserAddress, Kind: "bogus", Reason: "Served",
		}), wantErr: "status.addresses[0].kind"},
		{name: "rejects the same address twice", mutate: status(primary, primary), wantErr: "status.addresses"},
	})
})

var _ = Describe("MailUser printer columns", func() {
	It("shows the server of a MailUser", func() {
		ns := newAPINamespace()
		_, err := createUser(ns, apiUserName, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(printerCell(ns, "mailusers", apiUserName, "Server")).To(Equal(apiServerName))
	})
})
