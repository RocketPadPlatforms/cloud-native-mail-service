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
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// imapURL is the IMAPS Service of the Dovecot pod (implicit TLS; the
// certificate is the self-signed one of the MailServer, so curl skips the
// verification).
const imapURL = "imaps://mail-dovecot." + e2eNamespace + ".svc.cluster.local"

var (
	// rcptReplyRe finds the reply to the RCPT TO of a swaks transcript;
	// authReplyRe the reply to its AUTH.
	rcptReplyRe = regexp.MustCompile(`(?s)[-~]> RCPT TO:<[^>\n]*>\s*\n\s*<[-*~]+\s+(\d{3}(?: \d\.\d+\.\d+)?)([^\n]*)`)
	authReplyRe = regexp.MustCompile(`(?s)[-~]> AUTH [^\n]*\n\s*<[-*~]+\s+(\d{3}(?: \d\.\d+\.\d+)?)`)
)

// rcptReply opens a session on the inbound port, sends RCPT TO and returns
// the reply code and enhanced status (for example "250 2.1.5"). When the
// session never gets that far it returns the transcript.
func rcptReply(rcpt string) string {
	out := swaks("--to", rcpt, "--from", "bob@client.example.org",
		"--server", e2ePostfixSVC+":25", "--quit-after", "RCPT")
	if m := rcptReplyRe.FindStringSubmatch(out); m != nil {
		return m[1] + " "
	}
	return out
}

// authReply logs in on the submission port and returns the reply code and
// enhanced status to the AUTH (for example "235 2.7.0"), or the transcript.
func authReply(login, password string) string {
	out := swaks("--to", e2eMailbox, "--from", e2eMailbox,
		"--server", e2ePostfixSVC+":587", "--tls",
		"--auth", "PLAIN", "--auth-user", login, "--auth-password", password,
		"--quit-after", "AUTH")
	if m := authReplyRe.FindStringSubmatch(out); m != nil {
		return m[1] + " "
	}
	return out
}

// imapSearch logs in to Dovecot and returns the numbers of the INBOX
// messages with the given subject (a single word). A refused login is an
// error.
func imapSearch(login, password, subject string) ([]string, error) {
	out, err := e2eClientStdout("curl", "-sk", "-u", login+":"+password,
		"--url", imapURL+"/INBOX?SUBJECT%20"+subject)
	if err != nil {
		return nil, err
	}
	_, found, _ := strings.Cut(out, "* SEARCH")
	return strings.Fields(found), nil
}

// patchMailServer merge-patches the MailServer under test.
func patchMailServer(patch string) {
	out, err := kubectl("patch", "mailserver", e2eServer, "-n", e2eNamespace, "--type", "merge", "-p", patch)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "patch output: %s", out)
}

// awaitMailServerReady waits until the MailServer is Ready for its current
// spec: a changed user source is a new, validated revision of the data
// plane, which must have rolled out.
func awaitMailServerReady() {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, err := kubectl("get", "mailserver", e2eServer, "-n", e2eNamespace, "-o",
			`jsonpath={.metadata.generation} {.status.conditions[?(@.type=='Ready')].observedGeneration} {.status.conditions[?(@.type=='Ready')].status}`)
		g.Expect(err).NotTo(HaveOccurred())
		fields := strings.Fields(out)
		g.Expect(fields).To(HaveLen(3), "no Ready condition yet: %s", out)
		g.Expect(fields[1]).To(Equal(fields[0]), "Ready is not about the current spec yet")
		g.Expect(fields[2]).To(Equal("True"), "reason: %s", mustReason("Ready"))
	}, 10*time.Minute, 5*time.Second).Should(Succeed())
}

// directoryFixture describes one directory of directoryfixtures.go.
type directoryFixture struct {
	label      string // for the spec names
	directory  string // the MailDirectory
	deployment string // the fixture Deployment and Service (one name)
	// port is the port of the Service, targetPort the one of the pod.
	port, targetPort int
	subject          string // a word that tells the messages of the fixtures apart
	// disabled is an account that the directory switches off ("" if it
	// has none).
	disabled string
	// deadAliases are aliases whose account the directory does not serve (the
	// account is missing, switched off or has a guarded login): no recipient.
	deadAliases []string
}

