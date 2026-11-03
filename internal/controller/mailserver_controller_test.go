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
	"crypto/rand"
	"fmt"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// Test fixtures for the controller envtest suite. Each spec gets a
// unique namespace and domain so the asynchronous deletion of one
// spec (cluster-scoped grants, namespaces) cannot collide with the
// next.

// testDomain is the canonical domain used by the unit-level
// authorization and MailDomain tests (the envtest specs use
// uniqueDomain instead).
const testDomain = "example.org"

// postfixObjectName is the Postfix component object name of the
// "mail" test server.
const postfixObjectName = "mail-postfix"

// usersSecretName is the Phase 2 static-users Secret name.
const usersSecretName = "users"

// directoryName is the name of the MailDirectory of the test servers.
const directoryName = "directory"

// testServerName is the MailServer name of the gate and rollout specs.
const testServerName = "mail"

// issuerKind and prometheusEnabled are spec values used by several specs.
const (
	issuerKind        = "Issuer"
	prometheusEnabled = "enabled"
)

// tlsCertKey is the certificate key of the TLS Secret.
const tlsCertKey = "tls.crt"

func uniqueNamespace() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("cnms-%x", b)
}

// uniqueDomain returns a unique domain with at least two labels.
func uniqueDomain() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("example-%x.org", b)
}

// domainLabel returns the DNS-1123 label form of a domain, for use
// as the MailDomain object name (spec.domains entries are labels).
func domainLabel(domain string) string {
	return strings.ReplaceAll(domain, ".", "")
}

func createNamespaceG(name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })
}

func createDirectoryG(ns string) {
	dir := &cnmsv1alpha1.MailDirectory{
		ObjectMeta: metav1.ObjectMeta{Name: directoryName, Namespace: ns},
		Spec: cnmsv1alpha1.MailDirectorySpec{
			Type: cnmsv1alpha1.DirectoryTypeLDAP,
			LDAP: &cnmsv1alpha1.LDAP{
				Host:   "ldap.example.org",
				BaseDN: "dc=example,dc=org",
			},
		},
	}
	Expect(k8sClient.Create(ctx, dir)).To(Succeed())
}

func createDomainG(ns, domain string) {
	d := &cnmsv1alpha1.MailDomain{
		ObjectMeta: metav1.ObjectMeta{Name: domainLabel(domain), Namespace: ns},
		Spec:       cnmsv1alpha1.MailDomainSpec{Domain: domain},
	}
	Expect(k8sClient.Create(ctx, d)).To(Succeed())
}

func createGrantG(domain, ns string, includeSubdomains bool) {
	g := &cnmsv1alpha1.MailDomainGrant{
		ObjectMeta: metav1.ObjectMeta{Name: domain},
		Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: ns},
	}
	if includeSubdomains {
		g.Spec.IncludeSubdomains = new(true)
	}
	Expect(k8sClient.Create(ctx, g)).To(Succeed())
}

// createServerG creates a MailServer for the given domain; mutate
// applies spec overrides before creation.
func createServerG(ns, name, domain string, mutate func(*cnmsv1alpha1.MailServer)) {
	Expect(k8sClient.Create(ctx, newServerG(ns, name, domain, mutate))).To(Succeed())
}

// newServerG builds the MailServer createServerG creates.
func newServerG(ns, name, domain string, mutate func(*cnmsv1alpha1.MailServer)) *cnmsv1alpha1.MailServer {
	s := &cnmsv1alpha1.MailServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cnmsv1alpha1.MailServerSpec{
			DirectoryRef: cnmsv1alpha1.NameReference{Name: directoryName},
			Outbound:     cnmsv1alpha1.Outbound{Direct: &cnmsv1alpha1.DirectOutbound{}},
			Domains:      []cnmsv1alpha1.NameReference{{Name: domainLabel(domain)}},
			TLS: cnmsv1alpha1.TLS{
				IssuerRef: cnmsv1alpha1.IssuerReference{Kind: issuerKind, Name: "test-issuer"},
			},
		},
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

func mutateServerG(ns string, fn func(*cnmsv1alpha1.MailServer)) {
	server := &cnmsv1alpha1.MailServer{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
	fn(server)
	Expect(k8sClient.Update(ctx, server)).To(Succeed())
}

func createUsersSecretG(ns, payload string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: usersSecretName, Namespace: ns},
		Data:       map[string][]byte{"users.yaml": []byte(payload)},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
}

func newReconcilerG(mode domainAuthorizationMode, insecure bool) *MailServerReconciler {
	return &MailServerReconciler{
		Client:                     k8sClient,
		Scheme:                     scheme.Scheme,
		APIReader:                  k8sClient,
		DomainAuthorization:        mode,
		EnableInsecureTestFeatures: insecure,
	}
}

func reconcileServerG(r *MailServerReconciler, ns, name string) ctrl.Result {
	res, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	})
	Expect(err).ToNot(HaveOccurred())
	return res
}

