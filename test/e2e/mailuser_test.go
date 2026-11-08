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
)

// The MailUser specs (ADR 0015, 3b): accounts with aliases in two domains,
// the live Dovecot files, strict sender binding, and no restart for a
// changed account. They run on the e2e MailServer after its directory is
// removed (a server has one user source).

const (
	// e2eSecondDomain is the second domain of the aliases; its MailDomain
	// object is e2eSecondDomainName.
	e2eSecondDomain     = "e2etwo.example.org"
	e2eSecondDomainName = "e2etwo"

	muMain     = "mailuser"
	muMainPW   = "mu-main-pass-123"
	muOther    = "muother"
	muOtherPW  = "mu-other-pass-123"
	muOff      = "mudisabled"
	muOffPW    = "mu-off-pass-123"
	muLate     = "mulate"
	muLatePW1  = "mu-late-pass-111"
	muLatePW2  = "mu-late-pass-222"
	muMainAddr = muMain + "@" + e2eDomain
	// muAlias and muSales are the aliases of muMain in the second domain.
	muAlias = muMain + "@" + e2eSecondDomain
	muSales = "sales@" + e2eSecondDomain

	// muLatencyBound is how long the specs wait for the kubelet to project
	// a changed Secret and Dovecot to read it (ADR 0015, section 4.7).
	muLatencyBound = 5 * time.Minute
)

