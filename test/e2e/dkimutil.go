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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/mail"
	"os/exec"
	"slices"
	"strings"

	msgdkim "github.com/emersion/go-msgauth/dkim"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// mailpitAPI is the API of the Mailpit sink, reached from the e2e client.
const mailpitAPI = "http://mailpit." + e2eNamespace + ".svc.cluster.local:8025/api/v1"

// getMailDomainObject reads a MailDomain of the data plane namespace.
func getMailDomainObject(name string) (*cnmsv1alpha1.MailDomain, error) {
	out, err := kubectl("get", "maildomain", name, "-n", e2eNamespace, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, out)
	}
	md := &cnmsv1alpha1.MailDomain{}
	if err := json.Unmarshal([]byte(out), md); err != nil {
		return nil, err
	}
	return md, nil
}

// mailDomainCondition returns the status of a condition of a MailDomain
// ("" when it is not set).
func mailDomainCondition(md *cnmsv1alpha1.MailDomain, condType string) metav1.ConditionStatus {
	if c := meta.FindStatusCondition(md.Status.Conditions, condType); c != nil {
		return c.Status
	}
	return ""
}

// activeSelectors returns the selector of every active key by algorithm.
func activeSelectors(md *cnmsv1alpha1.MailDomain) map[string]string {
	out := map[string]string{}
	if md.Status.DKIM != nil {
		for _, k := range md.Status.DKIM.Active {
			out[k.Algorithm] = k.Selector
		}
	}
	return out
}

// generatePrivateKeyPEM returns a new RSA-2048 key as a PKCS#8 PEM, one of
// the formats of ADR 0011 section 4.
func generatePrivateKeyPEM() (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// applyOwnKeySecret creates or replaces the content of the Secret that
// holds the bring-your-own key.
func applyOwnKeySecret(value string) {
	applyJSONManifest(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": dkimOwnSecret, "namespace": e2eNamespace},
		"stringData": map[string]string{dkimOwnDataKey: value},
	})
}

// submitFromDomain sends one message from the e2e user, with a header
// From in domain, through the authenticated submission port to the
// Mailpit sink. It returns the swaks transcript. The envelope sender is
// the mailbox the user owns in the directory: Postfix binds it to the
// login (ADR 0015, section 4.4), while Rspamd signs by the domain of the
// header From (allow_hdrfrom_mismatch) and the guard needs only a served
// domain there. swaks takes the header From from --h-From and the
// envelope sender from --from.
func submitFromDomain(domain, subject string) string {
	return swaks(
		"--to", "sink@"+e2eMailpitSink,
		"--from", e2eMailbox,
		"--h-From", e2eUser+"@"+domain,
		"--server", e2ePostfixSVC+":587",
		"--tls",
		"--auth", "PLAIN",
		"--auth-user", e2eUser,
		"--auth-password", e2ePassword,
		"--header", "Subject: "+subject,
		"--body", "dkim e2e",
	)
}

// e2eClientStdout runs a command in the e2e client Pod and returns its
// standard output only: kubectl warnings on stderr must not end up in a
// raw message. A failure carries the standard error.
func e2eClientStdout(args ...string) (string, error) {
	cmd := exec.Command("kubectl", append([]string{"exec", "-n", e2eNamespace, "e2e-client", "--"}, args...)...)
	out, err := cmd.Output()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		return string(out), fmt.Errorf("%w: %s", err, ee.Stderr)
	}
	return string(out), err
}

// mailpitRawBySubject returns the raw message the Mailpit sink holds under
// subject, and whether there is one. The search matches substrings, so
// the subjects it returns are compared exactly.
func mailpitRawBySubject(subject string) (string, bool, error) {
	list, err := e2eClientStdout("curl", "-s", "-G", mailpitAPI+"/search",
		"--data-urlencode", `query=subject:"`+subject+`"`, "--data-urlencode", "limit=100")
	if err != nil {
		return "", false, fmt.Errorf("%w: %s", err, list)
	}
	var messages struct {
		Messages []struct{ ID, Subject string }
	}
	if err := json.Unmarshal([]byte(list), &messages); err != nil {
		return "", false, fmt.Errorf("the Mailpit API did not answer with JSON: %w: %s", err, list)
	}
	for _, m := range messages.Messages {
		if m.Subject != subject {
			continue
		}
		raw, err := e2eClientStdout("curl", "-s", mailpitAPI+"/message/"+m.ID+"/raw")
		return raw, err == nil, err
	}
	return "", false, nil
}

// dnsName normalizes a DNS name for comparison: lower case, no trailing dot.
func dnsName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

// verifyDKIM verifies every DKIM-Signature of a raw message like a receiver
// would, against the TXT records the MailDomain publishes in status.records.
// The lookup answers only from those records and never falls back to the
// resolver of the host: the tests make no real DNS queries.
func verifyDKIM(raw string, records []cnmsv1alpha1.DNSRecord) ([]*msgdkim.Verification, error) {
	txt := map[string][]string{}
	for _, rec := range records {
		if rec.Type == "TXT" {
			txt[dnsName(rec.Name)] = append(txt[dnsName(rec.Name)], rec.Value)
		}
	}
	return msgdkim.VerifyWithOptions(strings.NewReader(raw), &msgdkim.VerifyOptions{
		LookupTXT: func(name string) ([]string, error) {
			if values, ok := txt[dnsName(name)]; ok {
				return values, nil
			}
			return nil, fmt.Errorf("no TXT record at %s", name)
		},
	})
}

// dkimSignature holds the tags of one DKIM-Signature header that the
// specs check.
type dkimSignature struct{ Algorithm, Domain, Selector string }

// dkimSignatures returns the DKIM-Signature headers of a raw message, in
// header order, sorted by selector.
func dkimSignatures(raw string) ([]dkimSignature, error) {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var sigs []dkimSignature
	for _, header := range msg.Header["Dkim-Signature"] {
		tags := map[string]string{}
		for part := range strings.SplitSeq(header, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
				tags[k] = strings.Join(strings.Fields(v), "")
			}
		}
		sigs = append(sigs, dkimSignature{Algorithm: tags["a"], Domain: tags["d"], Selector: tags["s"]})
	}
	slices.SortFunc(sigs, func(a, b dkimSignature) int { return strings.Compare(a.Selector, b.Selector) })
	return sigs, nil
}
