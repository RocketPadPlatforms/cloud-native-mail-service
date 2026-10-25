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
	if len(req.AuthorizedDomains) == 0 {
		return nil, errors.New("no authorized domains")
	}
	mainCF := renderMainCF(req)
	return &render.RenderResult{
		ConfigData: map[string]string{
			"main.cf":   mainCF,
			"master.cf": renderMasterCF(req),
		},
		// `postfix check` validates the rendered configuration before
		// rollout (D4: a bad config must never replace a working one).
		ValidateCommand: []string{"postfix", "check"},
		// Certificate renewals trigger a reload, not a rollout (D5).
		ReloadCommand: []string{"postfix", "reload"},
	}, nil
}

// primaryDomain is the domain used for myhostname, the postmaster
// address and the certificate (D2). Domains are sorted so rendering is
// deterministic across replicas and reconciles.
func primaryDomain(req *render.RenderRequest) string {
	domains := slices.Clone(req.AuthorizedDomains)
	slices.Sort(domains)
	return domains[0]
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
// enabled. The API default is true (kubebuilder default).
func submissionEnabled(req *render.RenderRequest) bool {
	sub := req.Server.Spec.Submission
	return sub == nil || sub.Enabled == nil || *sub.Enabled
}
