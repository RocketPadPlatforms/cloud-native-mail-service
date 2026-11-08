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
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
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

// Account is one operator-managed account (ADR 0015, section 5). The
// insecureStaticUsers Secret (Phase 2 test source; see the MailServer
// docs) is one producer of accounts, and the service accounts are another
// (SendOnly). PasswordHash is a crypt(3) hash, never a plaintext password.
type Account struct {
	// Name is the account name: the login, and the mailbox directory.
	Name string
	// PasswordHash is the hash of the login. An account without one has
	// no login (its credential is missing), and still receives mail.
	PasswordHash string
	// Address is the primary (canonical) address.
	Address string
	// Disabled removes the login and the right to send; mail keeps being
	// delivered (ADR 0015, section 3, point 5).
	Disabled bool

	// Verdict is what addresses.Decide decided for the account: whether
	// it is suspended and one row per address (served, deferred, may
	// send). PlanAccounts only applies it, so that the status of a
	// MailUser and the tables that are rendered cannot differ. The
	// MailServer controller sets it from the MailUsers of the server.
	Verdict addresses.UserResult

	// Aliases are the further addresses of an account that has no
	// Verdict, named by DNS domain (the static test users, which have no
	// MailDomain objects): DecideAccounts feeds them to addresses.Decide
	// together with the primary address. They are ignored when Verdict is
	// set.
	Aliases []string

	// SendOnly marks the account of a MailServiceAccount (ADR 0015, section
	// 4.9): Name is its login (the reserved "sa." form), Address the sender
	// it may send as, and Verdict has that one row. It is a login (passdb,
	// by the name only) and an owner of the sender (the Postfix sender table
	// and the Rspamd table of owned From addresses), and nothing else: no
	// mailbox, no alias, no userdb line, so nothing is ever delivered to it.
	SendOnly bool
}

// DirectorySource is the external user directory of a MailServer (a
// MailDirectory, ADR 0015), with the secret values already read. Exactly
// one of LDAP and SQL is set. The renderers put the secret values into
// Secrets only, never into a ConfigMap.
type DirectorySource struct {
	LDAP *directory.LDAPTarget
	SQL  *directory.SQLTarget
}

// RenderRequest is the complete input for rendering the configuration
// of one component. It is a plain value: renderers are pure functions
// of it, which makes golden-file tests deterministic.
type RenderRequest struct {
	// Server is the MailServer to render for (name, namespace, spec).
	Server *cnmsv1alpha1.MailServer

	// AuthorizedDomains are the MailServer domains that passed the
	// domain authorization check (ADR-0003). Renderers must only
	// reference these. It may be empty (every domain revoked):
	// renderers then produce a fail-closed configuration that accepts
	// and sends no mail.
	AuthorizedDomains []string

	// Hostname is the hostname of the MailServer when it passed the
	// hostname authorization (ADR 0014); empty otherwise. It is the
	// name the server presents, see ServerHostname.
	Hostname string

	// Accounts are the operator-managed accounts: the MailUsers of the
	// server, with their verdict, or the parsed users of
	// spec.submission.insecureStaticUsers (empty when unset).
	Accounts []Account
	// Directory is the external user directory, or nil. It replaces
	// the accounts as the user source of Postfix and Dovecot.
	Directory *DirectorySource
	// ConflictedAddresses are the addresses that lost a conflict between
	// accounts (ADR 0015, section 4.2): they are served by nobody and
	// answer a temporary failure. The controller decides conflicts; the
	// renderers only apply the verdict.
	ConflictedAddresses []string

	// DKIMKeys are the active signing keys of the authorized domains
	// (ADR 0011). A domain without an entry has no key: it is still
	// served, and its outbound mail fails closed (ADR 0013).
	DKIMKeys []DomainKeys

	// HashKey keys the digest of the secret files in the config hash and
	// in the names of the Secrets (manifests.Build): a plain hash of a
	// bind password would let anyone who can read the pod template or the
	// Secret names confirm a guess offline. The renderers do not read it.
	HashKey []byte
}

// ServerHostname is the name the server presents (Postfix myhostname and
// HELO, the certificate): the authorized hostname, or, while the
// hostname is not authorized, the in-cluster Postfix Service name, which
// belongs to no tenant. It does not depend on the domains the server
// serves (ADR 0014).
func (r *RenderRequest) ServerHostname() string {
	if r.Hostname == "" {
		return ServiceFQDN(r.Server.Name, r.Server.Namespace, ComponentPostfix)
	}
	return r.Hostname
}

// RenderResult is the rendered configuration of one component.
type RenderResult struct {
	// ConfigData holds the rendered configuration files keyed by
	// relative path. The controller places them in a ConfigMap that
	// is mounted read-only into the component containers.
	ConfigData map[string]string

	// DataFiles holds rendered files that grow with the accounts and that the
	// engine reads from a directory of their own (the Rspamd table of the owned
	// header From addresses). The controller places them in an immutable,
	// hash-named ConfigMap of its own, mounted next to the main one; they are
	// part of the config hash, so a change rolls the workload. Empty for most
	// components.
	DataFiles map[string]string
	// SecretData holds rendered files that must not live in a
	// ConfigMap (the directory password and CA). The controller places
	// them in an immutable Secret that is mounted next to the ConfigMap.
	// Empty for most components.
	SecretData map[string]string
	// LiveData holds the files that the engine re-reads while it runs: the
	// passdb and userdb of Dovecot (ADR 0015, section 4.7). They go to one
	// Secret with a stable name that the controller updates in place, and
	// they are not part of the config hash, so a changed user is no new
	// revision and no restart. Empty for most components.
	LiveData map[string]string
	// SecretDigest is the keyed digest of SecretData that names the Secret
	// and enters the config hash. manifests.Build sets it; the renderers
	// leave it empty.
	SecretDigest string
	// Env is the environment the engine process needs beyond its
	// image (the PostgreSQL TLS settings of Postfix, which libpq reads
	// from the environment). It is part of the config hash. Empty for
	// most components.
	Env map[string]string

	// ValidateCommand runs in a validation Job with the new
	// configuration mounted. It must exit non-zero for an invalid
	// configuration, so a bad render never replaces a working one
	// (ADR 0009).
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
