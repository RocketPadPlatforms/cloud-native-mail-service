//go:build e2e
// +build e2e

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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/test/utils"
)

// namespace where the project is deployed in
const namespace = "cloud-native-mail-service-system"

// serviceAccountName created for the project
const serviceAccountName = "cloud-native-mail-service-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "cloud-native-mail-service-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "cloud-native-mail-service-metrics-binding"

var _ = Describe("CNMS e2e", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("waiting for stale namespaces and fixtures of a previous run")
		// A previous (possibly interrupted) run tears down with
		// --wait=false; creating into a Terminating namespace or
		// re-applying the cluster-scoped grant fails, so wait for
		// the cleanup to finish first.
		for _, arg := range [][]string{
			{"wait", "--for=delete", "ns", e2eNamespace, "--timeout=3m"},
			{"wait", "--for=delete", "ns", namespace, "--timeout=3m"},
		} {
			cmd := exec.Command("kubectl", arg...)
			_, _ = utils.Run(cmd) // no-op when the namespace is gone
		}

		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("waiting for a stale MailDomainGrant of a previous run")
		cmd = exec.Command("kubectl", "wait", "--for=delete",
			"maildomaingrant", e2eDomain, "--timeout=3m")
		_, _ = utils.Run(cmd) // no-op when the grant is gone

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("enabling the static test users (Phase 2 test feature)")
		// kubectl 1.37 no longer has `set args`; replace the pod
		// template args with a JSON patch (keeping the args the
		// kustomize overlays add: metrics, leader election, probes).
		cmd = exec.Command("kubectl", "patch", "deployment",
			"cloud-native-mail-service-controller-manager", "-n", namespace,
			"--type", "json",
			"-p", `[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["--metrics-bind-address=:8443","--leader-elect","--health-probe-bind-address=:8081","--enable-insecure-test-features"]}]`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to set the operator args")

		By("waiting for the operator rollout")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment",
			"cloud-native-mail-service-controller-manager", "-n", namespace,
			"--timeout=3m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Operator rollout did not finish")

		By("applying the e2e fixtures (MailServer, Mailpit sink, client)")
		kubectlApplyYAML(e2eFixturesYAML())
	})

	// After all tests have been executed, clean up by removing the data
	// plane, undeploying the controller, uninstalling CRDs, and deleting
	// the namespace.
	AfterAll(func() {
		By("removing the data plane namespace")
		_, _ = kubectl("delete", "ns", e2eNamespace, "--wait=false", "--ignore-not-found")

		By("removing the MailDomainGrant (cluster-scoped)")
		_, _ = kubectl("delete", "maildomaingrant", e2eDomain, "--wait=false", "--ignore-not-found")

		By("cleaning up the curl pod for metrics")
		_, _ = kubectl("delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found")

		By("undeploying the controller-manager")
		_, _ = utils.Run(exec.Command("make", "undeploy"))

		By("uninstalling CRDs")
		_, _ = utils.Run(exec.Command("make", "uninstall"))

		By("removing manager namespace")
		_, _ = kubectl("delete", "ns", namespace, "--wait=false", "--ignore-not-found")
	})

	// After each test, check for failures and collect logs and events
	// for debugging.
	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		dump := func(name string, args ...string) {
			By("Fetching " + name)
			out, err := kubectl(args...)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "%s:\n%s\n", name, out)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get %s: %s\n", name, err)
			}
		}
		if controllerPodName != "" {
			dump("Controller logs", "logs", controllerPodName, "-n", namespace)
		}
		dump("MailServer conditions", "get", "mailserver", "mail", "-n", e2eNamespace,
			"-o", `jsonpath={.status.conditions}`)
		// --all-pods needs a selector on kubectl >= 1.37; the label
		// also matches the e2e client pod.
		dump("Data plane pod logs", "logs", "-n", e2eNamespace,
			"-l", "cnms.rpp.gmbh/managed-by=cnms", "--all-pods", "--tail=100")
		dump("Data plane events", "get", "events", "-n", e2eNamespace, "--sort-by=.lastTimestamp")
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(3 * time.Second)

	Context("operator", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("ensuring a ClusterRoleBinding for the service account to allow access to metrics")
			// apply (not create) so the binding survives re-runs on a
			// cluster that already ran this suite.
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ` + metricsRoleBindingName + `
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cloud-native-mail-service-metrics-reader
subjects:
  - kind: ServiceAccount
    name: ` + serviceAccountName + `
    namespace: ` + namespace + `
`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply the metrics ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("MailServer data plane", func() {
		It("should reach Ready with the data plane", func() {
			// First run pulls the engine images into the kind node;
			// allow generous time.
			verifyReady := func(g Gomega) {
				status, err := mailServerCondition("Ready")
				g.Expect(err).NotTo(HaveOccurred())
				message, _ := mailServerConditionMessage("Ready")
				g.Expect(status).To(Equal("True"),
					"MailServer Ready is not True yet (reason: %s, message: %s)",
					mustReason("Ready"), message)
			}
			Eventually(verifyReady, 12*time.Minute).Should(Succeed())
			for _, w := range []struct{ kind, name string }{
				{"statefulset", "mail-postfix"},
				{"statefulset", "mail-dovecot"},
				{"deployment", "mail-rspamd"},
				{"deployment", "mail-valkey"},
			} {
				Expect(workloadReady(w.kind, w.name)).NotTo(HaveOccurred(),
					"%s/%s not ready", w.kind, w.name)
			}
		})

		It("should serve the Postfix queue metrics as text/plain (ADR 0005)", func() {
			verifyMetrics := func(g Gomega) {
				// -L: busybox httpd redirects /metrics to /metrics/.
				out, err := e2eClientExec("curl", "-sL", postfixMetricsURL)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("cnms_postfix_queue_up 1"),
					"the queue sample must succeed: %s", out)
			}
			Eventually(verifyMetrics).Should(Succeed())
			ctype, err := e2eClientExec("curl", "-sL", "-o", "/dev/null",
				"-w", "%{content_type}", postfixMetricsURL)
			Expect(err).NotTo(HaveOccurred())
			Expect(ctype).To(HavePrefix("text/plain"), "Prometheus 3 rejects a scrape without a content type")
		})

		It("should serve the Rspamd metrics via the loopback sidecar as text/plain (ADR 0005)", func() {
			Eventually(func(g Gomega) {
				// -L: busybox httpd redirects /metrics to /metrics/.
				out, err := e2eClientExec("curl", "-sL", rspamdMetricsURL)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("cnms_rspamd_metrics_up 1"),
					"the metrics copy must succeed: %s", out)
			}).Should(Succeed())
			out, err := e2eClientExec("curl", "-sL", "-o", "/dev/null",
				"-w", "%{http_code} %{content_type}", rspamdMetricsURL)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(HavePrefix("200 text/plain"), "Prometheus 3 rejects a scrape without a content type")
		})

		It("should refuse the Rspamd controller API to other pods", func() {
			// The controller serves the learn, fuzzy and map endpoints
			// and the history; a pod outside the Rspamd pod must reach
			// none of them (the NetworkPolicy drops the port and the
			// controller refuses non-loopback clients with 401).
			podIP, err := kubectl("get", "pods", "-n", e2eNamespace,
				"-l", "cnms.rpp.gmbh/mailserver=mail,app.kubernetes.io/component=rspamd",
				"-o", "jsonpath={.items[0].status.podIP}")
			Expect(err).NotTo(HaveOccurred())
			podIP = strings.TrimSpace(podIP)
			Expect(podIP).NotTo(BeEmpty())
			base := "http://" + podIP + ":11334"
			for _, req := range []struct{ method, path string }{
				{"GET", "/metrics"},
				{"POST", "/learnspam"},
				{"POST", "/fuzzyadd"},
				{"POST", "/savemap"},
				{"GET", "/history"},
			} {
				// A dropped connection makes curl print 000 (and fail).
				code, _ := e2eClientExec("curl", "-s", "-m", "5", "-o", "/dev/null", "-w", "%{http_code}",
					"-X", req.method, "--data", "x", base+req.path)
				Expect(strings.TrimSpace(code)).NotTo(Equal("200"),
					"%s %s must not be served to another pod", req.method, req.path)
			}
		})

		It("should log the Postfix daemons to the pod stdout (ADR 0004)", func() {
			// postscreen logs every readiness-probe connection to the
			// maillog; its presence on stdout proves the daemons' log
			// client reaches postlogd (which writes maillog_file, here
			// /dev/stdout).
			verifyDaemonLogs := func(g Gomega) {
				out, err := kubectl("logs", "-n", e2eNamespace, "mail-postfix-0")
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read the Postfix pod logs")
				g.Expect(out).To(ContainSubstring("postfix/postscreen"),
					"daemon logs must reach the pod stdout (maillog_file=/dev/stdout)")
			}
			Eventually(verifyDaemonLogs).Should(Succeed())
		})

		It("should deliver unauthenticated mail for an own domain to the Dovecot mailbox", func() {
			out := swaks(
				"--to", e2eMailbox,
				"--from", "bob@client.example.org",
				"--server", e2ePostfixSVC+":25",
				"--header", "Subject: e2e-inbound",
				"--body", "inbound test",
			)
			Expect(out).To(ContainSubstring("Ok: queued as"),
				"swaks output: %s", out)

			// Delivery lands in the Dovecot maildir (Postfix logs the
			// LMTP outcome to stdout; Dovecot's accept response is
			// "Saved").
			verifyDelivered := func(g Gomega) {
				logs, err := kubectl("logs", "-n", e2eNamespace, "mail-postfix-0")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(logs).To(ContainSubstring("to=<"+e2eMailbox+">"),
					"no delivery for the inbound recipient in the Postfix logs")
				g.Expect(logs).To(ContainSubstring("Saved"),
					"Dovecot LMTP did not accept the message")
			}
			Eventually(verifyDelivered, 3*time.Minute).Should(Succeed())
		})

		It("should reject unknown recipients of an own domain at SMTP time", func() {
			// No bounce may be generated for a forged sender: the
			// recipient table (virtual_mailbox_maps) rejects at RCPT.
			out := swaks(
				"--to", "nobody@"+e2eDomain,
				"--from", "bob@client.example.org",
				"--server", e2ePostfixSVC+":25",
			)
			Expect(out).To(ContainSubstring("550 5.1.1"),
				"an unknown recipient must be rejected while the SMTP session is open: %s", out)
		})

		It("should accept authenticated submission over TLS", func() {
			out := swaks(
				"--to", e2eMailbox,
				"--from", e2eMailbox,
				"--server", e2ePostfixSVC+":587",
				"--tls",
				"--auth", "PLAIN",
				"--auth-user", e2eUser,
				"--auth-password", e2ePassword,
				"--header", "Subject: e2e-submission",
				"--body", "submission test",
			)
			Expect(out).To(ContainSubstring("Ok: queued as"),
				"authenticated submission over TLS failed: %s", out)
		})

		It("should deliver authenticated mail to the Mailpit sink", func() {
			out := swaks(
				"--to", "sink@"+e2eMailpitSink,
				"--from", e2eMailbox,
				"--server", e2ePostfixSVC+":587",
				"--tls",
				"--auth", "PLAIN",
				"--auth-user", e2eUser,
				"--auth-password", e2ePassword,
				"--header", "Subject: e2e-outbound",
				"--body", "outbound test",
			)
			Expect(out).To(ContainSubstring("Ok: queued as"),
				"swaks output: %s", out)

			// The sink receives the message (verified via the Mailpit
			// API from the e2e client Pod).
			verifySink := func(g Gomega) {
				api, err := kubectl("exec", "-n", e2eNamespace, "e2e-client", "--",
					"curl", "-s", "http://mailpit."+e2eNamespace+".svc.cluster.local:8025/api/v1/messages?limit=10")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(api).To(ContainSubstring("e2e-outbound"),
					"the sink did not receive the outbound message: %s", api)
			}
			Eventually(verifySink, 5*time.Minute).Should(Succeed())
		})

		It("should reject unauthenticated mail for foreign domains (no open relay)", func() {
			out := swaks(
				"--to", "other@relay-e2e.example.org",
				"--from", "bob@client.example.org",
				"--server", e2ePostfixSVC+":25",
			)
			Expect(out).To(ContainSubstring("554 5.7.1"),
				"unauthenticated foreign-domain mail must be rejected (reject_unauth_destination): %s", out)
		})

		It("should refuse unauthenticated mail on the submission port", func() {
			out := swaks(
				"--to", e2eMailbox,
				"--from", "bob@client.example.org",
				"--server", e2ePostfixSVC+":587",
				"--tls",
			)
			Expect(out).To(ContainSubstring("554 5.7.1"),
				"port 587 must only serve authenticated clients: %s", out)
		})

		It("should require TLS before AUTH (ADR 0006 floor)", func() {
			out := swaks(
				"--to", e2eMailbox,
				"--from", e2eMailbox,
				"--server", e2ePostfixSVC+":587",
				"--auth", "PLAIN",
				"--auth-user", e2eUser,
				"--auth-password", e2ePassword,
			)
			Expect(out).To(Or(ContainSubstring("Must issue a STARTTLS command first"),
				ContainSubstring("538"), ContainSubstring("501"),
				// smtpd_tls_auth_only=yes hides AUTH from the pre-TLS EHLO; swaks reports that.
				ContainSubstring("Host did not advertise authentication")),
				"AUTH without TLS must be rejected (smtpd_tls_auth_only): %s", out)
		})

		It("should not replace a working data plane with a broken spec", func() {
			hash, err := podTemplateConfigHash()
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).NotTo(BeEmpty(), "the config hash annotation is missing")

			By("breaking the spec (missing MailDirectory)")
			out, err := kubectl("patch", "mailserver", "mail", "-n", e2eNamespace,
				"--type", "merge",
				"-p", `{"spec":{"directoryRef":{"name":"missing-directory"}}}`)
			Expect(err).NotTo(HaveOccurred(), "patch output: %s", out)

			By("the MailServer becomes not ready with the blocking reason")
			verifyBlocked := func(g Gomega) {
				status, err := mailServerCondition("Ready")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal("False"))
				reason, err := mailServerConditionReason("Ready")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(reason).To(Equal("DirectoryNotFound"),
					"unexpected Ready reason: %s", reason)
			}
			Eventually(verifyBlocked, 2*time.Minute).Should(Succeed())

			By("the working data plane is unchanged (no rollout)")
			Expect(workloadReady("statefulset", "mail-postfix")).NotTo(HaveOccurred())
			Expect(workloadReady("statefulset", "mail-dovecot")).NotTo(HaveOccurred())
			Expect(workloadReady("deployment", "mail-rspamd")).NotTo(HaveOccurred())
			Expect(workloadReady("deployment", "mail-valkey")).NotTo(HaveOccurred())
			restored, err := podTemplateConfigHash()
			Expect(err).NotTo(HaveOccurred())
			Expect(restored).To(Equal(hash), "the pod template changed although the apply was blocked")

			By("restoring the spec")
			out, err = kubectl("patch", "mailserver", "mail", "-n", e2eNamespace,
				"--type", "merge",
				"-p", `{"spec":{"directoryRef":{"name":"sql-e2e"}}}`)
			Expect(err).NotTo(HaveOccurred(), "patch output: %s", out)
			Eventually(func() (string, error) {
				return mailServerCondition("Ready")
			}, 2*time.Minute).Should(Equal("True"), "the MailServer did not recover after the restore")
		})

		It("should not replace a working data plane with a config that fails validation", func() {
			// A rendered configuration is valid by construction, so
			// the failure is simulated: a pods quota keeps the
			// validation Job from starting a Pod, and the Job is then
			// marked failed through its status.
			hash, err := podTemplateConfigHash()
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).NotTo(BeEmpty())
			currentJob := "job.batch/mail-postfix-v-" + hash[:8]

			restore := func() {
				_, _ = kubectl("delete", "resourcequota", "block-new-pods", "-n", e2eNamespace, "--ignore-not-found")
				_, _ = kubectl("patch", "mailserver", "mail", "-n", e2eNamespace,
					"--type", "merge", "-p", `{"spec":{"submission":{"enabled":true}}}`)
			}
			DeferCleanup(restore)

			By("blocking new Pods in the data plane namespace")
			pods, err := activePodCount()
			Expect(err).NotTo(HaveOccurred())
			out, err := kubectl("create", "resourcequota", "block-new-pods", "-n", e2eNamespace,
				fmt.Sprintf("--hard=pods=%d", pods))
			Expect(err).NotTo(HaveOccurred(), "quota output: %s", out)

			By("changing the configuration (submission off changes the Postfix config)")
			out, err = kubectl("patch", "mailserver", "mail", "-n", e2eNamespace,
				"--type", "merge", "-p", `{"spec":{"submission":{"enabled":false}}}`)
			Expect(err).NotTo(HaveOccurred(), "patch output: %s", out)

			By("the new configuration waits for its validation Job")
			var newJob string
			verifyJob := func(g Gomega) {
				jobs, err := postfixValidationJobs()
				g.Expect(err).NotTo(HaveOccurred())
				for _, j := range jobs {
					if j != currentJob {
						newJob = j
					}
				}
				g.Expect(newJob).NotTo(BeEmpty(), "no validation Job for the new configuration yet")
			}
			Eventually(verifyJob).Should(Succeed())
			status, err := mailServerCondition("ConfigValidated")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal("Unknown"))

			By("failing the validation Job")
			now := time.Now().UTC().Format(time.RFC3339)
			failed := fmt.Sprintf(`{"status":{"startTime":%q,"failed":1,"conditions":[`+
				`{"type":"FailureTarget","status":"True","reason":"BackoffLimitExceeded","lastTransitionTime":%q},`+
				`{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"simulated validation failure","lastTransitionTime":%q}]}}`,
				now, now, now)
			out, err = kubectl("patch", newJob, "-n", e2eNamespace,
				"--subresource=status", "--type", "merge", "-p", failed)
			Expect(err).NotTo(HaveOccurred(), "patch output: %s", out)

			By("the MailServer reports the rejected configuration")
			verifyRejected := func(g Gomega) {
				reason, err := mailServerConditionReason("Ready")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(reason).To(Equal("ConfigRejected"))
				message, err := mailServerConditionMessage("Ready")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(message).To(ContainSubstring("mail-postfix-v-"))
			}
			Eventually(verifyRejected, 2*time.Minute).Should(Succeed())

			By("the working data plane is unchanged")
			Expect(workloadReady("statefulset", "mail-postfix")).NotTo(HaveOccurred())
			unchanged, err := podTemplateConfigHash()
			Expect(err).NotTo(HaveOccurred())
			Expect(unchanged).To(Equal(hash), "the pod template changed although the validation failed")

			By("restoring the spec: the running configuration needs no validation")
			restore()
			Eventually(func() (string, error) {
				return mailServerCondition("Ready")
			}, 3*time.Minute).Should(Equal("True"))
		})

		It("should serve a renewed certificate without restarting Postfix", func() {
			// cert-manager re-issues a deleted certificate Secret; the
			// kubelet updates the mounted files and the cert-reload
			// sidecar reloads Postfix (renewal => reload, not rollout).
			before, err := servedCertFingerprint()
			Expect(err).NotTo(HaveOccurred())
			Expect(before).To(ContainSubstring("Fingerprint"))
			uid, err := kubectl("get", "pod", "mail-postfix-0", "-n", e2eNamespace,
				"-o", "jsonpath={.metadata.uid}")
			Expect(err).NotTo(HaveOccurred())

			out, err := kubectl("delete", "secret", "mail-tls", "-n", e2eNamespace)
			Expect(err).NotTo(HaveOccurred(), "delete output: %s", out)

			verifyRenewed := func(g Gomega) {
				after, err := servedCertFingerprint()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(after).To(ContainSubstring("Fingerprint"))
				g.Expect(after).NotTo(Equal(before), "Postfix still serves the old certificate")
			}
			// Secret propagation (kubelet sync) plus the 30s poll.
			Eventually(verifyRenewed, 8*time.Minute, 10*time.Second).Should(Succeed())

			sameUID, err := kubectl("get", "pod", "mail-postfix-0", "-n", e2eNamespace,
				"-o", "jsonpath={.metadata.uid}")
			Expect(err).NotTo(HaveOccurred())
			Expect(sameUID).To(Equal(uid), "the certificate renewal restarted the Postfix pod")
			Eventually(func() (string, error) {
				return mailServerCondition("Ready")
			}, 3*time.Minute).Should(Equal("True"))
		})
	})
})

// managerImage is the manager image to be built and loaded for testing.
const managerImage = "example.com/cloud-native-mail-service:v0.0.1"

// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
var shouldCleanupCertManager = false

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind and CertManager.
//
// To enable kubectl kuberc (use custom kubectl configurations), set: KUBECTL_KUBERC=true
// By default, kuberc is disabled to ensure consistent test behavior across different environments.
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting cloud-native-mail-service e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("loading the manager and data-plane images on Kind")
	for _, img := range []string{managerImage, images.Postfix, images.E2EClient} {
		err = utils.LoadImageToKindClusterWithName(img)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load %s into Kind", img)
	}

	configureKubectlKubeRC()
	setupCertManager()
})

var _ = AfterSuite(func() {
	teardownCertManager()
})

// Disable kubectl kuberc by default for test isolation.
// This prevents local kubectl configurations from affecting test behavior.
// To enable kuberc, set: KUBECTL_KUBERC=true
func configureKubectlKubeRC() {
	if os.Getenv("KUBECTL_KUBERC") != "true" {
		By("disabling kubectl kuberc for test isolation")
		err := os.Setenv("KUBECTL_KUBERC", "false")
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to disable kubectl kuberc")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl kuberc disabled for consistent test behavior (override with KUBECTL_KUBERC=true)\n")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl kuberc enabled (KUBECTL_KUBERC=true)\n")
	}
}

// setupCertManager installs CertManager if needed for webhook tests.
// Skips installation if CERT_MANAGER_INSTALL_SKIP=true or if already present.
func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation (CERT_MANAGER_INSTALL_SKIP=true)\n")
		return
	}

	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	// Mark for cleanup before installation to handle interruptions and partial installs.
	shouldCleanupCertManager = true

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by setup)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}

// mustReason returns the Ready condition reason for diagnostic output
// (best effort; the condition may not exist yet).
func mustReason(condType string) string {
	reason, err := mailServerConditionReason(condType)
	if err != nil {
		return "<unavailable>"
	}
	return reason
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
