//go:build e2e

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

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/test/utils"
)

// The MailDomains of the DKIM specs. They are served by the fixture
// MailServer next to e2e.example.org. The server presents its hostname
// whatever domains it serves (ADR 0014), so their names need no order.
const (
	// dkimDetached is a MailServer that does not exist: the domains start
	// there, so their keys and records exist before the fixture server
	// serves them. The setup attaches them with one patch each, so the
	// server may roll out once per patch; awaitKeysServed waits for both.
	dkimDetached = "dkim-detached"
	// e2eServer is the fixture MailServer.
	e2eServer = "mail"

	dkimGenName   = "dkim-generated"
	dkimGenDomain = "gen.e2e.example.org"
	dkimOwnName   = "dkim-own"
	dkimOwnDomain = "own.e2e.example.org"

	// The bring-your-own key: its Secret, the data key and the selector.
	dkimOwnSecret   = "dkim-own-key"
	dkimOwnDataKey  = "byo-2026.key"
	dkimOwnSelector = "byo-2026"

	// The a= tags of the signatures.
	algRSA     = "rsa-sha256"
	algEd25519 = "ed25519-sha256"

	// The condition types the specs read.
	condKeysValid       = "DKIMKeysValid"
	condRecordsVerified = "RecordsVerified"
	condReady           = "Ready"

	// The operator, which the guard spec stops.
	dkimOperatorDeployment = "cloud-native-mail-service-controller-manager"
	// The Rspamd pods of the fixture MailServer.
	dkimRspamdPods = "cnms.rpp.gmbh/mailserver=mail,app.kubernetes.io/component=rspamd"
)

// dkimServed is set once the data plane serves the keys of both domains.
var dkimServed bool