func serverConditionG(ns, name, condType string) (metav1.ConditionStatus, string, string) {
	server := &cnmsv1alpha1.MailServer{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, server)).To(Succeed())
	c := meta.FindStatusCondition(server.Status.Conditions, condType)
	if c == nil {
		return "", "", ""
	}
	return c.Status, c.Reason, c.Message
}

func getStatefulSetG(ns, name string) *appsv1.StatefulSet {
	sts := &appsv1.StatefulSet{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, sts)).To(Succeed())
	return sts
}

func getDeploymentG(ns, name string) {
	dep := &appsv1.Deployment{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, dep)).To(Succeed())
}

// componentLabelsG selects the objects of one component of a server.
func componentLabelsG(server, component string) client.MatchingLabels {
	return client.MatchingLabels{
		manifests.InstanceLabelKey:    server,
		"app.kubernetes.io/component": component,
	}
}

// configMapsG lists the immutable ConfigMaps of a component.
func configMapsG(ns, component string) []corev1.ConfigMap {
	list := &corev1.ConfigMapList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns), componentLabelsG(testServerName, component))).To(Succeed())
	return list.Items
}

// secretsG lists the immutable Secrets of a component.
func secretsG(ns, component string) []corev1.Secret {
	list := &corev1.SecretList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns), componentLabelsG(testServerName, component))).To(Succeed())
	return list.Items
}

// jobsG lists the validation Jobs of a server (all components, or
// one when component is set).
func jobsG(ns, server, component string) []batchv1.Job {
	labels := client.MatchingLabels{manifests.InstanceLabelKey: server}
	if component != "" {
		labels = componentLabelsG(server, component)
	}
	list := &batchv1.JobList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns), labels)).To(Succeed())
	return list.Items
}

// finishJobG sets the terminal status of a Job (envtest runs no Job
// controller).
func finishJobG(job *batchv1.Job, success bool) {
	now := metav1.Now()
	job.Status.StartTime = &now
	if success {
		job.Status.CompletionTime = &now
		job.Status.Succeeded = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
	} else {
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", LastTransitionTime: now},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded",
				Message: "Job has reached the specified backoff limit", LastTransitionTime: now},
		}
	}
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

// finishJobsG finishes every validation Job of the server that has
// no result yet.
func finishJobsG(ns, server string, success bool) {
	jobs := jobsG(ns, server, "")
	for i := range jobs {
		if len(jobs[i].Status.Conditions) == 0 {
			finishJobG(&jobs[i], success)
		}
	}
}

// deployG reconciles a server, lets all its validation Jobs succeed
// and reconciles again, so the workloads exist.
func deployG(r *MailServerReconciler, ns, name string) {
	reconcileServerG(r, ns, name)
	finishJobsG(ns, name, true)
	reconcileServerG(r, ns, name)
}

func getNetworkPoliciesG(ns string) []networkingv1.NetworkPolicy {
	list := &networkingv1.NetworkPolicyList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns))).To(Succeed())
	return list.Items
}

func getPDBG(ns, name string) *policyv1.PodDisruptionBudget {
	pdb := &policyv1.PodDisruptionBudget{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, pdb)).To(Succeed())
	return pdb
}

// podTemplateHash reads the config-hash annotation of a StatefulSet.
func podTemplateHash(sts *appsv1.StatefulSet) string {
	return sts.Spec.Template.Annotations[manifests.ConfigHashAnnotation]
}

func listServiceMonitorsG(ns string) []unstructured.Unstructured {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("monitoring.coreos.com/v1")
	list.SetKind("ServiceMonitorList")
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns))).To(Succeed())
	return list.Items
}

// grantFixture sets up namespace, directory and the granted domain.
func grantFixture() (ns, domain string) {
	ns = uniqueNamespace()
	domain = uniqueDomain()
	createNamespaceG(ns)
	createDirectoryG(ns)
	createDomainG(ns, domain)
	createGrantG(domain, ns, false)
	return ns, domain
}

