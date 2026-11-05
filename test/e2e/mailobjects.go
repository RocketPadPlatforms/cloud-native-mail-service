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

// Package e2e fixtures: the data plane and the test infrastructure of
// the MailServer e2e suite. The domain follows the example.*
// convention, the sink is the in-cluster Mailpit (ADR 0004, decision
// 7): its Service FQDN is a recipient domain whose MX-then-A lookup
// resolves to the sink inside the cluster.
package e2e

import (
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/gomega"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/test/utils"
)

// e2eNamespace is the namespace of the data plane under test.
const e2eNamespace = "cnms-e2e"

// e2eDomain is the mail domain of the MailDomainGrant and MailDomain.
const e2eDomain = "e2e.example.org"

// e2eHostname is the hostname of the MailServer under test: the name it
// presents (HELO, certificate), authorized by a grant of its own, so the
// grant of e2eDomain does not cover subdomains (ADR 0014).
const e2eHostname = "mail.e2e.example.org"

// e2eMailDomainName is the object name of the MailDomain (DNS-1123
// label form; the domain string lives in spec.domain).
const e2eMailDomainName = "e2eexampleorg"

// e2eUser is the static test user of the e2e-users Secret.
const e2eUser = "alice"

// e2ePassword is the password of e2eUser; the SHA512-CRYPT hash in
// the fixture (fixed salt) is its crypt(3) encoding.
const e2ePassword = "e2e-pass-123"

// e2eMailbox is the full mailbox address of e2eUser.
const e2eMailbox = e2eUser + "@" + e2eDomain

// e2eMailpitSink is the recipient domain of the outbound sink test:
// the cluster FQDN of the mailpit Service.
const e2eMailpitSink = "mailpit." + e2eNamespace + ".svc.cluster.local"

// e2ePostfixSVC is the cluster FQDN of the Postfix inbound Service.
const e2ePostfixSVC = "mail-postfix." + e2eNamespace + ".svc.cluster.local"

// e2eFixturesYAMLTemplate is the full fixture set applied to the
// cluster: namespace, the cert-manager Issuer, the credential and
// static-user Secrets, the MailDirectory, the MailDomainGrants of the
// domain and of the hostname (cluster-scoped, one each), the MailDomain,
// the MailServer under test, the
// Mailpit sink (Pod + Service) and the e2e client Pod (swaks,
// openssl, curl). The two %s are the Mailpit and the e2e client
// images (pinned in internal/images, ADR 0004).
//
// The MailServer runs with one replica per component (CI keeps the
// suite fast; HA behaviour is Phase 9), mailboxes enabled (so the
// inbound path delivers to the Dovecot mail volume) and the ClusterIP
// inbound service (kind has no load balancer).
const e2eFixturesYAMLTemplate = `
apiVersion: v1
kind: Namespace
metadata:
  name: cnms-e2e
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned-e2e
  namespace: cnms-e2e
spec:
  selfSigned: {}
---
apiVersion: v1
kind: Secret
metadata:
  name: sql-creds
  namespace: cnms-e2e
stringData:
  password: e2e-sql-pass
---
apiVersion: v1
kind: Secret
metadata:
  name: e2e-users
  namespace: cnms-e2e
stringData:
  users.yaml: |
    users:
      - username: alice
        passwordHash: "$6$e2efixedsalt$SViigPLFNLb6S8tH8GdMzgK5b7w.vDHWhZMFunPyTVvz9v3S3WyukyQO9ktCCfKMoGvLpyCFouCP4vIf.gl7i0"
        mailbox: alice@e2e.example.org
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDirectory
metadata:
  name: sql-e2e
  namespace: cnms-e2e
spec:
  type: sql
  sql:
    host: sql.cnms-e2e.svc
    database: maildir
    username: maildir
    passwordSecretRef:
      name: sql-creds
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: e2e.example.org
spec:
  namespace: cnms-e2e
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: mail.e2e.example.org
spec:
  namespace: cnms-e2e
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomain
metadata:
  name: e2eexampleorg
  namespace: cnms-e2e
spec:
  domain: e2e.example.org
  serverRef:
    name: mail
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailServer
metadata:
  name: mail
  namespace: cnms-e2e
spec:
  hostname: mail.e2e.example.org
  directoryRef:
    name: sql-e2e
  outbound:
    direct: {}
  tls:
    issuerRef:
      kind: Issuer
      name: selfsigned-e2e
  inbound:
    serviceType: ClusterIP
  submission:
    enabled: true
    insecureStaticUsers:
      secretName: e2e-users
  mailboxes:
    enabled: true
  postfix:
    replicas: 1
  rspamd:
    replicas: 1
---
apiVersion: v1
kind: Pod
metadata:
  name: mailpit
  namespace: cnms-e2e
  labels:
    app: mailpit
spec:
  containers:
    - name: mailpit
      image: %s
      # The SMTP listener runs on port 25 (like a real MX): the e2e
      # cluster's NetworkPolicy enforcement (kind kindnet) evaluates
      # service traffic on the post-DNAT tuple, so the Postfix pod's
      # port-25 egress allow (mail-egress-smtp) must match the target
      # port of the delivery path.
      args:
        - --smtp
        - "[::]:25"
      ports:
        - containerPort: 25
        - containerPort: 8025
---
apiVersion: v1
kind: Service
metadata:
  name: mailpit
  namespace: cnms-e2e
spec:
  selector:
    app: mailpit
  ports:
    - name: smtp
      port: 25
    - name: api
      port: 8025
---
apiVersion: v1
kind: Pod
metadata:
  name: e2e-client
  namespace: cnms-e2e
spec:
  containers:
    - name: client
      image: %s
      command: ["sleep", "infinity"]
`

