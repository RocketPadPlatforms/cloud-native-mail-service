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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// These tests send objects to the API server to check the schema and
// CEL rules of the CRDs: a valid object is accepted, an invalid one is
// rejected with a message naming the field or the rule.

var apiNSCounter int

// Names shared by the cases of this file.
const (
	apiDomainName    = "dom"
	apiDirectoryName = "dir"
	mtaStsName       = "mta-sts." + testDomain
	ldapHostField    = "spec.ldap.host"
	dmarcRuaField    = "spec.dmarc.rua"
	domainField      = "spec.domain"
	domainRefField   = "spec.domains[0].name"
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
		ms := validMailServer("srv")
		ms.Namespace = ns
		mutate(ms)
		return k8sClient.Create(ctx, ms)
	}
}

// createDomain creates a valid MailDomain "dom" for example.org in ns
// after applying mutate, and returns it with the create error.
func createDomain(ns string, mutate func(*cnmsv1alpha1.MailDomain)) (*cnmsv1alpha1.MailDomain, error) {
	d := &cnmsv1alpha1.MailDomain{
		ObjectMeta: metav1.ObjectMeta{Name: apiDomainName, Namespace: ns},
		Spec:       cnmsv1alpha1.MailDomainSpec{Domain: testDomain},
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
					{Name: mtaStsName, Type: "A", Value: "203.0.113.10"},
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
			Host: "db.example.org", Database: testServerName, Username: testServerName,
			PasswordSecretRef: cnmsv1alpha1.SecretReference{Name: "db-password"},
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
		return createLDAP(func(l *cnmsv1alpha1.LDAP) { l.UserFilter = &f })
	}
	const placeholderMsg = "exactly once"
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
			l.BindSecretRef = &cnmsv1alpha1.SecretReference{Name: "bind"}
		}), wantErr: "spec.ldap.bindDN"},
		{name: "accepts a filter with one placeholder", mutate: filter("(&(objectClass=person)(mail={user}))")},
		{name: "rejects a filter without a placeholder", mutate: filter("(objectClass=person)"), wantErr: placeholderMsg},
		{name: "rejects a filter with two placeholders", mutate: filter("(|(mail={user})(uid={user}))"), wantErr: placeholderMsg},
		{name: "rejects a filter with Postfix syntax", mutate: filter("(mail=%s)"), wantErr: "spec.ldap.userFilter"},
		{name: "rejects a filter with a newline", mutate: filter("(mail={user})\nresult_attribute = x"), wantErr: "spec.ldap.userFilter"},
		{name: "accepts an SQL database name with a dash", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Database = "mail-db_1" })},
		{name: "rejects an SQL database name with a space", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Database = "mail sslmode=disable" }), wantErr: "spec.sql.database"},
		{name: "rejects an SQL user with a quote", mutate: createSQL(func(s *cnmsv1alpha1.SQL) { s.Username = "mail'x" }), wantErr: "spec.sql.username"},
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
		{name: "rejects a MailDomain that is an IPv4 address", mutate: domain("203.0.113.1"), wantErr: domainField},
		{name: "rejects a MailDomain with a 64 character label", mutate: domain(longLabel + ".example.org"), wantErr: domainField},
		{name: "rejects a MailDomain with an underscore", mutate: domain("a_b.example.org"), wantErr: domainField},
		{name: "accepts a MailDomainGrant with a 63 character label", mutate: grant(okLabel + ".example.org")},
		{name: "rejects a MailDomainGrant named like an IPv4 address", mutate: grant("203.0.113.1"), wantErr: "at least two labels"},
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
				"metadata":   map[string]any{"name": apiDomainName, "namespace": ns},
				"spec":       map[string]any{"domain": testDomain},
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
		{name: "rejects the default overlap for a 24h interval", mutate: rotation("24h", nil), wantErr: rotationMsg},
		{name: "rejects an interval that is not a duration", mutate: rotation("soon", nil), wantErr: "spec.dkim.rotation"},
	})
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
	domains := func(name string) func(string) error {
		return createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Domains = []cnmsv1alpha1.NameReference{{Name: name}}
		})
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
		{name: "accepts a domain reference named after the domain", mutate: domains(testDomain)},
		{name: "rejects a reference with an upper-case letter", mutate: domains("Example.org"), wantErr: domainRefField},
		{name: "rejects a reference with an empty label", mutate: domains("example..org"), wantErr: domainRefField},
		{name: "rejects a reference ending in a dot", mutate: domains("example.org."), wantErr: domainRefField},
		{name: "accepts a TLS secret name with dots", mutate: tlsSecret("example.org-tls")},
		{name: "rejects a TLS secret name with an underscore", mutate: tlsSecret("example_org"), wantErr: "spec.tls.secretName"},
		{name: "accepts a bind secret name with dots", mutate: bindSecret("ldap.bind")},
		{name: "rejects a bind secret name starting with a dot", mutate: bindSecret(".ldap"), wantErr: "spec.ldap.bindSecretRef.name"},
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
	runAPICases([]apiCase{
		{name: "accepts an update that keeps the domain", mutate: update(testDomain)},
		{name: "rejects changing the domain", mutate: update("example.net"), wantErr: "domain is immutable"},
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

var _ = Describe("MailServer printer columns", func() {
	It("shows the relay of a relay server", func() {
		apiNSCounter++
		ns := fmt.Sprintf("cnms-api-%d", apiNSCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(createServer(func(ms *cnmsv1alpha1.MailServer) {
			ms.Spec.Outbound = cnmsv1alpha1.Outbound{
				Relay: &cnmsv1alpha1.RelayOutbound{RelayRef: cnmsv1alpha1.NameReference{Name: "smarthost"}},
			}
		})(ns)).To(Succeed())

		c := rest.CopyConfig(cfg)
		c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
		restClient, err := rest.UnversionedRESTClientFor(c)
		Expect(err).NotTo(HaveOccurred())
		raw, err := restClient.Get().
			AbsPath("/apis/cnms.rpp.gmbh/v1alpha1/namespaces/"+ns+"/mailservers/srv").
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
			if col.Name == "Relay" {
				Expect(table.Rows[0].Cells[i]).To(Equal("smarthost"))
				return
			}
		}
		Fail("no Relay column")
	})
})
