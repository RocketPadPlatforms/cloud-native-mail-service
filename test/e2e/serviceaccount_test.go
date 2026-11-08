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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
)

// The MailServiceAccount specs (ADR 0015, 3c): an application in a second
// namespace sends with the credential the operator makes, only as its sender,
// and loses it when the server withdraws its consent.

const (
	saNamespace      = "cnms-e2e-app"
	saOtherNamespace = "cnms-e2e-other"
	saName           = "notifications"
	// saLabelledPod carries the label that the NetworkPolicy of the account
	// selects, saPlainPod does not.
	saLabelledPod = "sa-client"
	saPlainPod    = "sa-plain"
	saSender      = "notifications@" + e2eDomain
	// saConsent is the consent of the server to saNamespace, for the domain of
	// the fixtures only.
	saConsent = `{"spec":{"serviceAccounts":{"allowedNamespaces":[{"name":"` + saNamespace + `","senderDomains":["` + e2eDomain + `"]}]}}}`
)

func namespaceYAML(name string) string {
	return fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", name)
}

func serviceAccountYAML(ns, name, sender string) string {
	return fmt.Sprintf(`
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailServiceAccount
metadata:
  name: %[2]s
  namespace: %[1]s
spec:
  serverRef:
    name: %[4]s
    namespace: %[5]s
  sender: %[3]s
`, ns, name, sender, e2eServer, e2eNamespace)
}

// secretValue reads one key of a Secret, decoded, or "" while it is missing.
func secretValue(ns, name, key string) string {
	out, err := kubectl("get", "secret", name, "-n", ns, "-o", `go-template={{index .data "`+key+`" | base64decode}}`)
	if err != nil {
		return ""
	}
	return out
}

func accountField(ns, name, jsonpath string) string {
	out, _ := kubectl("get", "mailserviceaccount", name, "-n", ns, "-o", "jsonpath="+jsonpath)
	return strings.TrimSpace(out)
}

func accountReady(ns, name string) (status, reason string) {
	return accountField(ns, name, `{.status.conditions[?(@.type=='Ready')].status}`),
		accountField(ns, name, `{.status.conditions[?(@.type=='Ready')].reason}`)
}

func objectExists(kind, name, ns string) bool {
	_, err := kubectl("get", kind, name, "-n", ns)
	return err == nil
}

// failureReport prints what a failed account spec needs.
func failureReport() {
	if !CurrentSpecReport().Failed() {
		return
	}
	for _, args := range [][]string{
		{"get", "mailserviceaccount", "-A", "-o", "yaml"},
		{"get", "mailserver", e2eServer, "-n", e2eNamespace, "-o", "yaml"},
		{"get", "networkpolicy,secret", "-n", saNamespace, "-o", "name"},
		{"logs", "-n", namespace, "-l", "control-plane=controller-manager", "--tail=60"},
		{"get", "events", "-n", e2eNamespace, "--sort-by=.lastTimestamp"},
	} {
		out, _ := kubectl(args...)
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n", strings.Join(args, " "), out)
	}
}

