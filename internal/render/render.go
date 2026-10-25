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

// Package render defines the engine-neutral configuration rendering
// contract (principle 3): the CRDs do not leak Postfix/Rspamd/Dovecot
// syntax, and config rendering sits behind an interface so another
// engine can be added later.
package render

import (
	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// Component identifies a data-plane component.
type Component string

// Data-plane components of a MailServer.
const (
	ComponentPostfix Component = "postfix"
	ComponentRspamd  Component = "rspamd"
	ComponentDovecot Component = "dovecot"
	ComponentValkey  Component = "valkey"
)

// StaticUser is one test user from the insecureStaticUsers Secret
// (Phase 2 test source; see the MailServer docs). PasswordHash is a
// crypt(3) hash, never a plaintext password.
type StaticUser struct {
	Username     string
	PasswordHash string
	Mailbox      string
}

// RenderRequest is the complete input for rendering the configuration
// of one component. It is a plain value: renderers are pure functions
// of it, which makes golden-file tests deterministic.
type RenderRequest struct {
	// Server is the MailServer to render for (name, namespace, spec).
	Server *cnmsv1alpha1.MailServer

	// AuthorizedDomains are the MailServer domains that passed the
	// domain authorization check (ADR-0003). Renderers must only
	// reference these; the first entry is the primary domain
	// (HELO name, certificate SAN order, postmaster address).
	AuthorizedDomains []string

	// StaticUsers holds the parsed users from
	// spec.submission.insecureStaticUsers (empty when unset).
	StaticUsers []StaticUser
}

// RenderResult is the rendered configuration of one component.
type RenderResult struct {
	// ConfigData holds the rendered configuration files keyed by
	// relative path. The controller places them in a ConfigMap that
	// is mounted read-only into the component containers.
	ConfigData map[string]string

	// ValidateCommand runs in an init container with the rendered
	// configuration mounted (and the queue volume attached). It must
	// exit non-zero for an invalid configuration, so a bad render
	// never replaces a working one (D4 of the Phase 2 plan).
	ValidateCommand []string

	// ReloadCommand reloads the running engine without a restart.
	// The certificate-reload sidecar executes it when the TLS
	// certificate changes (renewal triggers a reload, not a rollout).
	ReloadCommand []string
}

// EngineRenderer renders the configuration of one mail engine.
type EngineRenderer interface {
	// Component returns the component this renderer renders for.
	Component() Component

	// Render produces the component configuration for the request.
	Render(req *RenderRequest) (*RenderResult, error)
}