// registerDirectorySpecs registers the specs of the user sources (ADR
// 0015): a directory for recipient validation, aliases and login, over
// PostgreSQL and over LDAP, and the rule that a server has one user source.
// They run inside the ordered e2e Describe and leave the MailServer on the
// PostgreSQL directory, as the MailDomain specs expect it.
//
// The MailServer is the same for both directories: the LDAP run switches its
// directoryRef, which is one validated rollout, instead of paying for a
// second server (seven more pods and their startup).
func registerDirectorySpecs() {
	Context("User sources", func() {
		AfterEach(func() {
			if !CurrentSpecReport().Failed() {
				return
			}
			for _, args := range [][]string{
				{"get", "maildirectory", "-n", e2eNamespace, "-o", "yaml"},
				{"get", "pods", "-n", e2eNamespace, "-o", "wide"},
				{"logs", "-n", e2eNamespace, "-l", "app=ldap", "--tail=60"},
				{"logs", "-n", e2eNamespace, "-l", "app=sql", "--tail=60"},
				{"get", "events", "-n", e2eNamespace, "--sort-by=.lastTimestamp"},
			} {
				out, _ := kubectl(args...)
				_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n", strings.Join(args, " "), out)
			}
		})

		directorySpecs(directoryFixture{
			label: "PostgreSQL", directory: e2eSQLDirectory, deployment: "sql", port: 5432, targetPort: 5432, subject: "e2e-sql",
			deadAliases: []string{"stale@" + e2eDomain, "guarded-alias@" + e2eDomain},
		})

		Context("with a second user source", func() {
			var runningHash string

			It("is not Ready and keeps the running revision", func() {
				var err error
				runningHash, err = podTemplateConfigHash()
				Expect(err).NotTo(HaveOccurred())
				Expect(runningHash).NotTo(BeEmpty())

				// The Event comes with the change of Ready, so the server
				// has to be Ready before.
				awaitMailServerReady()
				kubectlApplyYAML(e2eStaticUsersYAML)
				patchMailServer(`{"spec":{"submission":{"insecureStaticUsers":{"secretName":"e2e-users"}}}}`)

				Eventually(func(g Gomega) {
					reason, err := mailServerConditionReason("Ready")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(reason).To(Equal("UserSourceConflict"))
					status, err := mailServerCondition("Ready")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(status).To(Equal("False"))
				}).Should(Succeed())
				message, err := mailServerConditionMessage("Ready")
				Expect(err).NotTo(HaveOccurred())
				Expect(message).To(ContainSubstring("directoryRef"))
				Expect(message).To(ContainSubstring("submission.insecureStaticUsers"))

				By("an Event says so")
				Eventually(func(g Gomega) {
					out, err := kubectl("get", "events", "-n", e2eNamespace,
						"--field-selector", "reason=UserSourceConflict", "-o", "name")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty())
				}).Should(Succeed())

				By("the running data plane is unchanged and still serves the directory")
				hash, err := podTemplateConfigHash()
				Expect(err).NotTo(HaveOccurred())
				Expect(hash).To(Equal(runningHash), "the pod template changed although the spec is in conflict")
				Expect(workloadReady("statefulset", "mail-postfix")).NotTo(HaveOccurred())
				Expect(rcptReply(e2eMailbox)).To(HavePrefix("250 "))
				Expect(rcptReply(e2eStaticUser + "@" + e2eDomain)).To(HavePrefix("550 5.1.1"))
			})

			It("serves the static test users once the directory is removed", func() {
				patchMailServer(`{"spec":{"directoryRef":null}}`)
				awaitMailServerReady()

				Eventually(func() string { return rcptReply(e2eStaticUser + "@" + e2eDomain) }).Should(HavePrefix("250 "))
				Expect(rcptReply(e2eMailbox)).To(HavePrefix("550 5.1.1"), "the directory accounts are gone")
				Expect(authReply(e2eStaticUser, e2ePassword)).To(HavePrefix("235 "))
				authRefused(e2eUser, e2ePassword)
			})
		})

		Context("over LDAP", func() {
			It("switches the server to the LDAP directory", func() {
				patchMailServer(`{"spec":{"directoryRef":{"name":"` + e2eLDAPDirectory + `"},"submission":{"insecureStaticUsers":null}}}`)
				awaitMailServerReady()
			})

			directorySpecs(directoryFixture{
				label: "LDAP", directory: e2eLDAPDirectory, deployment: "ldap", port: 389, targetPort: 3890, subject: "e2e-ldap",
				disabled:    e2eDisabledUser,
				deadAliases: []string{"carol-alias@" + e2eDomain},
			})
		})

		registerMailUserSpecs() // mailuser_test.go

		Context("back on PostgreSQL", func() {
			It("returns the server to the PostgreSQL directory", func() {
				patchMailServer(`{"spec":{"directoryRef":{"name":"` + e2eSQLDirectory + `"}}}`)
				awaitMailServerReady()
				_, _ = kubectl("delete", "secret", "e2e-users", "-n", e2eNamespace, "--ignore-not-found")
				Eventually(func() string { return authReply(e2eUser, e2ePassword) }).Should(HavePrefix("235 "))
			})
		})
	})
}

