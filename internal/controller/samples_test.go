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
	"fmt"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// These tests validate the bundled sample CRs against the generated
// CRDs: schema, defaulting and CEL rules are enforced by the API
// server on create.

// Each spec gets its own namespace so that the asynchronous namespace
// deletion of one spec cannot collide with the creation of the next.
var (
	sampleNamespace string
	sampleNSCounter int
)

// LDAP test fixtures shared by the MailDirectory tests.
const (
	ldapTestHost   = "ldap.example.org"
	ldapTestBaseDN = "ou=people,dc=example,dc=org"
)

var _ = Describe("CRD samples", func() {
	BeforeEach(func() {
		sampleNSCounter++
		sampleNamespace = fmt.Sprintf("cnms-samples-%d", sampleNSCounter)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sampleNamespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	})

	AfterEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sampleNamespace}}
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
	})

	When("applying the samples from config/samples", func() {
		It("accepts every sample file", func() {
			files, err := filepath.Glob(filepath.Join("..", "..", "config", "samples", "cnms_v1alpha1_*.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(files).NotTo(BeEmpty())
			slices.Sort(files)

			var clusterScoped []string
			defer func() {
				for _, name := range clusterScoped {
					grant := &cnmsv1alpha1.MailDomainGrant{ObjectMeta: metav1.ObjectMeta{Name: name}}
					Expect(k8sClient.Delete(ctx, grant)).To(Succeed())
				}
			}()

			for _, file := range files {
				data, err := os.ReadFile(file)
				Expect(err).NotTo(HaveOccurred(), "reading %s", file)

				obj := &unstructured.Unstructured{}
				Expect(yaml.Unmarshal(data, &obj.Object)).To(Succeed(), "parsing %s", file)
				// The static-users sample is a corev1 Secret; every
				// other sample is a CNMS CR.
				if obj.GetKind() != "Secret" {
					Expect(obj.GetAPIVersion()).To(Equal("cnms.rpp.gmbh/v1alpha1"), "apiVersion in %s", file)
				}
				if obj.GetKind() == "MailDomainGrant" {
					// Cluster-scoped: no namespace.
					clusterScoped = append(clusterScoped, obj.GetName())
				} else {
					obj.SetNamespace(sampleNamespace)
				}

				Expect(k8sClient.Create(ctx, obj)).To(Succeed(), "applying %s", file)
			}
		})
	})

	When("validating CEL rules", func() {
		It("rejects a MailServer without an outbound mode", func() {
			ms := validMailServer("no-outbound")
			ms.Spec.Outbound = cnmsv1alpha1.Outbound{}
			Expect(k8sClient.Create(ctx, ms)).To(MatchError(ContainSubstring("exactly one of direct or relay")))
		})

		It("rejects a MailServer with both direct and relay outbound", func() {
			ms := validMailServer("both-outbound")
			ms.Spec.Outbound = cnmsv1alpha1.Outbound{
				Direct: &cnmsv1alpha1.DirectOutbound{},
				Relay:  &cnmsv1alpha1.RelayOutbound{RelayRef: cnmsv1alpha1.NameReference{Name: "relay"}},
			}
			Expect(k8sClient.Create(ctx, ms)).To(MatchError(ContainSubstring("exactly one of direct or relay")))
		})

		It("rejects a MailDirectory with type ldap but no ldap settings", func() {
			md := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: "no-settings", Namespace: sampleNamespace},
				Spec:       cnmsv1alpha1.MailDirectorySpec{Type: cnmsv1alpha1.DirectoryTypeLDAP},
			}
			Expect(k8sClient.Create(ctx, md)).To(MatchError(ContainSubstring("exactly one of ldap or sql")))
		})

		It("rejects a MailDomainGrant named after a single-label domain", func() {
			grant := &cnmsv1alpha1.MailDomainGrant{
				ObjectMeta: metav1.ObjectMeta{Name: "org"},
				Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: sampleNamespace},
			}
			Expect(k8sClient.Create(ctx, grant)).To(MatchError(ContainSubstring("at least two labels")))
		})

		It("rejects a MailDomainGrant whose name has no label separator", func() {
			// "exampleorg" is one label; a regex dot that matches any
			// character would wrongly accept it.
			grant := &cnmsv1alpha1.MailDomainGrant{
				ObjectMeta: metav1.ObjectMeta{Name: "exampleorg"},
				Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: sampleNamespace},
			}
			Expect(k8sClient.Create(ctx, grant)).To(MatchError(ContainSubstring("at least two labels")))
		})

		It("rejects a MailDomainGrant pointing at an invalid namespace", func() {
			grant := &cnmsv1alpha1.MailDomainGrant{
				ObjectMeta: metav1.ObjectMeta{Name: "grant.example.org"},
				Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: "Tenant A"},
			}
			Expect(k8sClient.Create(ctx, grant)).To(MatchError(ContainSubstring("spec.namespace")))
		})

		It("rejects an LDAP directory with a bind DN but no bind secret", func() {
			ldap := "cn=lookup,ou=services,dc=example,dc=org"
			md := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: "no-bind-secret", Namespace: sampleNamespace},
				Spec: cnmsv1alpha1.MailDirectorySpec{
					Type: cnmsv1alpha1.DirectoryTypeLDAP,
					LDAP: &cnmsv1alpha1.LDAP{
						Host:   ldapTestHost,
						BaseDN: ldapTestBaseDN,
						BindDN: &ldap,
					},
				},
			}
			Expect(k8sClient.Create(ctx, md)).To(MatchError(ContainSubstring("bindSecretRef is required")))
		})

		It("rejects an LDAP directory with a bind DN and tls mode None", func() {
			ldap := "cn=lookup,ou=services,dc=example,dc=org"
			none := "None"
			md := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: "cleartext-bind", Namespace: sampleNamespace},
				Spec: cnmsv1alpha1.MailDirectorySpec{
					Type: cnmsv1alpha1.DirectoryTypeLDAP,
					LDAP: &cnmsv1alpha1.LDAP{
						Host:   ldapTestHost,
						BaseDN: ldapTestBaseDN,
						BindDN: &ldap,
						BindSecretRef: &cnmsv1alpha1.SecretReference{
							Name: "ldap-bind-secret",
						},
						TLS: &cnmsv1alpha1.LDAPTLS{Mode: &none},
					},
				},
			}
			Expect(k8sClient.Create(ctx, md)).To(MatchError(ContainSubstring("cleartext bind")))
		})

		It("rejects an LDAP directory with an invalid tls mode", func() {
			mode := "STARTTLS"
			md := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-tls-mode", Namespace: sampleNamespace},
				Spec: cnmsv1alpha1.MailDirectorySpec{
					Type: cnmsv1alpha1.DirectoryTypeLDAP,
					LDAP: &cnmsv1alpha1.LDAP{
						Host:   ldapTestHost,
						BaseDN: ldapTestBaseDN,
						TLS:    &cnmsv1alpha1.LDAPTLS{Mode: &mode},
					},
				},
			}
			Expect(k8sClient.Create(ctx, md)).To(MatchError(ContainSubstring("spec.ldap.tls.mode")))
		})

		It("defaults the LDAP tls mode to StartTLS", func() {
			// The mode default applies within a present tls object;
			// a fully absent tls object resolves to the same default
			// in the operator (Phase 3).
			md := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: "tls-default", Namespace: sampleNamespace},
				Spec: cnmsv1alpha1.MailDirectorySpec{
					Type: cnmsv1alpha1.DirectoryTypeLDAP,
					LDAP: &cnmsv1alpha1.LDAP{
						Host:   ldapTestHost,
						BaseDN: ldapTestBaseDN,
						TLS:    &cnmsv1alpha1.LDAPTLS{},
					},
				},
			}
			Expect(k8sClient.Create(ctx, md)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tls-default", Namespace: sampleNamespace}, md)).To(Succeed())
			Expect(md.Spec.LDAP.TLS.Mode).To(HaveValue(Equal("StartTLS")))
		})
	})
})

// validMailServer returns a spec-valid MailServer for CEL negative tests.
func validMailServer(name string) *cnmsv1alpha1.MailServer {
	return &cnmsv1alpha1.MailServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sampleNamespace},
		Spec: cnmsv1alpha1.MailServerSpec{
			DirectoryRef: cnmsv1alpha1.NameReference{Name: "dir"},
			Outbound:     cnmsv1alpha1.Outbound{Direct: &cnmsv1alpha1.DirectOutbound{}},
			Domains:      []cnmsv1alpha1.NameReference{{Name: "dom"}},
			TLS: cnmsv1alpha1.TLS{
				IssuerRef: cnmsv1alpha1.IssuerReference{Kind: "Issuer", Name: "iss"},
			},
		},
	}
}
