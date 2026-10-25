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

// Package valkey renders the Valkey configuration (valkey.conf) of
// a MailServer. Rendered values target Valkey 9.x (ADR 0001); the
// image layout facts (entrypoint behaviour, run user, config path)
// are verified against valkey/valkey:9.1.2 (ADR 0004).
package valkey

import (
	"errors"
	"fmt"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// Renderer renders the Valkey configuration.
type Renderer struct{}

// New returns a Valkey renderer.
func New() *Renderer {
	return &Renderer{}
}

// Component implements render.EngineRenderer.
func (r *Renderer) Component() render.Component {
	return render.ComponentValkey
}

// Render implements render.EngineRenderer.
func (r *Renderer) Render(req *render.RenderRequest) (*render.RenderResult, error) {
	if req == nil || req.Server == nil {
		return nil, errors.New("render request without MailServer")
	}
	if len(req.AuthorizedDomains) == 0 {
		return nil, errors.New("no authorized domains")
	}
	// 9.1.2 has no config-check flag (`--check-config` is parsed
	// as a config directive), so the gate briefly starts the
	// server with the rendered configuration, pings it and shuts
	// it down. Verified against the running image: exit 0 for a
	// valid, 1 for an invalid configuration. D4: a bad config
	// must never replace a working one.
	validateCmd := fmt.Sprintf(
		"valkey-server %s --daemonize yes --pidfile /tmp/cnms-valkey.pid && sleep 1 && [ \"$(valkey-cli -p %d ping 2>/dev/null)\" = PONG ] && valkey-cli -p %d shutdown nosave",
		valkeyConfPath, render.PortValkey, render.PortValkey)
	return &render.RenderResult{
		ConfigData: map[string]string{
			"valkey.conf": renderValkeyConf(req),
		},
		ValidateCommand: []string{"sh", "-c", validateCmd},
		// No reload: Valkey applies its configuration at startup
		// and the pod has no certificate to reload; configuration
		// changes roll out via the content-hash annotation (D5).
	}, nil
}