// directorySpecs registers the specs of one directory, for a MailServer
// that uses it: its conditions, valid, alias and invalid recipients, the
// logins, the outage and the recovery.
func directorySpecs(f directoryFixture) {
	Context(f.label, func() {
		It("has a MailDirectory that is Ready and Reachable", func() {
			// The probe runs when the directory is created and then every
			// five minutes; the fixture may not have been up yet.
			Eventually(func(g Gomega) {
				for _, cond := range []string{"Ready", "Reachable"} {
					out, err := kubectl("get", "maildirectory", f.directory, "-n", e2eNamespace, "-o",
						`jsonpath={.status.conditions[?(@.type=='`+cond+`')].status}/{.status.conditions[?(@.type=='`+cond+`')].reason}`)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(HavePrefix("True/"), "%s of %s", cond, f.directory)
				}
			}, 8*time.Minute, 10*time.Second).Should(Succeed())
		})

		It("accepts valid recipients and an alias, and rejects invalid ones", func() {
			Expect(rcptReply(e2eMailbox)).To(HavePrefix("250 "))
			Expect(rcptReply(strings.ToUpper(e2eMailbox))).To(HavePrefix("250 "))
			Expect(rcptReply(e2eOtherUser + "@" + e2eDomain)).To(HavePrefix("250 "))
			Expect(rcptReply(e2eAlias)).To(HavePrefix("250 "), "an alias of the directory is a valid recipient")

			Expect(rcptReply("nobody@" + e2eDomain)).To(HavePrefix("550 5.1.1"))
			for _, dead := range f.deadAliases {
				Expect(rcptReply(dead)).To(HavePrefix("550 5.1.1"),
					"an alias of an account the directory does not serve is no recipient: mail for it would be accepted and bounce later")
			}
			if f.disabled != "" {
				Expect(rcptReply(f.disabled+"@"+e2eDomain)).To(HavePrefix("550 5.1.1"),
					"an account the directory switches off is no recipient")
			}
		})

		It("delivers a message for an alias once, into the canonical mailbox", func() {
			subject := f.subject + "-alias"
			out := swaks(
				"--to", e2eAlias,
				"--from", "bob@client.example.org",
				"--server", e2ePostfixSVC+":25",
				"--header", "Subject: "+subject,
				"--body", "alias test",
			)
			Expect(out).To(ContainSubstring("Ok: queued as"), "swaks output: %s", out)

			Eventually(func(g Gomega) {
				found, err := imapSearch(e2eUser, e2ePassword, subject)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(HaveLen(1), "the message is in the INBOX of %s exactly once", e2eUser)
			}, 5*time.Minute, 5*time.Second).Should(Succeed())

			found, err := imapSearch(e2eOtherUser, e2eOtherPassword, subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeEmpty(), "the message is in no other mailbox")
		})

		It("lets a directory user log in by name and by address, and not by alias", func() {
			Expect(authReply(e2eUser, e2ePassword)).To(HavePrefix("235 "), "by login name")
			Expect(authReply(e2eMailbox, e2ePassword)).To(HavePrefix("235 "), "by primary address")
			Expect(authReply(strings.ToUpper(e2eMailbox), e2ePassword)).To(HavePrefix("235 "), "by address in upper case")
			authRefused(e2eAlias, e2ePassword, "an alias is no login")
			authRefused(e2eUser, "wrong-"+e2ePassword, "a wrong password")
			authRefused("nobody", e2ePassword, "an unknown login")
			if f.disabled != "" {
				authRefused(f.disabled, "carol-pass-789", "a switched off account")
			}
		})

		It("reads mail over IMAP with the same logins", func() {
			subject := f.subject + "-alias"
			for _, login := range []string{e2eUser, e2eMailbox} {
				found, err := imapSearch(login, e2ePassword, subject)
				Expect(err).NotTo(HaveOccurred(), "IMAP login %s", login)
				Expect(found).To(HaveLen(1), "IMAP login %s sees the delivered message", login)
			}
			_, err := imapSearch(e2eAlias, e2ePassword, subject)
			Expect(err).To(HaveOccurred(), "an alias is no IMAP login either")
			_, err = imapSearch(e2eUser, "wrong-"+e2ePassword, subject)
			Expect(err).To(HaveOccurred(), "a wrong password")
		})

		It("answers with temporary failures while the directory is down, and recovers", func() {
			// The outage: the Service of the directory forwards to a port
			// where nothing listens, so connections are refused at once
			// (like a stopped server; a Service without endpoints would
			// have the network policy of the fixtures drop the packets).
			outage := func(targetPort int) {
				out, err := kubectl("patch", "service", f.deployment, "-n", e2eNamespace, "--type", "merge",
					"-p", fmt.Sprintf(`{"spec":{"ports":[{"name":%q,"port":%d,"targetPort":%d}]}}`,
						f.deployment, f.port, targetPort))
				ExpectWithOffset(1, err).NotTo(HaveOccurred(), "patch output: %s", out)
			}
			// A failed spec must not leave the directory down.
			DeferCleanup(func() { outage(f.targetPort) })

			outage(9999)
			// The engines keep their connections open, and a changed
			// Service does not end them; a new pod does.
			out, err := kubectl("delete", "pod", "-l", "app="+f.deployment, "-n", e2eNamespace, "--wait")
			Expect(err).NotTo(HaveOccurred(), "delete output: %s", out)

			By("recipients get 451, never a permanent rejection")
			Eventually(func() string { return rcptReply(e2eMailbox) }).Should(HavePrefix("451 4.3.0"))
			Expect(rcptReply("nobody@"+e2eDomain)).To(HavePrefix("451 4.3.0"), "an unknown address is not rejected either")
			Expect(rcptReply(e2eAlias)).To(HavePrefix("451 4.3.0"))

			By("nobody logs in while the directory is down")
			// ADR 0015 (row 16) captured 454 4.7.0 for a directory that
			// was stopped under Dovecot. A directory that refuses new
			// connections, as here, is answered 535 5.7.8 (Dovecot logs
			// "Not connected to database" and the internal failure
			// reaches the client as a failed login), so only a login that
			// does not succeed is asserted; the reply is logged.
			reply := authReply(e2eUser, e2ePassword)
			_, _ = fmt.Fprintf(GinkgoWriter, "login reply while the directory is down: %s\n", reply)
			Expect(reply).NotTo(HavePrefix("235 "), "no login without the directory")

			By("the server stays Ready: an outage of the directory is not a failure of the data plane")
			status, err := mailServerCondition("Ready")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal("True"))

			By("the directory comes back")
			outage(f.targetPort)
			// The PostgreSQL client of Postfix waits about a minute after
			// a failed connection before it tries again.
			Eventually(func() string { return rcptReply(e2eMailbox) }, 6*time.Minute, 5*time.Second).Should(HavePrefix("250 "))
			Expect(rcptReply("nobody@" + e2eDomain)).To(HavePrefix("550 5.1.1"))
			Eventually(func() string { return authReply(e2eUser, e2ePassword) }, 3*time.Minute, 5*time.Second).Should(HavePrefix("235 "))
		})
	})
}

// authRefused expects the login to be refused with 535. Dovecot slows down
// repeated failed logins of one client, and Postfix then sometimes reports
// a lost connection to the authentication server (454) for one of them, so
// a 454 is tried again; the refusal itself must come.
func authRefused(login, password string, why ...string) {
	GinkgoHelper()
	Eventually(func() string { return authReply(login, password) }, 3*time.Minute, 10*time.Second).
		Should(HavePrefix("535 "), strings.Join(why, " "))
}