// e2eFixturesYAML renders the fixture set with the pinned images.
func e2eFixturesYAML() string {
	return fmt.Sprintf(e2eFixturesYAMLTemplate, images.Mailpit, images.E2EClient)
}

// kubectl runs a kubectl command and returns its combined output.
func kubectl(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", args...))
}

// kubectlApplyYAML applies a multi-document manifest on stdin.
func kubectlApplyYAML(manifest string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply the e2e fixtures")
}

// swaks runs swaks in the e2e client Pod and returns the combined
// output (rejection or success is asserted on the output text).
func swaks(args ...string) string {
	full := append([]string{"exec", "-n", e2eNamespace, "e2e-client", "--", "swaks"}, args...)
	out, _ := kubectl(full...)
	return out
}

// mailServerCondition returns the status of the named MailServer
// condition.
func mailServerCondition(condType string) (string, error) {
	out, err := kubectl("get", "mailserver", "mail", "-n", e2eNamespace,
		"-o", `jsonpath={.status.conditions[?(@.type=='`+condType+`')].status}`)
	return strings.TrimSpace(out), err
}

// mailServerConditionMessage returns the message of the named
// MailServer condition.
func mailServerConditionMessage(condType string) (string, error) {
	out, err := kubectl("get", "mailserver", "mail", "-n", e2eNamespace,
		"-o", `jsonpath={.status.conditions[?(@.type=='`+condType+`')].message}`)
	return strings.TrimSpace(out), err
}

// mailServerConditionReason returns the reason of the named
// MailServer condition.
func mailServerConditionReason(condType string) (string, error) {
	out, err := kubectl("get", "mailserver", "mail", "-n", e2eNamespace,
		"-o", `jsonpath={.status.conditions[?(@.type=='`+condType+`')].reason}`)
	return strings.TrimSpace(out), err
}

// podTemplateConfigHash returns the rendered-config hash annotation of
// the Postfix StatefulSet pod template (the marker of a rollout).
// kubectl's jsonpath needs dots in the annotation key escaped; a
// bracketed key (['...']) silently evaluates to the empty string.
func podTemplateConfigHash() (string, error) {
	out, err := kubectl("get", "statefulset", "mail-postfix", "-n", e2eNamespace,
		"-o", `jsonpath={.spec.template.metadata.annotations.cnms\.rpp\.gmbh/config-hash}`)
	return strings.TrimSpace(out), err
}

// workloadReady reports an error unless the given workload reports
// exactly one ready replica.
func workloadReady(kind, name string) error {
	out, err := kubectl("get", kind, name, "-n", e2eNamespace,
		"-o", `jsonpath={.status.readyReplicas}`)
	if err != nil {
		return err
	}
	if ready := strings.TrimSpace(out); ready != "1" {
		return fmt.Errorf("%s/%s ready replicas = %s, want 1", kind, name, ready)
	}
	return nil
}

// e2eClientExec runs a command in the e2e client Pod.
func e2eClientExec(args ...string) (string, error) {
	return kubectl(append([]string{"exec", "-n", e2eNamespace, "e2e-client", "--"}, args...)...)
}

// postfixMetricsURL is the queue-metrics endpoint of the Postfix Service.
const postfixMetricsURL = "http://mail-postfix-metrics." + e2eNamespace + ".svc.cluster.local:9150/metrics"

// rspamdMetricsURL is the metrics sidecar endpoint of the Rspamd Service.
const rspamdMetricsURL = "http://mail-rspamd." + e2eNamespace + ".svc.cluster.local:9151/metrics"

// servedCertFingerprint returns the SHA-256 fingerprint of the
// certificate Postfix presents on the submission port (STARTTLS).
// The timeout keeps a stalled connection (milter timeouts delay the
// greeting) from blocking the caller.
func servedCertFingerprint() (string, error) {
	out, err := e2eClientExec("sh", "-c",
		"timeout 30 openssl s_client -starttls smtp -connect "+e2ePostfixSVC+":587 </dev/null 2>/dev/null"+
			" | openssl x509 -noout -fingerprint -sha256")
	return strings.TrimSpace(out), err
}

// servedCertNames returns the subject and the subject alternative names
// of the certificate Postfix presents on the submission port (STARTTLS).
func servedCertNames() (string, error) {
	out, err := e2eClientExec("sh", "-c",
		"timeout 30 openssl s_client -starttls smtp -connect "+e2ePostfixSVC+":587 </dev/null 2>/dev/null"+
			" | openssl x509 -noout -subject -ext subjectAltName")
	return strings.TrimSpace(out), err
}

// activePodCount returns the number of non-terminal Pods of the data
// plane namespace (what a pods ResourceQuota counts).
func activePodCount() (int, error) {
	out, err := kubectl("get", "pods", "-n", e2eNamespace, "-o", "name",
		"--field-selector=status.phase!=Succeeded,status.phase!=Failed")
	if err != nil {
		return 0, err
	}
	return len(strings.Fields(out)), nil
}

// postfixValidationJobs returns the names of the Postfix validation
// Jobs ("job.batch/<name>").
func postfixValidationJobs() ([]string, error) {
	out, err := kubectl("get", "jobs", "-n", e2eNamespace, "-o", "name",
		"-l", "cnms.rpp.gmbh/mailserver=mail,app.kubernetes.io/component=postfix")
	return strings.Fields(out), err
}
