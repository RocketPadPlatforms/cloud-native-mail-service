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
	if len(req.AuthorizedDomains) == 0 {
		return nil, errors.New("no authorized domains")
	}
	return &render.RenderResult{
		ConfigData: map[string]string{
			"local.d/worker-controller.inc": renderWorkerController(req),
			"local.d/worker-normal.inc":     renderWorkerNormal(req),
			"local.d/redis.conf":            renderRedis(req),
		},
		// `rspamadm configtest` validates the rendered configuration
		// before rollout (D4: a bad config must never replace a
		// working one). Verified against the running 4.2.3 image:
		// rspamadm is at /usr/bin/rspamadm, the config test reads
		// /etc/rspamd (LOCAL_CONFDIR), and the Valkey FQDN need not
		// resolve (name resolution is deferred at config time).
		ValidateCommand: []string{"rspamadm", "configtest"},
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
	b.WriteString("# The controller worker serves the HTTP API (GET /metrics,\n")
	b.WriteString("# ADR 0005) without a password. `null` (not an empty string)\n")
	b.WriteString("# yields unauthenticated read-only access; an empty string\n")
	b.WriteString("# would require an empty password. Each MailServer has its\n")
	b.WriteString("# own Rspamd set and access is restricted by the\n")
	b.WriteString("# NetworkPolicies of ADR 0006.\n")
	b.WriteString("password = null;\n")
	b.WriteString("# CNMS never submits messages via File/Path/Shm inputs;\n")
	b.WriteString("# disabling them (the shipped default is true) keeps such\n")
	b.WriteString("# inputs from being accepted.\n")
	b.WriteString("allow_file_and_shm_inputs = false;\n")
	return b.String()
}

// renderWorkerNormal renders local.d/worker-normal.inc.
func renderWorkerNormal(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/worker-normal.inc"))
	b.WriteString("# CNMS never submits messages via File/Path/Shm inputs;\n")
	b.WriteString("# disabling them (the shipped default is true) keeps such\n")
	b.WriteString("# inputs from being accepted.\n")
	b.WriteString("allow_file_and_shm_inputs = false;\n")
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