var _ = Describe("MailServer controller", func() {
	Describe("domain authorization", func() {
		var ns, domain string
		BeforeEach(func() {
			ns = uniqueNamespace()
			domain = uniqueDomain()
			createNamespaceG(ns)
			createDirectoryG(ns)
			createDomainG(ns, domain)
		})

		It("does not deploy the data plane for an unauthorized domain", func() {
			createServerG(ns, "mail-a", domain, nil)
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail-a")

			status, reason, _ := serverConditionG(ns, "mail-a", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonNotGranted))
			readyStatus, readyReason, _ := serverConditionG(ns, "mail-a", conditionReady)
			Expect(readyStatus).To(Equal(metav1.ConditionFalse))
			Expect(readyReason).To(Equal(ReasonNotGranted))
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "mail-a-postfix", Namespace: ns},
				&appsv1.StatefulSet{})
			Expect(err).To(HaveOccurred())
		})

		It("deploys the data plane for a granted domain", func() {
			createGrantG(domain, ns, false)
			createServerG(ns, "mail-b", domain, nil)
			deployG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail-b")

			status, reason, _ := serverConditionG(ns, "mail-b", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAuthorized))
			// Workloads do not come up in envtest, so Ready stays false.
			readyStatus, _, _ := serverConditionG(ns, "mail-b", conditionReady)
			Expect(readyStatus).To(Equal(metav1.ConditionFalse))
			getStatefulSetG(ns, "mail-b-postfix")
		})

		It("authorizes a subdomain via includeSubdomains", func() {
			sub := "mail." + domain
			createDomainG(ns, sub)
			// The includeSubdomains grant covers the subdomain.
			createGrantG(domain, ns, true)
			createServerG(ns, "mail-c", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Domains = []cnmsv1alpha1.NameReference{{Name: domainLabel(sub)}}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail-c")

			status, reason, _ := serverConditionG(ns, "mail-c", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAuthorized))
		})

		It("blocks a duplicated domain claim in the owning namespace", func() {
			createGrantG(domain, ns, false)
			Expect(k8sClient.Create(ctx, &cnmsv1alpha1.MailDomain{
				ObjectMeta: metav1.ObjectMeta{Name: "copy", Namespace: ns},
				Spec:       cnmsv1alpha1.MailDomainSpec{Domain: domain},
			})).To(Succeed())
			createServerG(ns, "mail-d", domain, nil)
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail-d")

			status, reason, _ := serverConditionG(ns, "mail-d", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonDuplicateDomain))
		})

		It("ignores a foreign claim from a namespace without a grant", func() {
			createGrantG(domain, ns, false)
			ns2 := uniqueNamespace()
			createNamespaceG(ns2)
			createDomainG(ns2, domain)
			createServerG(ns, "mail-d", domain, nil)
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail-d")

			status, reason, _ := serverConditionG(ns, "mail-d", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAuthorized))
		})

		It("fails closed for a missing referenced MailDomain", func() {
			missing := "nope-" + domainLabel(domain)
			createServerG(ns, "mail-e", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Domains = []cnmsv1alpha1.NameReference{{Name: missing}}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail-e")

			status, reason, _ := serverConditionG(ns, "mail-e", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal(ReasonDomainNotFound))
		})

		It("authorizes without grants in unrestricted mode", func() {
			createServerG(ns, "mail-f", domain, nil)
			deployG(newReconcilerG(AuthorizationUnrestricted, true), ns, "mail-f")

			status, reason, _ := serverConditionG(ns, "mail-f", ConditionDomainsAuthorized)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal(ReasonAuthorizationDisabled))
			getStatefulSetG(ns, "mail-f-postfix")
		})
	})

	Describe("data plane", func() {
		var ns, domain string
		BeforeEach(func() {
			ns, domain = grantFixture()
		})
		reconcile := func(name string, mutate func(*cnmsv1alpha1.MailServer)) {
			createServerG(ns, name, domain, mutate)
			deployG(newReconcilerG(AuthorizationAllowlist, true), ns, name)
		}

		It("creates the workloads, services, policies and the budget", func() {
			reconcile("mail", nil)

			getStatefulSetG(ns, postfixObjectName)
			getStatefulSetG(ns, "mail-dovecot")
			getDeploymentG(ns, "mail-rspamd")
			getDeploymentG(ns, "mail-valkey")
			for _, c := range []string{"postfix", "rspamd", "dovecot", "valkey"} {
				cms := configMapsG(ns, c)
				Expect(cms).To(HaveLen(1))
				Expect(*cms[0].Immutable).To(BeTrue())
				Expect(cms[0].Name).To(MatchRegexp("^mail-" + c + "-[0-9a-f]{8}$"))
				Expect(cms[0].OwnerReferences).To(HaveLen(1))
			}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns},
				&corev1.Service{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "mail-postfix-headless", Namespace: ns},
				&corev1.Service{})).To(Succeed())

			policies := getNetworkPoliciesG(ns)
			names := make([]string, 0, len(policies))
			for _, p := range policies {
				names = append(names, p.Name)
			}
			Expect(names).To(ContainElements(
				"mail-ingress", "mail-egress", "mail-egress-smtp", "mail-egress-rspamd"))

			getPDBG(ns, postfixObjectName)

			// The Certificate is not created without the cert-manager CRDs.
			cert := &unstructured.Unstructured{}
			cert.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
			err := k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, cert)
			Expect(err).To(HaveOccurred())
		})

		It("applies the PDB minAvailable override", func() {
			minAv := intstr.FromInt32(2)
			reconcile("mail", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Availability = &cnmsv1alpha1.Availability{PostfixMinAvailable: &minAv}
			})
			pdb := getPDBG(ns, postfixObjectName)
			Expect(*pdb.Spec.MinAvailable).To(Equal(minAv))
		})

		It("creates the PDB with the default minAvailable of an empty availability", func() {
			reconcile("mail", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Availability = &cnmsv1alpha1.Availability{}
			})
			pdb := getPDBG(ns, postfixObjectName)
			Expect(*pdb.Spec.MinAvailable).To(Equal(intstr.FromInt32(1)))
		})

		It("creates the valkey PVC only with persistence", func() {
			reconcile("mail-off", nil)
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: "mail-off-valkey-valkey-data", Namespace: ns,
			}, &corev1.PersistentVolumeClaim{})
			Expect(err).To(HaveOccurred())

			reconcile("mail-on", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Valkey = &cnmsv1alpha1.Valkey{
					Persistence: new(true),
					Storage:     new(resource.MustParse("1Gi")),
				}
			})
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "mail-on-valkey-valkey-data", Namespace: ns,
			}, pvc)).To(Succeed())
			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].Kind).To(Equal("MailServer"))
			Expect(*pvc.OwnerReferences[0].Controller).To(BeTrue())
			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).
				To(Equal(resource.MustParse("1Gi")))
		})

		It("creates the dovecot mail volume only with mailboxes", func() {
			reconcile("mail-off", nil)
			Expect(getStatefulSetG(ns, "mail-off-dovecot").Spec.VolumeClaimTemplates).To(BeEmpty())

			reconcile("mail-on", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
			})
			sts := getStatefulSetG(ns, "mail-on-dovecot")
			Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal("mail"))
		})

		It("keeps the config hash stable and config-bound", func() {
			reconcile("mail", nil)
			first := podTemplateHash(getStatefulSetG(ns, postfixObjectName))
			Expect(first).ToNot(BeEmpty())
			dovecotFirst := podTemplateHash(getStatefulSetG(ns, "mail-dovecot"))

			// A second reconcile with unchanged config keeps the hash.
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			Expect(podTemplateHash(getStatefulSetG(ns, postfixObjectName))).To(Equal(first))

			// A config change changes the hash.
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
			})
			deployG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			Expect(podTemplateHash(getStatefulSetG(ns, "mail-dovecot"))).ToNot(Equal(dovecotFirst))
		})

		It("warns about hidden client addresses without failing Ready", func() {
			reconcile("mail", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Inbound = &cnmsv1alpha1.Inbound{ExternalTrafficPolicy: new("Cluster")}
			})
			status, reason, _ := serverConditionG(ns, "mail", ConditionClientIPPreserved)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal("ClusterTrafficPolicy"))
			// The warning is not part of the Ready roll-up.
			_, readyReason, _ := serverConditionG(ns, "mail", conditionReady)
			Expect(readyReason).ToNot(Equal("ClusterTrafficPolicy"))

			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Inbound.ExternalTrafficPolicy = new("Local")
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, _, _ = serverConditionG(ns, "mail", ConditionClientIPPreserved)
			Expect(status).To(Equal(metav1.ConditionTrue))
		})

		It("warns about a budget that blocks node drains without failing Ready", func() {
			reconcile("mail", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Postfix = &cnmsv1alpha1.Postfix{Replicas: new(int32(1))}
			})
			status, reason, _ := serverConditionG(ns, "mail", ConditionNodeDrainable)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal("BudgetBlocksDrain"))
			_, readyReason, _ := serverConditionG(ns, "mail", conditionReady)
			Expect(readyReason).ToNot(Equal("BudgetBlocksDrain"))

			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Postfix.Replicas = new(int32(2))
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, _, _ = serverConditionG(ns, "mail", ConditionNodeDrainable)
			Expect(status).To(Equal(metav1.ConditionTrue))
		})

		It("reports a full percentage budget as blocking node drains", func() {
			pct := intstr.FromString("100%")
			reconcile("mail", func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Availability = &cnmsv1alpha1.Availability{PostfixMinAvailable: &pct}
			})
			status, _, _ := serverConditionG(ns, "mail", ConditionNodeDrainable)
			Expect(status).To(Equal(metav1.ConditionFalse))
		})

		It("reports the observed generation", func() {
			reconcile("mail", nil)
			server := &cnmsv1alpha1.MailServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testServerName, Namespace: ns}, server)).To(Succeed())
			Expect(server.Status.ObservedGeneration).To(Equal(server.Generation))
		})
	})

	Describe("TLS certificate", func() {
		var ns, domain string
		BeforeEach(func() {
			ns, domain = grantFixture()
		})

		It("is not ready without the certificate Secret and becomes ready with it", func() {
			createServerG(ns, "mail", domain, nil)
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, reason, _ := serverConditionG(ns, "mail", ConditionCertificateReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal("SecretNotFound"))

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "mail-tls", Namespace: ns},
				Data: map[string][]byte{
					tlsCertKey: []byte("crt"),
					"tls.key":  []byte("key"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, reason, _ = serverConditionG(ns, "mail", ConditionCertificateReady)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal("Issued"))
		})

		It("is not ready for an incomplete Secret", func() {
			createServerG(ns, "mail", domain, nil)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "mail-tls", Namespace: ns},
				Data:       map[string][]byte{tlsCertKey: []byte("crt")},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, reason, _ := serverConditionG(ns, "mail", ConditionCertificateReady)
			Expect(status).To(Equal(metav1.ConditionFalse))
			Expect(reason).To(Equal("SecretIncomplete"))
		})
	})

	Describe("static test users", func() {
		var ns, domain string
		validUsers := "users:\n" +
			"  - username: testuser\n" +
			"    passwordHash: '$6$salt$hash'\n" +
			"    mailbox: testuser@example.org\n"
		BeforeEach(func() {
			ns, domain = grantFixture()
		})

		It("renders with a valid users Secret", func() {
			createUsersSecretG(ns, validUsers)
			createServerG(ns, "mail", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{
					InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName},
				}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

			// Rendering succeeded; readiness is limited by the (not
			// issued) certificate, not by the users.
			_, readyReason, _ := serverConditionG(ns, "mail", conditionReady)
			Expect(readyReason).ToNot(Equal(ReasonStaticUsersInvalid))
			Expect(readyReason).ToNot(Equal(ReasonInsecureStaticUsersDisabled))
			// The password hashes live in an immutable Secret, not in
			// the ConfigMap.
			secrets := secretsG(ns, "dovecot")
			Expect(secrets).To(HaveLen(1))
			Expect(*secrets[0].Immutable).To(BeTrue())
			Expect(string(secrets[0].Data["users"])).To(ContainSubstring("testuser"))
			for _, cm := range configMapsG(ns, "dovecot") {
				Expect(cm.Data).ToNot(HaveKey("users"))
			}
		})

		It("fails closed for an invalid password hash", func() {
			createUsersSecretG(ns,
				"users:\n  - username: testuser\n    passwordHash: plaintext\n    mailbox: testuser@example.org\n")
			createServerG(ns, "mail", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{
					InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName},
				}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

			_, reason, message := serverConditionG(ns, "mail", conditionReady)
			Expect(reason).To(Equal(ReasonStaticUsersInvalid))
			Expect(message).To(ContainSubstring("crypt"))
			err := k8sClient.Get(ctx, types.NamespacedName{Name: postfixObjectName, Namespace: ns},
				&appsv1.StatefulSet{})
			Expect(err).To(HaveOccurred())
		})

		It("fails closed without the feature flag", func() {
			createUsersSecretG(ns, validUsers)
			createServerG(ns, "mail", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{
					InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName},
				}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, false), ns, "mail")

			_, reason, _ := serverConditionG(ns, "mail", conditionReady)
			Expect(reason).To(Equal(ReasonInsecureStaticUsersDisabled))
		})

		It("fails closed for a missing Secret", func() {
			createServerG(ns, "mail", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Submission = &cnmsv1alpha1.Submission{
					InsecureStaticUsers: &cnmsv1alpha1.InsecureStaticUsers{SecretName: usersSecretName},
				}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")

			_, reason, _ := serverConditionG(ns, "mail", conditionReady)
			Expect(reason).To(Equal(ReasonStaticUsersInvalid))
		})
	})

	Describe("observability", func() {
		var ns, domain string
		BeforeEach(func() {
			ns, domain = grantFixture()
		})

		It("creates ServiceMonitors in auto mode when the CRDs are installed", func() {
			createServerG(ns, "mail", domain, nil)
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, reason, _ := serverConditionG(ns, "mail", ConditionObservability)
			Expect(status).To(Equal(metav1.ConditionTrue))
			if os.Getenv("PROMETHEUS_CRDS") == "1" {
				Expect(reason).To(Equal("MonitorsCreated"))
				Expect(listServiceMonitorsG(ns)).To(HaveLen(3))
			} else {
				Expect(reason).To(Equal("CRDsUnavailable"))
			}
		})

		It("requires the CRDs in enabled mode", func() {
			enabled := prometheusEnabled
			createServerG(ns, "mail", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Observability = &cnmsv1alpha1.Observability{PrometheusOperator: &enabled}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, reason, _ := serverConditionG(ns, "mail", ConditionObservability)
			if os.Getenv("PROMETHEUS_CRDS") == "1" {
				Expect(status).To(Equal(metav1.ConditionTrue))
				Expect(reason).To(Equal("MonitorsCreated"))
			} else {
				Expect(status).To(Equal(metav1.ConditionFalse))
				Expect(reason).To(Equal("CRDsRequired"))
			}
		})

		It("deletes ServiceMonitors when the setting switches to disabled", func() {
			if os.Getenv("PROMETHEUS_CRDS") != "1" {
				return // nothing can exist without the CRDs
			}
			enabled := prometheusEnabled
			createServerG(ns, "mail", domain, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Observability = &cnmsv1alpha1.Observability{PrometheusOperator: &enabled}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			Expect(listServiceMonitorsG(ns)).To(HaveLen(3))

			disabled := "disabled"
			mutateServerG(ns, func(s *cnmsv1alpha1.MailServer) {
				s.Spec.Observability = &cnmsv1alpha1.Observability{PrometheusOperator: &disabled}
			})
			reconcileServerG(newReconcilerG(AuthorizationAllowlist, true), ns, "mail")
			status, reason, _ := serverConditionG(ns, "mail", ConditionObservability)
			Expect(status).To(Equal(metav1.ConditionTrue))
			Expect(reason).To(Equal("Disabled"))
			Expect(listServiceMonitorsG(ns)).To(BeEmpty())
		})
	})
})

var _ = Describe("MailDomainGrant controller", func() {
	var domain string
	BeforeEach(func() {
		domain = uniqueDomain()
	})

	It("reports an existing namespace", func() {
		ns := uniqueNamespace()
		createNamespaceG(ns)
		createGrantG(domain, ns, false)

		r := &MailDomainGrantReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: domain},
		})
		Expect(err).ToNot(HaveOccurred())

		grant := &cnmsv1alpha1.MailDomainGrant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: domain}, grant)).To(Succeed())
		c := meta.FindStatusCondition(grant.Status.Conditions, ConditionNamespaceExists)
		Expect(c).ToNot(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(grant.Status.ObservedGeneration).To(Equal(grant.Generation))
	})

	It("reports a missing namespace", func() {
		createGrantG(domain, "no-such-ns", false)

		r := &MailDomainGrantReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: domain},
		})
		Expect(err).ToNot(HaveOccurred())

		grant := &cnmsv1alpha1.MailDomainGrant{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: domain}, grant)).To(Succeed())
		c := meta.FindStatusCondition(grant.Status.Conditions, ConditionNamespaceExists)
		Expect(c).ToNot(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("NamespaceNotFound"))
	})
})
