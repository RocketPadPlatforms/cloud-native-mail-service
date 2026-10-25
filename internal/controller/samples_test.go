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

			for _, file := range files {
				data, err := os.ReadFile(file)
				Expect(err).NotTo(HaveOccurred(), "reading %s", file)

				obj := &unstructured.Unstructured{}
				Expect(yaml.Unmarshal(data, &obj.Object)).To(Succeed(), "parsing %s", file)
				Expect(obj.GetAPIVersion()).To(Equal("cnms.rpp.gmbh/v1alpha1"), "apiVersion in %s", file)
				obj.SetNamespace(sampleNamespace)

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

		It("rejects an LDAP directory with a bind DN but no bind secret", func() {
			ldap := "cn=lookup,ou=services,dc=example,dc=org"
			md := &cnmsv1alpha1.MailDirectory{
				ObjectMeta: metav1.ObjectMeta{Name: "no-bind-secret", Namespace: sampleNamespace},
				Spec: cnmsv1alpha1.MailDirectorySpec{
					Type: cnmsv1alpha1.DirectoryTypeLDAP,
					LDAP: &cnmsv1alpha1.LDAP{
						Host:   "ldap.example.org",
						BaseDN: "ou=people,dc=example,dc=org",
						BindDN: &ldap,
					},
				},
			}
			Expect(k8sClient.Create(ctx, md)).To(MatchError(ContainSubstring("bindSecretRef is required")))
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