// registerDKIMSpecs registers the MailDomain specs (Phase 5, ADR 0011 to
// 0013) inside the ordered e2e Describe: they reuse its operator, MailServer
// and Mailpit sink, and its failure dump.
func registerDKIMSpecs() {
	Context("MailDomain DKIM, DNS records and the never-unsigned guard", func() {
		var ownKeyPEM string

		BeforeAll(func() {
			By("starting the test DNS server")
			deployDNSServer()

			By("pointing the operator at the test DNS server")
			out, err := kubectl("patch", "deployment", dkimOperatorDeployment, "-n", namespace,
				"--type", "json", "-p", fmt.Sprintf(
					`[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns-resolver=%s"}]`,
					dnsResolverAddr))
			Expect(err).NotTo(HaveOccurred(), "patch output: %s", out)
			out, err = kubectl("rollout", "status", "deployment", dkimOperatorDeployment, "-n", namespace, "--timeout=3m")
			Expect(err).NotTo(HaveOccurred(), "the operator rollout did not finish: %s", out)

			By("creating the bring-your-own key")
			ownKeyPEM, err = generatePrivateKeyPEM()
			Expect(err).NotTo(HaveOccurred())
			applyOwnKeySecret(ownKeyPEM)

			By("granting and creating the two MailDomains, not yet served")
			kubectlApplyYAML(dkimDomainsYAML())

			By("waiting for the keys and the required records")
			Eventually(func(g Gomega) {
				gen, err := getMailDomainObject(dkimGenName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(activeSelectors(gen)).To(HaveLen(2), "generated keys are not active yet")
				g.Expect(gen.Status.Records).To(HaveLen(2))
				own, err := getMailDomainObject(dkimOwnName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(mailDomainCondition(own, condKeysValid)).To(Equal(metav1.ConditionTrue))
				g.Expect(own.Status.Records).To(HaveLen(1))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())

			By("publishing the records in the test zone")
			gen, err := getMailDomainObject(dkimGenName)
			Expect(err).NotTo(HaveOccurred())
			own, err := getMailDomainObject(dkimOwnName)
			Expect(err).NotTo(HaveOccurred())
			publishZone(slices.Concat(gen.Status.Records, own.Status.Records))

			By("serving both domains from the MailServer")
			for _, name := range []string{dkimGenName, dkimOwnName} {
				out, err = kubectl("patch", "maildomain", name, "-n", e2eNamespace, "--type", "merge", "-p",
					fmt.Sprintf(`{"spec":{"serverRef":{"name":%q}}}`, e2eServer))
				Expect(err).NotTo(HaveOccurred(), "patch output: %s", out)
			}

			By("publishing the MX records the server adds to the domains")
			Eventually(func(g Gomega) {
				gen, err = getMailDomainObject(dkimGenName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gen.Status.Records).To(HaveLen(3), "the MX record is not listed yet")
				own, err = getMailDomainObject(dkimOwnName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(own.Status.Records).To(HaveLen(2), "the MX record is not listed yet")
			}, 3*time.Minute, 3*time.Second).Should(Succeed())
			publishZone(slices.Concat(gen.Status.Records, own.Status.Records))
		})

		AfterAll(func() {
			By("removing the MailDomainGrants of the DKIM specs (cluster-scoped)")
			_, _ = kubectl("delete", "maildomaingrant", dkimGenDomain, dkimOwnDomain, "--wait=false", "--ignore-not-found")
		})

		AfterEach(func() {
			if !CurrentSpecReport().Failed() {
				return
			}
			dump := func(name string, args ...string) {
				By("Fetching " + name)
				out, err := kubectl(args...)
				if err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get %s: %s\n%s\n", name, err, out)
					return
				}
				_, _ = fmt.Fprintf(GinkgoWriter, "%s:\n%s\n", name, out)
			}
			dump("MailDomains", "get", "maildomain", "-n", e2eNamespace, "-o", "yaml")
			dump("DKIM key Secrets", "get", "secrets", "-n", e2eNamespace,
				"-l", "cnms.rpp.gmbh/maildomain", "-o", "name")
			dump("MailServer conditions", "get", "mailserver", "mail", "-n", e2eNamespace,
				"-o", `jsonpath={range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}`)
			dump("Test zone", "get", "configmap", dnsZoneConfigMap, "-n", e2eNamespace,
				"-o", `jsonpath={.data.e2e\.example\.org\.zone}`)
			dump("Rspamd logs", "logs", "-n", e2eNamespace, "-l", dkimRspamdPods,
				"-c", "rspamd", "--all-pods", "--tail=200")
			By("Fetching the operator logs of the MailDomain controller")
			if logs, err := kubectl("logs", "-n", namespace, "deployment/"+dkimOperatorDeployment, "--tail=3000"); err == nil {
				for line := range strings.Lines(logs) {
					if strings.Contains(line, "maildomain") || strings.Contains(line, "MailDomain") {
						_, _ = fmt.Fprint(GinkgoWriter, line)
					}
				}
			}
		})

		It("verifies the DKIM records against the test DNS server", func() {
			Eventually(func(g Gomega) {
				for _, name := range []string{dkimGenName, dkimOwnName} {
					md, err := getMailDomainObject(name)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(mailDomainCondition(md, condRecordsVerified)).To(Equal(metav1.ConditionTrue),
						"%s: records %+v", name, md.Status.Records)
					g.Expect(mailDomainCondition(md, condReady)).To(Equal(metav1.ConditionTrue), name)
				}
			}, 8*time.Minute, 5*time.Second).Should(Succeed())

			gen, err := getMailDomainObject(dkimGenName)
			Expect(err).NotTo(HaveOccurred())
			active := activeSelectors(gen)
			Expect(active).To(HaveKey("rsa"))
			Expect(active).To(HaveKey("ed25519"))
			names := make([]string, 0, len(gen.Status.Records))
			for _, rec := range gen.Status.Records {
				names = append(names, rec.Type+" "+rec.Name+" "+rec.Value)
				Expect(rec.Verified).NotTo(BeNil())
				Expect(*rec.Verified).To(BeTrue(), rec.Name)
			}
			// The two keys and the MX record at the hostname of the server.
			Expect(names).To(HaveLen(3))
			Expect(names).To(ContainElement("MX " + dkimGenDomain + " 10 " + e2eHostname))
			Expect(names).To(ContainElement(HavePrefix("TXT " + active["rsa"] + "._domainkey." + dkimGenDomain + " ")))
			Expect(names).To(ContainElement(HavePrefix("TXT " + active["ed25519"] + "._domainkey." + dkimGenDomain + " ")))
		})

		It("signs authenticated mail with an RSA and an Ed25519 signature", func() {
			awaitKeysServed()
			gen, err := getMailDomainObject(dkimGenName)
			Expect(err).NotTo(HaveOccurred())
			active := activeSelectors(gen)

			// This also proves the key files of the projected volume are
			// readable by the Rspamd user on a real cluster (ADR 0011).
			sigs := sendAndFetchSignatures(dkimGenDomain, "e2e-dkim-generated")
			Expect(sigs).To(ConsistOf(
				dkimSignature{algRSA, dkimGenDomain, active["rsa"]},
				dkimSignature{algEd25519, dkimGenDomain, active["ed25519"]},
			))
		})

		It("signs with a bring-your-own key", func() {
			awaitKeysServed()
			sigs := sendAndFetchSignatures(dkimOwnDomain, "e2e-dkim-own")
			Expect(sigs).To(ConsistOf(dkimSignature{algRSA, dkimOwnDomain, dkimOwnSelector}))
		})

		It("keeps its keys when a restore loses the rotation annotation", func() {
			awaitKeysServed()
			before, err := getMailDomainObject(dkimGenName)
			Expect(err).NotTo(HaveOccurred())
			rotation := before.Annotations[dkim.AnnotationRotation]
			Expect(rotation).NotTo(BeEmpty(), "the rotation state is not recorded")
			secrets := keySecrets(dkimGenName)
			Expect(secrets).To(HaveLen(2))

			By("deleting the annotation, as a restore from GitOps does")
			out, err := kubectl("annotate", "maildomain", dkimGenName, "-n", e2eNamespace, dkim.AnnotationRotation+"-")
			Expect(err).NotTo(HaveOccurred(), "annotate output: %s", out)

			By("the controller derives the same state again")
			Eventually(func(g Gomega) {
				md, err := getMailDomainObject(dkimGenName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(md.Annotations[dkim.AnnotationRotation]).To(Equal(rotation))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("no key was created, replaced or deleted")
			Expect(keySecrets(dkimGenName)).To(Equal(secrets))
			after, err := getMailDomainObject(dkimGenName)
			Expect(err).NotTo(HaveOccurred())
			Expect(activeSelectors(after)).To(Equal(activeSelectors(before)))

			By("the data plane still signs with the same keys")
			Eventually(func() (string, error) { return mailServerCondition("Ready") }, 5*time.Minute, 5*time.Second).
				Should(Equal("True"))
			sigs := sendAndFetchSignatures(dkimGenDomain, "e2e-dkim-restored")
			Expect(sigs).To(ConsistOf(
				dkimSignature{algRSA, dkimGenDomain, activeSelectors(before)["rsa"]},
				dkimSignature{algEd25519, dkimGenDomain, activeSelectors(before)["ed25519"]},
			))
		})

		It("defers the mail of a domain whose key is corrupted until the key is back", func() {
			awaitKeysServed()
			// A failed spec must not leave the key broken for the next one.
			DeferCleanup(func() { applyOwnKeySecret(ownKeyPEM) })

			By("corrupting the key Secret")
			applyOwnKeySecret("this is not a private key\n")
			// The Secret is neither cached nor watched: the controller reads
			// it on its next timer (5 minutes), or when the MailDomain changes.
			touchMailDomain(dkimOwnName)
			Eventually(func(g Gomega) {
				md, err := getMailDomainObject(dkimOwnName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(mailDomainCondition(md, condKeysValid)).To(Equal(metav1.ConditionFalse))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("the guard defers authenticated mail once the kubelet has synced the Secret")
			var attempts []string
			Eventually(func(g Gomega) {
				subject := fmt.Sprintf("e2e-dkim-corrupted-%d", len(attempts))
				attempts = append(attempts, subject)
				out := submitFromDomain(dkimOwnDomain, subject)
				g.Expect(out).To(ContainSubstring("451 4.7.1"), "swaks output: %s", out)
				g.Expect(out).NotTo(ContainSubstring("queued as"))
			}, 8*time.Minute, 5*time.Second).Should(Succeed())

			By("nothing left unsigned")
			for i, subject := range attempts {
				raw, found, err := mailpitRawBySubject(subject)
				Expect(err).NotTo(HaveOccurred())
				if i == len(attempts)-1 {
					Expect(found).To(BeFalse(), "a deferred message reached the sink")
				} else if found { // accepted before the key file changed: signed
					sigs, err := dkimSignatures(raw)
					Expect(err).NotTo(HaveOccurred())
					Expect(sigs).NotTo(BeEmpty(), "%s was delivered unsigned", subject)
				}
			}

			By("restoring the key")
			applyOwnKeySecret(ownKeyPEM)
			touchMailDomain(dkimOwnName)
			Eventually(func(g Gomega) {
				md, err := getMailDomainObject(dkimOwnName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(mailDomainCondition(md, condKeysValid)).To(Equal(metav1.ConditionTrue))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("the next retry is signed and delivered")
			sigs := sendAndFetchSignatures(dkimOwnDomain, "e2e-dkim-recovered")
			Expect(sigs).To(ConsistOf(dkimSignature{algRSA, dkimOwnDomain, dkimOwnSelector}))
		})

		It("defers submission while Rspamd is down, without the operator", func() {
			awaitKeysServed()
			gen, err := getMailDomainObject(dkimGenName)
			Expect(err).NotTo(HaveOccurred())
			active := activeSelectors(gen)
			DeferCleanup(func() {
				_, _ = kubectl("scale", "deployment", "mail-rspamd", "-n", e2eNamespace, "--replicas=1")
				_, _ = kubectl("scale", "deployment", dkimOperatorDeployment, "-n", namespace, "--replicas=1")
			})

			By("stopping the operator: the data plane does not depend on it (principle 4)")
			_, err = kubectl("scale", "deployment", dkimOperatorDeployment, "-n", namespace, "--replicas=0")
			Expect(err).NotTo(HaveOccurred())
			_, _ = kubectl("wait", "--for=delete", "pod", "-l", "control-plane=controller-manager",
				"-n", namespace, "--timeout=2m") // fails when no pod is left, which is fine
			sigs := sendAndFetchSignatures(dkimGenDomain, "e2e-dkim-operator-down")
			Expect(sigs).To(HaveLen(2))

			By("stopping Rspamd: the operator would scale it back, so it is stopped too")
			_, err = kubectl("scale", "deployment", "mail-rspamd", "-n", e2eNamespace, "--replicas=0")
			Expect(err).NotTo(HaveOccurred())
			_, _ = kubectl("wait", "--for=delete", "pod", "-l", dkimRspamdPods, "-n", e2eNamespace, "--timeout=2m")
			Eventually(func(g Gomega) {
				out := submitFromDomain(dkimGenDomain, "e2e-dkim-rspamd-down")
				g.Expect(out).NotTo(ContainSubstring("queued as"), "swaks output: %s", out)
				g.Expect(out).To(MatchRegexp(`(?m)^\s*<\S*\s+4\d\d[ -]`), "swaks output: %s", out)
			}, time.Minute, 5*time.Second).Should(Succeed())
			_, found, err := mailpitRawBySubject("e2e-dkim-rspamd-down")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "mail was accepted without a filter")

			By("starting Rspamd: the deferred mail goes through, signed")
			_, err = kubectl("scale", "deployment", "mail-rspamd", "-n", e2eNamespace, "--replicas=1")
			Expect(err).NotTo(HaveOccurred())
			out, err := kubectl("rollout", "status", "deployment", "mail-rspamd", "-n", e2eNamespace, "--timeout=3m")
			Expect(err).NotTo(HaveOccurred(), "rollout status output: %s", out)
			sigs = sendAndFetchSignatures(dkimGenDomain, "e2e-dkim-rspamd-up")
			Expect(sigs).To(ConsistOf(
				dkimSignature{algRSA, dkimGenDomain, active["rsa"]},
				dkimSignature{algEd25519, dkimGenDomain, active["ed25519"]},
			))

			By("starting the operator again")
			_, err = kubectl("scale", "deployment", dkimOperatorDeployment, "-n", namespace, "--replicas=1")
			Expect(err).NotTo(HaveOccurred())
			out, err = kubectl("rollout", "status", "deployment", dkimOperatorDeployment, "-n", namespace, "--timeout=3m")
			Expect(err).NotTo(HaveOccurred(), "rollout status output: %s", out)
		})
	})
}

// dkimDomainsYAML is the grants and the two MailDomains of the specs,
// which reference a server that does not exist (dkimDetached) until the
// setup moves them to the fixture server. The generated domain signs with
// RSA and Ed25519; the other uses the key of the Secret the specs create.
func dkimDomainsYAML() string {
	return fmt.Sprintf(`
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: %[2]s
spec:
  namespace: %[1]s
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: %[4]s
spec:
  namespace: %[1]s
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomain
metadata:
  name: %[3]s
  namespace: %[1]s
spec:
  domain: %[2]s
  serverRef:
    name: %[9]s
  dkim:
    ed25519: true
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomain
metadata:
  name: %[5]s
  namespace: %[1]s
spec:
  domain: %[4]s
  serverRef:
    name: %[9]s
  dkim:
    keys:
      - selector: %[6]s
        secretRef:
          name: %[7]s
          key: %[8]s
`, e2eNamespace, dkimGenDomain, dkimGenName, dkimOwnDomain, dkimOwnName,
		dkimOwnSelector, dkimOwnSecret, dkimOwnDataKey, dkimDetached)
}

// awaitKeysServed waits until the Rspamd pods mount the keys of both
// domains and the MailServer has rolled out. The domains are moved to the
// MailServer in the setup; the new configuration is validated first and
// rolled out one workload at a time.
func awaitKeysServed() {
	if dkimServed {
		return
	}
	Eventually(func(g Gomega) {
		volumes, err := kubectl("get", "deployment", "mail-rspamd", "-n", e2eNamespace,
			"-o", "jsonpath={.spec.template.spec.volumes}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(volumes).To(ContainSubstring(dkimGenName+"-dkim-"), "the generated keys are not mounted yet")
		g.Expect(volumes).To(ContainSubstring(dkimOwnSecret), "the own key is not mounted yet")
	}, 8*time.Minute, 5*time.Second).Should(Succeed())
	for _, w := range []string{"deployment/mail-rspamd", "statefulset/mail-postfix", "statefulset/mail-dovecot"} {
		out, err := kubectl("rollout", "status", w, "-n", e2eNamespace, "--timeout=5m")
		Expect(err).NotTo(HaveOccurred(), "rollout status %s: %s", w, out)
	}
	Eventually(func() (string, error) { return mailServerCondition("Ready") }, 5*time.Minute, 5*time.Second).
		Should(Equal("True"))
	dkimServed = true
}

// sendAndFetchSignatures submits a message from domain until the server
// accepts it (a rollout may still be settling) and returns the signatures
// of the copy the Mailpit sink receives. Every signature must verify
// against the records the MailDomain publishes in its status.
func sendAndFetchSignatures(domain, subject string) []dkimSignature {
	GinkgoHelper()
	mailDomain := dkimGenName
	if domain == dkimOwnDomain {
		mailDomain = dkimOwnName
	}
	md, err := getMailDomainObject(mailDomain)
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		out := submitFromDomain(domain, subject)
		g.Expect(out).To(ContainSubstring("Ok: queued as"), "swaks output: %s", out)
	}, 8*time.Minute, 10*time.Second).Should(Succeed())

	var sigs []dkimSignature
	var raw string
	Eventually(func(g Gomega) {
		var found bool
		var err error
		raw, found, err = mailpitRawBySubject(subject)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeTrue(), "the sink did not receive %q", subject)
		sigs, err = dkimSignatures(raw)
		g.Expect(err).NotTo(HaveOccurred())
	}, 5*time.Minute, 5*time.Second).Should(Succeed())

	verifications, err := verifyDKIM(raw, md.Status.Records)
	Expect(err).NotTo(HaveOccurred())
	Expect(verifications).To(HaveLen(len(sigs)))
	for _, v := range verifications {
		Expect(v.Err).NotTo(HaveOccurred(), "the signature of %s does not verify against status.records", v.Domain)
	}
	return sigs
}

// touchMailDomain changes an annotation of the MailDomain, which the
// controller sees at once, unlike a change of a Secret.
func touchMailDomain(name string) {
	GinkgoHelper()
	out, err := kubectl("annotate", "maildomain", name, "-n", e2eNamespace, "--overwrite",
		fmt.Sprintf("e2e.example.org/touched=%d", time.Now().UnixNano()))
	Expect(err).NotTo(HaveOccurred(), "annotate output: %s", out)
}

// keySecrets returns "name uid" of every key Secret of a MailDomain, so a
// replaced Secret differs from the one before.
func keySecrets(mailDomain string) []string {
	GinkgoHelper()
	out, err := kubectl("get", "secrets", "-n", e2eNamespace, "-l", "cnms.rpp.gmbh/maildomain="+mailDomain,
		"-o", `jsonpath={range .items[*]}{.metadata.name} {.metadata.uid}{"\n"}{end}`)
	Expect(err).NotTo(HaveOccurred(), "kubectl output: %s", out)
	lines := utils.GetNonEmptyLines(out)
	slices.Sort(lines)
	return lines
}
