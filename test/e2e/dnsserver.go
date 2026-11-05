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

// The test DNS server of the MailDomain e2e specs: a CoreDNS pod that is
// authoritative for the test zone and nothing else (no forwarding, no
// recursion). The operator under test points --dns-resolver at it, so the
// records it verifies (ADR 0012) never leave the cluster.
package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/gomega"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/test/utils"
)

const (
	// dnsServerName names the Deployment, the Service and the Pod label.
	dnsServerName = "cnms-dns"
	// dnsZoneConfigMap holds the Corefile and the zone file.
	dnsZoneConfigMap = "cnms-dns-zone"
	// dnsZone is the only zone the server answers for; the test domains
	// live below it.
	dnsZone = "e2e.example.org"
	// dnsZoneKey is the ConfigMap key of the zone file.
	dnsZoneKey = dnsZone + ".zone"
	// dnsResolverAddr is the value of the operator's --dns-resolver flag.
	dnsResolverAddr = dnsServerName + "." + e2eNamespace + ".svc.cluster.local:53"
)

// dnsCorefile serves the zone from the mounted file. There is no forward
// and no cache plugin: a name outside the zone is refused, and every
// lookup reflects the file the pod started with. The zone is replaced by
// restarting the pod (publishZone), which is faster and more predictable
// than the kubelet's ConfigMap propagation.
const dnsCorefile = dnsZone + `:53 {
    errors
    ready
    file /zone/` + dnsZoneKey + `
}
`

// dnsServerYAMLTemplate is the Deployment and the Service. The single
// %s is the pinned CoreDNS image. CoreDNS binds port 53, so the container
// keeps NET_BIND_SERVICE (the image's binary carries that file capability
// and fails to start when it is dropped from the bounding set).
const dnsServerYAMLTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cnms-dns
  namespace: cnms-e2e
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cnms-dns
  template:
    metadata:
      labels:
        app: cnms-dns
    spec:
      containers:
        - name: coredns
          image: %s
          args: ["-conf", "/zone/Corefile"]
          ports:
            - {name: dns-udp, containerPort: 53, protocol: UDP}
            - {name: dns-tcp, containerPort: 53, protocol: TCP}
          readinessProbe:
            httpGet: {path: /ready, port: 8181}
            periodSeconds: 2
          resources:
            requests: {cpu: 10m, memory: 16Mi}
            limits: {memory: 64Mi}
          securityContext:
            runAsNonRoot: true
            runAsUser: 65532
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
              add: ["NET_BIND_SERVICE"]
            seccompProfile:
              type: RuntimeDefault
          volumeMounts:
            - {name: zone, mountPath: /zone, readOnly: true}
      volumes:
        - name: zone
          configMap:
            name: cnms-dns-zone
---
apiVersion: v1
kind: Service
metadata:
  name: cnms-dns
  namespace: cnms-e2e
spec:
  selector:
    app: cnms-dns
  ports:
    - {name: dns-udp, port: 53, protocol: UDP}
    - {name: dns-tcp, port: 53, protocol: TCP}
`

// zoneFile renders the test zone with the records recs. A TXT value is
// written as character-strings of at most 255 octets, the way a provider
// stores the RSA-2048 DKIM value; the resolver joins them again (RFC
// 6376). The MX records of the domains are served too (ADR 0014). The
// zone holds an address for the hostname, which the MX records point at,
// but no address records come from the status: the fixture server is of
// type ClusterIP and requires none.
func zoneFile(serial int64, recs []cnmsv1alpha1.DNSRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s.\n$TTL 60\n", dnsZone)
	fmt.Fprintf(&b, "@ IN SOA ns.%s. hostmaster.%s. %d 60 60 3600 60\n", dnsZone, dnsZone, serial)
	b.WriteString("@ IN NS ns\nns IN A 127.0.0.1\nmail IN A 203.0.113.10\n")
	for _, rec := range recs {
		name := strings.TrimSuffix(rec.Name, ".") + "."
		switch rec.Type {
		case "TXT":
			b.WriteString(name + " IN TXT")
			for _, chunk := range txtChunks(rec.Value) {
				fmt.Fprintf(&b, " %q", chunk)
			}
			b.WriteString("\n")
		case "MX":
			// The value is "<preference> <host>".
			fmt.Fprintf(&b, "%s IN MX %s.\n", name, strings.TrimSuffix(rec.Value, "."))
		default:
			panic("the test DNS server serves TXT and MX records only, got " + rec.Type)
		}
	}
	return b.String()
}

// txtChunks splits s into pieces of at most 255 bytes.
func txtChunks(s string) []string {
	const limit = 255
	var out []string
	for len(s) > limit {
		out, s = append(out, s[:limit]), s[limit:]
	}
	return append(out, s)
}

// applyZone writes the Corefile and the zone into the ConfigMap. The
// serial grows with every write, as a zone's serial has to.
func applyZone(recs []cnmsv1alpha1.DNSRecord) {
	applyJSONManifest(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": dnsZoneConfigMap, "namespace": e2eNamespace},
		"data": map[string]string{
			"Corefile": dnsCorefile,
			dnsZoneKey: zoneFile(time.Now().Unix(), recs),
		},
	})
}

// applyJSONManifest applies a manifest given as a Go value (JSON is YAML).
func applyJSONManifest(obj map[string]any) {
	manifest, err := json.Marshal(obj)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(manifest))
	out, err := utils.Run(cmd)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "kubectl apply output: %s", out)
}

// deployDNSServer starts the test DNS server with an empty zone and waits
// until it is ready.
func deployDNSServer() {
	applyZone(nil)
	kubectlApplyYAML(fmt.Sprintf(dnsServerYAMLTemplate, images.CoreDNS))
	waitDNSServer()
}

// publishZone replaces the served zone with recs and waits for the new
// pod, so a lookup that follows sees the records.
func publishZone(recs []cnmsv1alpha1.DNSRecord) {
	applyZone(recs)
	out, err := kubectl("rollout", "restart", "deployment", dnsServerName, "-n", e2eNamespace)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "rollout restart output: %s", out)
	waitDNSServer()
}

func waitDNSServer() {
	out, err := kubectl("rollout", "status", "deployment", dnsServerName, "-n", e2eNamespace, "--timeout=3m")
	gomega.ExpectWithOffset(2, err).NotTo(gomega.HaveOccurred(), "the test DNS server did not become ready: %s", out)
}
