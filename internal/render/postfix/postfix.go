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

// Package postfix renders the Postfix configuration (main.cf,
// master.cf) of a MailServer. Rendered values target Postfix 3.11
// (ADR 0001); the security floor is ADR 0006.
package postfix

import (
	"errors"
	"slices"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Floor values (ADR 0006). Not exposed in the v1 API.
const (
	// TLS.
	tlsMinProtocol    = ">=TLSv1.2"
	tlsMinCipherGrade = "medium"
	// Milter timeouts (deliberate; with milter_default_action=tempfail
	// these bound how long mail waits on a stuck filter).
	milterConnectTimeout = "10s"
	milterCommandTimeout = "30s"
	milterContentTimeout = "120s"
	// Anvil per-client rate limits.
	perClientConnectionLimit = 30
	perClientMessageLimit    = 60
	perClientRecipientLimit  = 100
	anvilTimeUnit            = "60s"
	// Message size.
	messageSizeLimit = 10485760 // 10 MiB
)

// validateScript is the Postfix config gate: `postfix check` must
// pass and `postconf -n` must not warn (stdout is the parameter dump
// and is discarded; only the warnings on stderr are inspected).
const validateScript = `set -eu
postfix check
warnings=$(postconf -n 2>&1 >/dev/null)
if [ -n "$warnings" ]; then
  echo "$warnings" >&2
  case "$warnings" in *warning:*) exit 1;; esac
fi`

// Renderer renders the Postfix configuration.
type Renderer struct{}

// New returns a Postfix renderer.
func New() *Renderer {
	return &Renderer{}
}

// Component implements render.EngineRenderer.
func (r *Renderer) Component() render.Component {
	return render.ComponentPostfix
}

// Render implements render.EngineRenderer.
func (r *Renderer) Render(req *render.RenderRequest) (*render.RenderResult, error) {
	if req == nil || req.Server == nil {
		return nil, errors.New("render request without MailServer")
	}
	mainCF := renderMainCF(req)
	return &render.RenderResult{
		ConfigData: map[string]string{
			"main.cf":    mainCF,
			"master.cf":  renderMasterCF(req),
			vmailboxFile: renderVMailbox(req),
		},
		// `postfix check` validates the rendered configuration before
		// rollout (D4: a bad config must never replace a working one).
		// It only warns (exit 0) about unknown main.cf parameters,
		// and so does `postconf -n` ("unused parameter" on stderr),
		// so any warning there fails the gate as well.
		ValidateCommand: []string{"sh", "-c", validateScript},
		// Certificate renewals trigger a reload, not a rollout (D5).
		ReloadCommand: []string{"postfix", "reload"},
	}, nil
}

// serverHostname is the name used for myhostname, which is also the HELO
// name and, through myorigin, the identity of bounces (ADR 0014): the
// authorized hostname, or the in-cluster Service name without one.
func serverHostname(req *render.RenderRequest) string {
	return req.ServerHostname()
}

// sortedDomains returns the authorized domains in sorted order.
func sortedDomains(req *render.RenderRequest) []string {
	domains := slices.Clone(req.AuthorizedDomains)
	slices.Sort(domains)
	return domains
}

// mailboxesEnabled reports whether Dovecot mail storage is enabled.
func mailboxesEnabled(req *render.RenderRequest) bool {
	return req.Server.Spec.Mailboxes != nil &&
		req.Server.Spec.Mailboxes.Enabled != nil &&
		*req.Server.Spec.Mailboxes.Enabled
}

// submissionEnabled reports whether authenticated submission (587) is
// enabled. The API default is true (kubebuilder default). Without an
// authorized domain nothing may be sent, so submission is off.
func submissionEnabled(req *render.RenderRequest) bool {
	if len(req.AuthorizedDomains) == 0 {
		return false
	}
	sub := req.Server.Spec.Submission
	return sub == nil || sub.Enabled == nil || *sub.Enabled
}