// mailUserYAML is a MailUser with a password from a Secret.
func mailUserYAML(name, password string, extra string) string {
	return fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: %[1]s-password
  namespace: cnms-e2e
stringData:
  password: %[2]s
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailUser
metadata:
  name: %[1]s
  namespace: cnms-e2e
spec:
  serverRef:
    name: mail
  domainRef:
    name: %[3]s
  passwordSecretRef:
    name: %[1]s-password
%[4]s`, name, password, e2eMailDomainName, extra)
}

// e2eMailUsersYAML is the second domain and the first three accounts.
func e2eMailUsersYAML() string {
	return fmt.Sprintf(`
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: %[1]s
spec:
  namespace: cnms-e2e
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomain
metadata:
  name: %[2]s
  namespace: cnms-e2e
spec:
  domain: %[1]s
  serverRef:
    name: mail
`, e2eSecondDomain, e2eSecondDomainName) +
		"---" + mailUserYAML(muMain, muMainPW, fmt.Sprintf(`  aliases:
    - localPart: %s
      domainRef:
        name: %s
    - localPart: sales
      domainRef:
        name: %s
`, muMain, e2eSecondDomainName, e2eSecondDomainName)) +
		"---" + mailUserYAML(muOther, muOtherPW, "") +
		"---" + mailUserYAML(muOff, muOffPW, "  disabled: true\n")
}

// mailFromReply logs in on the submission port, gives a sender and a
// recipient, and returns the reply to the RCPT TO, where Postfix judges the
// sender (smtpd_delay_reject):
// for example "250 2.1.5 Ok" or "553 5.7.1 <...>: Sender address rejected: ...",
// or the transcript.
func mailFromReply(login, password, from string) string {
	out := swaks("--to", "sink@"+e2eMailpitSink, "--from", from,
		"--server", e2ePostfixSVC+":587", "--tls",
		"--auth", "PLAIN", "--auth-user", login, "--auth-password", password,
		"--quit-after", "RCPT")
	if m := rcptReplyRe.FindStringSubmatch(out); m != nil {
		return m[1] + m[2]
	}
	return out
}

// dovecotPodState is the identity of the Dovecot pod and the restarts of
// its containers: it must not change when an account does.
func dovecotPodState() string {
	GinkgoHelper()
	out, err := kubectl("get", "pod", "mail-dovecot-0", "-n", e2eNamespace, "-o",
		`jsonpath={.metadata.uid}/{.status.containerStatuses[*].restartCount}`)
	Expect(err).NotTo(HaveOccurred(), out)
	return strings.TrimSpace(out)
}

// since polls f until it returns true and reports how long that took.
func since(what string, f func() bool) time.Duration {
	GinkgoHelper()
	start := time.Now()
	Eventually(f, muLatencyBound, 3*time.Second).Should(BeTrue(), what)
	took := time.Since(start).Round(time.Second)
	AddReportEntry(what, took.String())
	_, _ = fmt.Fprintf(GinkgoWriter, "%s: %s\n", what, took)
	return took
}

func loginWorks(login, password string) bool {
	return strings.HasPrefix(authReply(login, password), "235 ")
}

func loginRefused(login, password string) bool {
	return strings.HasPrefix(authReply(login, password), "535 ")
}

func deleteMailUser(name string) {
	_, _ = kubectl("delete", "mailuser", name, "-n", e2eNamespace, "--ignore-not-found")
	_, _ = kubectl("delete", "secret", name+"-password", "-n", e2eNamespace, "--ignore-not-found")
}

func registerMailUserSpecs() {
	Context("over MailUsers", func() {
		var dovecotBefore string

		AfterEach(func() {
			if !CurrentSpecReport().Failed() {
				return
			}
			for _, args := range [][]string{
				{"get", "mailuser", "-n", e2eNamespace, "-o", "yaml"},
				{"get", "mailserver", e2eServer, "-n", e2eNamespace, "-o", "yaml"},
				{"get", "pods", "-n", e2eNamespace, "-o", "wide"},
				{"logs", "-n", e2eNamespace, "mail-dovecot-0", "-c", "dovecot", "--tail=80"},
				{"get", "events", "-n", e2eNamespace, "--sort-by=.lastTimestamp"},
			} {
				out, _ := kubectl(args...)
				_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n", strings.Join(args, " "), out)
			}
		})

		It("refuses the MailUsers while the server has a directory", func() {
			awaitMailServerReady()
			running, err := podTemplateConfigHash()
			Expect(err).NotTo(HaveOccurred())

			kubectlApplyYAML(e2eMailUsersYAML())
			Eventually(func() string {
				reason, _ := mailServerConditionReason("Ready")
				return reason
			}, 3*time.Minute, 3*time.Second).Should(Equal("UserSourceConflict"))
			message, _ := mailServerConditionMessage("Ready")
			Expect(message).To(ContainSubstring("MailUsers"))
			hash, err := podTemplateConfigHash()
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).To(Equal(running), "the running revision is not replaced")
			Eventually(func() string {
				out, _ := kubectl("get", "mailuser", muMain, "-n", e2eNamespace, "-o",
					`jsonpath={.status.conditions[?(@.type=='Ready')].reason}`)
				return out
			}, 2*time.Minute, 3*time.Second).Should(Equal("DirectoryConfigured"))
		})

		It("serves them once the directory is removed", func() {
			patchMailServer(`{"spec":{"directoryRef":null}}`)
			awaitMailServerReady()

			Eventually(func() string { return rcptReply(muMainAddr) }, 2*time.Minute, 5*time.Second).Should(HavePrefix("250 "))
			Eventually(func() bool { return loginWorks(muMain, muMainPW) }, muLatencyBound, 5*time.Second).Should(BeTrue())
			// After the first login: the pod has its final configuration.
			dovecotBefore = dovecotPodState()
			Expect(rcptReply(e2eMailbox)).To(HavePrefix("550 5.1.1"), "the directory accounts are gone")
			Eventually(func() string {
				out, _ := kubectl("get", "mailserver", e2eServer, "-n", e2eNamespace, "-o",
					`jsonpath={.status.conditions[?(@.type=='AddressesServed')].status}`)
				return out
			}, 2*time.Minute, 3*time.Second).Should(Equal("True"))
		})

		It("receives on every address of an account, into one mailbox", func() {
			for _, addr := range []string{muMainAddr, muAlias, muSales, muOther + "@" + e2eDomain, muOff + "@" + e2eDomain} {
				Expect(rcptReply(addr)).To(HavePrefix("250 "), addr)
			}
			Expect(rcptReply("nobody@" + e2eSecondDomain)).To(HavePrefix("550 5.1.1"))
			Expect(rcptReply("nobody@" + e2eDomain)).To(HavePrefix("550 5.1.1"))

			for i, addr := range []string{muMainAddr, muAlias, muSales} {
				subject := fmt.Sprintf("mu-recv-%d", i)
				out := swaks("--to", addr, "--from", "bob@client.example.org", "--server", e2ePostfixSVC+":25",
					"--header", "Subject: "+subject, "--body", "mailuser e2e")
				Expect(out).To(ContainSubstring("Ok: queued as"), out)
			}
			// Both login forms read the one mailbox; each message is there once.
			for i := range 3 {
				subject := fmt.Sprintf("mu-recv-%d", i)
				Eventually(func(g Gomega) {
					found, err := imapSearch(muMain, muMainPW, subject)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(found).To(HaveLen(1), subject)
				}, 5*time.Minute, 5*time.Second).Should(Succeed())
				found, err := imapSearch(muMainAddr, muMainPW, subject)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(HaveLen(1))
				other, err := imapSearch(muOther, muOtherPW, subject)
				Expect(err).NotTo(HaveOccurred())
				Expect(other).To(BeEmpty(), "in no other mailbox")
			}
		})

		It("logs an account in by name and by primary address, never by alias", func() {
			Expect(authReply(muMain, muMainPW)).To(HavePrefix("235 "))
			Expect(authReply(muMainAddr, muMainPW)).To(HavePrefix("235 "))
			Expect(authReply(strings.ToUpper(muMainAddr), muMainPW)).To(HavePrefix("235 "))
			authRefused(muAlias, muMainPW, "an alias is no login")
			authRefused(muMain, "wrong-"+muMainPW, "a wrong password")
			_, err := imapSearch(muAlias, muMainPW, "x")
			Expect(err).To(HaveOccurred())
		})

		It("lets an account send as each of its addresses, and as no other", func() {
			for i, addr := range []string{muMainAddr, muAlias, muSales} {
				Expect(mailFromReply(muMain, muMainPW, addr)).To(HavePrefix("250 "), "own address "+addr)
				subject := fmt.Sprintf("mu-send-%d", i)
				out := swaks("--to", "sink@"+e2eMailpitSink, "--from", addr, "--h-From", muMainAddr,
					"--server", e2ePostfixSVC+":587", "--tls", "--auth", "PLAIN",
					"--auth-user", muMain, "--auth-password", muMainPW,
					"--header", "Subject: "+subject, "--body", "mailuser e2e")
				Expect(out).To(ContainSubstring("Ok: queued as"), out)
				Eventually(func() bool {
					_, found, err := mailpitRawBySubject(subject)
					return err == nil && found
				}, 5*time.Minute, 5*time.Second).Should(BeTrue(), "the message from "+addr+" reaches the sink")
			}
			// Another account's address, a served address nobody owns, a
			// foreign domain and the null sender.
			Expect(mailFromReply(muMain, muMainPW, muOther+"@"+e2eDomain)).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user")))
			Expect(mailFromReply(muMain, muMainPW, "nobody@"+e2eDomain)).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user")))
			Expect(mailFromReply(muMain, muMainPW, "x@elsewhere.example.org")).To(And(HavePrefix("553 5.7.1"), ContainSubstring("not owned by user")))
			Expect(mailFromReply(muMain, muMainPW, "<>")).To(And(HavePrefix("554 5.7.1"), ContainSubstring("null sender is not allowed")))
		})

		It("keeps a disabled account out of the logins and delivers its mail", func() {
			authRefused(muOff, muOffPW, "a disabled account cannot log in (SASL)")
			_, err := imapSearch(muOff, muOffPW, "x")
			Expect(err).To(HaveOccurred(), "a disabled account cannot log in (IMAP)")

			out := swaks("--to", muOff+"@"+e2eDomain, "--from", "bob@client.example.org", "--server", e2ePostfixSVC+":25",
				"--header", "Subject: mu-disabled-mail", "--body", "still delivered")
			Expect(out).To(ContainSubstring("Ok: queued as"), out)

			// Enabled again, the account reads what was delivered meanwhile.
			out, err = kubectl("patch", "mailuser", muOff, "-n", e2eNamespace, "--type", "merge", "-p", `{"spec":{"disabled":false}}`)
			Expect(err).NotTo(HaveOccurred(), out)
			since("a re-enabled account logs in", func() bool { return loginWorks(muOff, muOffPW) })
			Eventually(func(g Gomega) {
				found, err := imapSearch(muOff, muOffPW, "mu-disabled-mail")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(HaveLen(1))
			}, 5*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("adds, changes and removes an account with no Dovecot restart", func() {
			By("adding")
			kubectlApplyYAML(mailUserYAML(muLate, muLatePW1, ""))
			added := since("a new account logs in", func() bool { return loginWorks(muLate, muLatePW1) })

			By("changing the password")
			out, err := kubectl("patch", "secret", muLate+"-password", "-n", e2eNamespace, "--type", "merge",
				"-p", `{"stringData":{"password":"`+muLatePW2+`"}}`)
			Expect(err).NotTo(HaveOccurred(), out)
			changed := since("the new password works", func() bool { return loginWorks(muLate, muLatePW2) })
			since("the old password is refused", func() bool { return loginRefused(muLate, muLatePW1) })

			By("removing")
			deleteMailUser(muLate)
			removed := since("a removed account is refused", func() bool { return loginRefused(muLate, muLatePW2) })

			Expect(dovecotPodState()).To(Equal(dovecotBefore), "Dovecot was not restarted")

			By("doveadm reaches the master in the pod (the runbook ends sessions with it)")
			out, err = kubectl("exec", "-n", e2eNamespace, "mail-dovecot-0", "-c", "dovecot", "--", "doveadm", "who")
			Expect(err).NotTo(HaveOccurred(), out)
			out, err = kubectl("exec", "-n", e2eNamespace, "mail-dovecot-0", "-c", "dovecot", "--", "doveadm", "kick", muLate)
			_, _ = fmt.Fprintf(GinkgoWriter, "doveadm kick of an account without a session: err=%v out=%q\n", err, out)
			AddReportEntry("doveadm kick, no session", fmt.Sprintf("err=%v out=%q", err, strings.TrimSpace(out)))
			AddReportEntry("latency of an account change (kubelet projection included)",
				fmt.Sprintf("add %s, password %s, remove %s", added, changed, removed))
		})

		It("stops the addresses of a revoked alias domain, and keeps the account", func() {
			out, err := kubectl("delete", "maildomain", e2eSecondDomainName, "-n", e2eNamespace)
			Expect(err).NotTo(HaveOccurred(), out)
			Eventually(func() string { return rcptReply(muSales) }, 6*time.Minute, 5*time.Second).Should(HavePrefix("554 5.7.1"))
			// Postfix is fail closed for the moment, the narrowed revision still
			// validates: the accounts of the domain that stays keep their logins
			// (the live files are written by the narrowed render only).
			Expect(authReply(muMain, muMainPW)).To(HavePrefix("235 "))
			Expect(authReply(muOther, muOtherPW)).To(HavePrefix("235 "))
			awaitMailServerReady()
			Eventually(func() string { return rcptReply(muMainAddr) }, 3*time.Minute, 5*time.Second).Should(HavePrefix("250 "))
			Expect(rcptReply(muAlias)).To(HavePrefix("554 5.7.1"))
			Expect(mailFromReply(muMain, muMainPW, muAlias)).To(HavePrefix("553 5.7.1"), "no sending as an address of a revoked domain")
			Expect(authReply(muMain, muMainPW)).To(HavePrefix("235 "))
			Eventually(func() string {
				out, _ := kubectl("get", "mailuser", muMain, "-n", e2eNamespace, "-o",
					`jsonpath={.status.conditions[?(@.type=='AddressesServed')].reason}`)
				return out
			}, 2*time.Minute, 3*time.Second).Should(Equal("PartiallyServed"))
		})

		It("leaves the server without MailUsers", func() {
			for _, name := range []string{muMain, muOther, muOff, muLate} {
				deleteMailUser(name)
			}
			_, _ = kubectl("delete", "maildomaingrant", e2eSecondDomain, "--ignore-not-found")
			Eventually(func() string {
				out, _ := kubectl("get", "mailuser", "-n", e2eNamespace, "-o", "name")
				return strings.TrimSpace(out)
			}, time.Minute, 2*time.Second).Should(BeEmpty())
		})
	})
}
