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

// Package dovecot renders the Dovecot configuration (dovecot.conf,
// users) of a MailServer. Rendered values target Dovecot 2.4
// (ADR 0001); the TLS floor is ADR 0006. The rendered dovecot.conf is
// self-contained: mounted at /etc/dovecot it shadows the image's
// shipped configuration tree (ADR 0004).
package dovecot

import (
	"errors"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Renderer renders the Dovecot configuration.
type Renderer struct{}

// New returns a Dovecot renderer.
func New() *Renderer {
	return &Renderer{}
}

// Component implements render.EngineRenderer.
func (r *Renderer) Component() render.Component {
	return render.ComponentDovecot
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
			"dovecot.conf": renderDovecotConf(req),
			"users":        renderUsers(req),
		},
		// `doveconf -n` validates the rendered configuration before
		// rollout (D4: a bad config must never replace a working
		// one). Verified against the running 2.4.5 image: unknown
		// settings are fatal (exit code 89), so the check is strict.
		ValidateCommand: []string{"doveconf", "-n"},
		// `doveadm reload` reloads the running master without a
		// restart (verified: exit code 0 on 2.4.5 without
		// doveadm_password). Certificate renewals trigger a reload,
		// not a rollout (D5).
		ReloadCommand: []string{"doveadm", "reload"},
	}, nil
}

// mailboxesEnabled reports whether Dovecot mail storage is enabled.
func mailboxesEnabled(req *render.RenderRequest) bool {
	return req.Server.Spec.Mailboxes != nil &&
		req.Server.Spec.Mailboxes.Enabled != nil &&
		*req.Server.Spec.Mailboxes.Enabled
}
