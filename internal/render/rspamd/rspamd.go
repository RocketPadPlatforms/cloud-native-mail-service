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

// Package rspamd renders the Rspamd configuration (local.d overrides)
// of a MailServer. Rendered values target Rspamd 4.x (ADR 0001); the
// official image ships the configuration tree, CNMS overrides the
// local.d files only (ADR 0004).
package rspamd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Renderer renders the Rspamd configuration.
type Renderer struct{}

// New returns a Rspamd renderer.
func New() *Renderer {
	return &Renderer{}
}

// Component implements render.EngineRenderer.
func (r *Renderer) Component() render.Component {
	return render.ComponentRspamd
}

// Render implements render.EngineRenderer.
func (r *Renderer) Render(req *render.RenderRequest) (*render.RenderResult, error) {
	if req == nil || req.Server == nil {
		return nil, errors.New("render request without MailServer")
	}
	return &render.RenderResult{
		// Flat file names: the ConfigMap volume is mounted at
		// /etc/rspamd/local.d (internal/manifests), where ConfigMap
		// data keys become the files. ConfigMap keys may not contain
		// slashes, so the target directory is not part of the key.
		ConfigData: map[string]string{
			"worker-controller.inc": renderWorkerController(req),
			"worker-normal.inc":     renderWorkerNormal(req),
			"worker-proxy.inc":      renderWorkerProxy(req),
			"redis.conf":            renderRedis(req),
			"history_redis.conf":    renderHistoryRedis(req),
			"options.inc":           renderOptions(req),
			"dkim_signing.conf":     renderDKIMSigning(req),
			// Not a local.d file: the pod mounts it at
			// /etc/rspamd/rspamd.local.lua (internal/manifests).
			render.RspamdLocalLua: renderLocalLua(req),
		},
		// `rspamadm configtest` validates the rendered configuration
		// before rollout (D4: a bad config must never replace a
		// working one). Verified against the 4.2.3 image: the main
		// file is /usr/share/rspamd/config/rspamd.conf (it includes
		// the local.d overrides under /etc/rspamd), syntax errors
		// exit 1, and an unresolvable Valkey FQDN is only logged
		// (name resolution is deferred at config time). --strict is
		// not used: it counts those resolution logs as errors.
		ValidateCommand: []string{"rspamadm", "configtest", "-c", "/usr/share/rspamd/config/rspamd.conf"},
		// `rspamadm control reload` reloads the workers' dynamic
		// data via the master unix socket (verified against the
		// 4.2.3 source, src/rspamadm/control.c). Certificate
		// renewals trigger a reload, not a rollout (D5).
		ReloadCommand: []string{"rspamadm", "control", "reload"},
	}, nil
}

// sourceHeader is the rendered-by banner of a local.d file.
func sourceHeader(req *render.RenderRequest, file string) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Rspamd " + file + " -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0004)\n\n",
		req.Server.Namespace, req.Server.Name)
	return b.String()
}

// renderWorkerController renders local.d/worker-controller.inc.
func renderWorkerController(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/worker-controller.inc"))
	b.WriteString("# The controller worker serves the HTTP API. The shipped\n")
	b.WriteString("# secure_ip (127.0.0.1 and ::1) is kept: those clients need no\n")
	b.WriteString("# password. Every other client is refused (401) on all\n")
	b.WriteString("# endpoints, including /metrics, because no password is\n")
	b.WriteString("# configured: `null` (not an empty string) removes the shipped\n")
	b.WriteString("# default password \"q1\", so remote access stays closed.\n")
	b.WriteString("password = null;\n")
	b.WriteString(disableFileInputs)
	return b.String()
}

// renderWorkerNormal renders local.d/worker-normal.inc.
func renderWorkerNormal(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/worker-normal.inc"))
	b.WriteString(disableFileInputs)
	return b.String()
}

// renderWorkerProxy renders local.d/worker-proxy.inc. The proxy
// worker (milter on 11332) is the scanning entry point reachable from
// the Postfix pods.
func renderWorkerProxy(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/worker-proxy.inc"))
	b.WriteString(disableFileInputs)
	return b.String()
}

// disableFileInputs turns off the client-named message sources of a
// worker (verified against the worker configuration shipped in the
// Rspamd image pinned in internal/images/images.go; 4.2.3 when last
// verified).
const disableFileInputs = `# CNMS never submits messages via File/Path/Shm inputs;
# disabling them (the shipped default is true) keeps such
# inputs from being accepted.
allow_file_and_shm_inputs = false;
`

// renderHistoryRedis renders local.d/history_redis.conf. With Redis
// servers configured, the history module is active by default and
// stores sender, recipients, subject, Message-ID, client IP and
// authenticated user of every scanned message without expiry.
// Verified against the 4.2.3 image: this file together with
// history_rows = 0 (options.inc) stops it and no history key is
// written.
func renderHistoryRedis(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/history_redis.conf"))
	b.WriteString("# No message metadata in Valkey (ADR 0005). The WebUI\n")
	b.WriteString("# history is unavailable as a consequence.\n")
	b.WriteString("enabled = false;\n")
	return b.String()
}

// renderOptions renders local.d/options.inc (see renderHistoryRedis).
func renderOptions(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/options.inc"))
	b.WriteString("# No message history is kept (see history_redis.conf).\n")
	b.WriteString("history_rows = 0;\n")
	return b.String()
}

// renderRedis renders local.d/redis.conf.
func renderRedis(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/redis.conf"))
	b.WriteString("# One dedicated Valkey instance per MailServer (principle 6):\n")
	b.WriteString("# rate-limit, reputation and Bayes state are never shared\n")
	b.WriteString("# between servers.\n")
	fmt.Fprintf(&b, "servers = \"%s:%d\";\n",
		render.ServiceFQDN(req.Server.Name, req.Server.Namespace, render.ComponentValkey),
		render.PortValkey)
	return b.String()
}