// shq quotes a word for sh.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// appPodsYAML is two pods of the application, in its own namespace, from the
// e2e client image, each with the credential Secret mounted at /creds: one
// carries the label that the NetworkPolicy of the account selects, one does not.
func appPodsYAML() string {
	pod := func(name, labels string) string {
		return fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
%[3]sspec:
  containers:
    - name: client
      image: %[4]s
      imagePullPolicy: IfNotPresent
      command: ["sleep", "infinity"]
      volumeMounts:
        - name: creds
          mountPath: /creds
          readOnly: true
  volumes:
    - name: creds
      secret:
        secretName: %[5]s-credential
`, name, saNamespace, labels, images.E2EClient, saName)
	}
	return pod(saLabelledPod, "  labels:\n    cnms.rpp.gmbh/mailserviceaccount: "+saName+"\n") + "---" + pod(saPlainPod, "")
}

// appExec runs a shell script in a pod of the application.
func appExec(pod, script string) (string, error) {
	return kubectl("exec", "-n", saNamespace, pod, "--", "sh", "-c", script)
}

// appCreds reads the credential Secret in the pod, like an application would.
const appCreds = `U=$(cat /creds/username); P=$(cat /creds/password); H=$(cat /creds/server); N=$(cat /creds/tlsServerName)
`

// appSwaks runs swaks in the labelled pod against the submission port, with the
// credential from the mounted Secret, over STARTTLS with the certificate
// verified against ca.crt and the name tlsServerName.
func appSwaks(extra ...string) string {
	script := appCreds + `swaks --server "$H:587" --tls --tls-verify --tls-ca-path /creds/ca.crt --tls-verify-target "$N" ` +
		`--auth PLAIN --auth-user "$U" --auth-password "$P" ` + strings.Join(extra, " ")
	out, _ := appExec(saLabelledPod, script)
	return out
}

// appMailFromReply is mailFromReply from the pod of the application.
func appMailFromReply(from string) string {
	out := appSwaks("--to", shq("sink@"+e2eMailpitSink), "--from", shq(from), "--quit-after", "RCPT")
	if m := rcptReplyRe.FindStringSubmatch(out); m != nil {
		return m[1] + m[2]
	}
	return out
}

// reachable runs a command in a pod and reports whether it succeeded.
func reachable(pod, command string) bool {
	_, err := appExec(pod, command)
	return err == nil
}

// registerServiceAccountDirectorySpecs checks the part of the account
// controller that a directory decides, on the PostgreSQL directory: a sender
// that is free is admitted after a lookup against the real database, a sender
// that belongs to a directory account is not. The accounts and the consent are
// removed again at once, so that no revision of the data plane is waited for.
func registerServiceAccountDirectorySpecs() {
	Context("service accounts on a directory", func() {
		AfterEach(failureReport)

		It("admits a free sender and refuses the address of a directory account", func() {
			awaitMailServerReady()
			kubectlApplyYAML(namespaceYAML(saNamespace))
			patchMailServer(saConsent)
			kubectlApplyYAML(serviceAccountYAML(saNamespace, "free", saSender) + "---" +
				serviceAccountYAML(saNamespace, "taken", e2eMailbox))

			By("the free sender is admitted for this server and directory, and gets its credential")
			Eventually(func() string { _, reason := accountReady(saNamespace, "free"); return reason }, 3*time.Minute, 3*time.Second).
				Should(Equal("Ready"))
			Expect(accountField(saNamespace, "free", "{.status.admittedSender}")).To(Equal(saSender))
			Expect(accountField(saNamespace, "free", "{.status.admittedFor}")).To(Equal(e2eNamespace + "/" + e2eServer + "/" + e2eSQLDirectory))
			Expect(secretValue(saNamespace, "free-credential", "username")).To(MatchRegexp(`^sa\.[0-9a-f]{16}$`))
			Expect(secretValue(saNamespace, "free-credential", "server")).To(Equal(e2ePostfixSVC))
			Expect(secretValue(saNamespace, "free-credential", "tls")).To(Equal("mandatory"))
			Expect(secretValue(saNamespace, "free-credential", "helo")).To(Equal(e2eHostname))
			Expect(secretValue(saNamespace, "free-credential", "tlsServerName")).To(Equal(e2eHostname))
			Expect(secretValue(saNamespace, "free-credential", "ca.crt")).To(HavePrefix("-----BEGIN CERTIFICATE-----"))
			Expect(secretValue(saNamespace, "free-credential", "password")).To(HaveLen(43))
			Expect(objectExists("networkpolicy", "free-submission", saNamespace)).To(BeTrue())

			By("the address of a directory account is refused, and nothing is issued for it")
			Eventually(func() string { _, reason := accountReady(saNamespace, "taken"); return reason }, 3*time.Minute, 3*time.Second).
				Should(Equal("SenderConflict"))
			Expect(accountField(saNamespace, "taken", "{.status.admittedSender}")).To(BeEmpty())
			Expect(objectExists("secret", "taken-credential", saNamespace)).To(BeFalse())
			Expect(objectExists("networkpolicy", "taken-submission", saNamespace)).To(BeFalse())
		})

		// The Dovecot of a server with a directory asks the passwd-file of the
		// service accounts first, and Postfix reads the union of the sender
		// table of the accounts and the directory's (ADR 0015, section 4.9).
		It("logs the account in through the Dovecot of the directory and sends as its sender, next to the directory's accounts", func() {
			awaitMailServerReady()
			login := secretValue(saNamespace, "free-credential", "username")
			password := secretValue(saNamespace, "free-credential", "password")
			Expect(login).To(MatchRegexp(`^sa\.[0-9a-f]{16}$`))
			since("the service account logs in on the directory server", func() bool { return loginWorks(login, password) })
			authRefused(login, "wrong-"+password, "a wrong password")

			By("the union sender table: the sender of the account, and not another address")
			Eventually(func() string { return mailFromReply(login, password, saSender) }, 8*time.Minute, 5*time.Second).
				Should(HavePrefix("250 "))
			Expect(mailFromReply(login, password, "other@"+e2eDomain)).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user "+login)))
			Expect(mailFromReply(login, password, e2eMailbox)).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user "+login)),
				"the address of a directory account is not the account's")
			Expect(mailFromReply(login, password, "<>")).To(HavePrefix("554 5.7.1"))

			By("the accounts of the directory keep their addresses, and have not the sender of the service account")
			Expect(authReply(e2eUser, e2ePassword)).To(HavePrefix("235 "))
			Expect(mailFromReply(e2eUser, e2ePassword, e2eMailbox)).To(HavePrefix("250 "))
			Expect(mailFromReply(e2eUser, e2ePassword, saSender)).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user "+e2eUser)))
		})

		It("leaves the server as it was", func() {
			_, _ = kubectl("delete", "mailserviceaccount", "free", "taken", "-n", saNamespace, "--ignore-not-found")
			patchMailServer(`{"spec":{"serviceAccounts":null}}`)
			_, _ = kubectl("delete", "namespace", saNamespace, "--ignore-not-found", "--wait=false")
			// The revision that the account may have started is settled: the
			// next specs expect a server that is Ready for its spec.
			awaitMailServerReady()
		})
	})
}

// registerServiceAccountSpecs runs on the server without a directory and
// without MailUsers that the MailUser specs leave: its only accounts are
// service accounts, so it is a send-only server (ADR 0015, section 3, point
// 7), and check 2 of the Rspamd guard applies to them.
func registerServiceAccountSpecs() {
	Context("service accounts on a send-only server", func() {
		var login, password string

		AfterEach(failureReport)

		It("refuses the namespace that the server does not consent to", func() {
			kubectlApplyYAML(namespaceYAML(saNamespace) + "---\n" + namespaceYAML(saOtherNamespace))
			kubectlApplyYAML(serviceAccountYAML(saOtherNamespace, saName, saSender))
			Eventually(func() string { _, reason := accountReady(saOtherNamespace, saName); return reason }, 3*time.Minute, 3*time.Second).
				Should(Equal("NamespaceNotAllowed"))
			Expect(objectExists("secret", saName+"-credential", saOtherNamespace)).To(BeFalse())
			Expect(objectExists("networkpolicy", saName+"-submission", saOtherNamespace)).To(BeFalse())
		})

		It("makes the credential and the egress policy in the namespace of the application", func() {
			patchMailServer(saConsent)
			kubectlApplyYAML(serviceAccountYAML(saNamespace, saName, saSender))
			Eventually(func() string { _, reason := accountReady(saNamespace, saName); return reason }, 3*time.Minute, 3*time.Second).
				Should(Equal("Ready"))

			login = secretValue(saNamespace, saName+"-credential", "username")
			password = secretValue(saNamespace, saName+"-credential", "password")
			Expect(login).To(MatchRegexp(`^sa\.[0-9a-f]{16}$`))
			Expect(password).To(HaveLen(43))
			Expect(secretValue(saNamespace, saName+"-credential", "server")).To(Equal(e2ePostfixSVC))
			Expect(secretValue(saNamespace, saName+"-credential", "tlsServerName")).To(Equal(e2eHostname))
			Expect(secretValue(saNamespace, saName+"-credential", "ca.crt")).To(HavePrefix("-----BEGIN CERTIFICATE-----"))
			port, err := kubectl("get", "networkpolicy", saName+"-submission", "-n", saNamespace, "-o",
				`jsonpath={.spec.podSelector.matchLabels.cnms\.rpp\.gmbh/mailserviceaccount} {.spec.egress[1].ports[0].port}`)
			Expect(err).NotTo(HaveOccurred(), port)
			Expect(strings.TrimSpace(port)).To(Equal(saName + " 587"))
		})

		// The application: pods in its own namespace that mount the credential
		// Secret, as the how-to says, one with the label of the policy.
		It("starts the pods of the application in its namespace, with the credential mounted", func() {
			kubectlApplyYAML(appPodsYAML())
			for _, pod := range []string{saLabelledPod, saPlainPod} {
				out, err := kubectl("wait", "pod", pod, "-n", saNamespace, "--for=condition=Ready", "--timeout=3m")
				Expect(err).NotTo(HaveOccurred(), out)
			}
			out, err := appExec(saLabelledPod, "ls /creds")
			Expect(err).NotTo(HaveOccurred(), out)
			Expect(strings.Fields(out)).To(ContainElements("username", "password", "server", "tls", "helo", "tlsServerName", "ca.crt"))
		})

		// ca.crt and tlsServerName are what an application verifies the server
		// with: from the pod of the application, over STARTTLS on the Service
		// named in the Secret.
		It("verifies the certificate of the server with ca.crt and the name in the Secret", func() {
			verify := `openssl s_client -starttls smtp -connect "$H:587" -CAfile /creds/ca.crt -verify_hostname "$%s" -verify_return_error -brief </dev/null 2>&1`
			out, err := appExec(saLabelledPod, appCreds+fmt.Sprintf(verify, "N"))
			Expect(err).NotTo(HaveOccurred(), out)
			Expect(out).To(ContainSubstring("Verification: OK"))
			Expect(out).To(ContainSubstring("Verified peername: " + e2eHostname))

			By("the name of the Service is not the name of the certificate: verifying against it fails")
			out, err = appExec(saLabelledPod, appCreds+fmt.Sprintf(verify, "H"))
			Expect(err).To(HaveOccurred(), out)
			Expect(out).NotTo(ContainSubstring("Verification: OK"))

			By("without ca.crt the certificate of the server is not trusted")
			out, err = appExec(saLabelledPod, appCreds+`openssl s_client -starttls smtp -connect "$H:587" -verify_hostname "$N" -verify_return_error -brief </dev/null 2>&1`)
			Expect(err).To(HaveOccurred(), out)
		})

		It("serves the account on a server that has no other user source", func() {
			awaitMailServerReady()
			Expect(mailServerConditionReason("Ready")).To(Equal("Ready"))
			since("the service account logs in", func() bool { return loginWorks(login, password) })
			authRefused(login, "wrong-"+password, "a wrong password")
			Expect(rcptReply(saSender)).To(HavePrefix("550 5.1.1"), "a service account receives nothing")
		})

		// From the pod of the application, with the credential of the Secret and
		// the certificate verified against ca.crt (swaks --tls-verify).
		It("lets the application send only as its sender, signed", func() {
			Expect(appMailFromReply(saSender)).To(HavePrefix("250 "))
			out := appSwaks("--to", shq("sink@"+e2eMailpitSink), "--from", shq(saSender), "--h-From", shq(saSender),
				"--header", shq("Subject: sa-send"), "--body", shq("service account e2e"))
			Expect(out).To(ContainSubstring("passed host verification (using host "+e2eHostname), out)
			Expect(out).To(ContainSubstring("Ok: queued as"), out)
			var raw string
			Eventually(func() bool {
				var found bool
				var err error
				raw, found, err = mailpitRawBySubject("sa-send")
				return err == nil && found
			}, 5*time.Minute, 5*time.Second).Should(BeTrue(), "the message reaches the sink")
			sigs, err := dkimSignatures(raw)
			Expect(err).NotTo(HaveOccurred())
			Expect(sigs).NotTo(BeEmpty(), "signed")
			Expect(sigs[0].Domain).To(Equal(e2eDomain))

			// Another address of the domain, a foreign domain and the null sender.
			Expect(appMailFromReply("other@" + e2eDomain)).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user "+login)))
			Expect(appMailFromReply("x@elsewhere.example.org")).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user")))
			Expect(appMailFromReply("<>")).To(And(HavePrefix("554 5.7.1"), ContainSubstring("null sender is not allowed")))
		})

		It("binds the header From to the sender", func() {
			out := appSwaks("--to", shq("sink@"+e2eMailpitSink), "--from", shq(saSender), "--h-From", shq("other@"+e2eDomain),
				"--header", shq("Subject: sa-from-refused"), "--body", shq("header From e2e"))
			Expect(out).To(And(ContainSubstring("554 5.7.1"), ContainSubstring("From address is not owned by the login")), out)
			_, found, err := mailpitRawBySubject("sa-from-refused")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "a refused message is not delivered")
		})

		// The policy selects the pods that carry the label, and allows them the
		// submission port of the server and DNS. kindnet enforces policies (ADR
		// 0004, item 7): a pod without the label reaches everything, the labelled
		// one reaches port 587 and no other port.
		It("limits the labelled pod to the submission port of the server", func() {
			mailpit := `curl -s -o /dev/null --max-time 5 ` + mailpitAPI + `/info`
			server := appCreds + `timeout 8 openssl s_client -starttls smtp -connect "$H:25" </dev/null >/dev/null 2>&1`
			Expect(reachable(saPlainPod, mailpit)).To(BeTrue(), "a pod without the label reaches the sink")
			Expect(reachable(saPlainPod, server)).To(BeTrue(), "and port 25 of the server")
			Eventually(func() bool { return reachable(saLabelledPod, mailpit) }, 2*time.Minute, 3*time.Second).
				Should(BeFalse(), "the labelled pod cannot reach the sink")
			Expect(reachable(saLabelledPod, server)).To(BeFalse(), "nor port 25 of the server")
			Expect(appMailFromReply(saSender)).To(HavePrefix("250 "), "port 587 works")
		})

		It("removes the credential and the policy when the server withdraws its consent, and the login stops", func() {
			patchMailServer(`{"spec":{"serviceAccounts":null}}`)
			Eventually(func() string { _, reason := accountReady(saNamespace, saName); return reason }, 3*time.Minute, 3*time.Second).
				Should(Equal("NamespaceNotAllowed"))
			Eventually(func() bool { return objectExists("secret", saName+"-credential", saNamespace) }, time.Minute, 2*time.Second).
				Should(BeFalse(), "the credential is deleted")
			Eventually(func() bool { return objectExists("networkpolicy", saName+"-submission", saNamespace) }, time.Minute, 2*time.Second).
				Should(BeFalse(), "the policy is deleted")
			Expect(accountField(saNamespace, saName, "{.status.admittedSender}")).To(BeEmpty())
			since("the withdrawn login is refused", func() bool { return loginRefused(login, password) })
		})

		It("leaves the cluster as it was", func() {
			_, _ = kubectl("delete", "mailserviceaccount", saName, "-n", saNamespace, "--ignore-not-found")
			_, _ = kubectl("delete", "mailserviceaccount", saName, "-n", saOtherNamespace, "--ignore-not-found")
			_, _ = kubectl("delete", "namespace", saNamespace, saOtherNamespace, "--ignore-not-found", "--wait=false")
		})
	})
}
