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

package controller

import (
	"fmt"
	"slices"
	"strings"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// Domain authorization condition reasons (ADR 0003).
const (
	// ReasonAuthorized is set when every claimed domain is granted.
	ReasonAuthorized = "Authorized"
	// ReasonAuthorizationDisabled is set in unrestricted mode.
	ReasonAuthorizationDisabled = "AuthorizationDisabled"
	// ReasonNotGranted is set when no grant covers a domain for the
	// server's namespace.
	ReasonNotGranted = "NotGranted"
	// ReasonDuplicateDomain is set when a domain is claimed by more
	// than one MailDomain among the competing claimants (the owning
	// namespace in allowlist mode); every one is blocked (fail
	// closed).
	ReasonDuplicateDomain = "DuplicateDomain"
	// ReasonPartiallyAuthorized is set when some, but not all, of the
	// referenced domains are authorized: only those are served.
	ReasonPartiallyAuthorized = "PartiallyAuthorized"
	// ReasonDomainNotFound is set when a referenced MailDomain is
	// missing.
	ReasonDomainNotFound = "DomainNotFound"
)

// domainAuthorizationMode selects how MailServer domains are
// authorized (ADR 0003).
type domainAuthorizationMode string

// ParseDomainAuthorization validates the --domain-authorization flag
// value.
func ParseDomainAuthorization(s string) (domainAuthorizationMode, error) {
	switch m := domainAuthorizationMode(s); m {
	case AuthorizationAllowlist, AuthorizationUnrestricted:
		return m, nil
	}
	return "", fmt.Errorf("--domain-authorization must be 'allowlist' or 'unrestricted' (got %q)", s)
}

// Operator startup modes for --domain-authorization.
const (
	// AuthorizationAllowlist requires a MailDomainGrant covering each
	// domain and naming the server's namespace.
	AuthorizationAllowlist domainAuthorizationMode = "allowlist"
	// AuthorizationUnrestricted skips the grant requirement (single
	// tenant clusters). Uniqueness is still enforced.
	AuthorizationUnrestricted domainAuthorizationMode = "unrestricted"
)

// domainClaim is one claimed domain, from a MailDomain object.
type domainClaim struct {
	name      string // MailDomain object name
	namespace string
	domain    string // spec.domain
}

// grantRef is one MailDomainGrant.
type grantRef struct {
	domain            string // the grant name (the domain)
	namespace         string
	includeSubdomains bool
}

// authorizationEvaluator evaluates the domain authorization of one
// MailServer (ADR 0003). It is a pure function of the current grants
// and claims: there is no cached decision and no "older wins"
// tie-break, so narrowing a grant revokes access on the next
// reconcile and restores cannot move a domain to another tenant.
type authorizationEvaluator struct {
	server *cnmsv1alpha1.MailServer
	grants []grantRef
	claims []domainClaim
	mode   domainAuthorizationMode
}

// authorization is the outcome of evaluating one MailServer: the
// authorized subset of its domains and one failure per other domain.
// A failing domain never takes the others down; the controller
// renders only the authorized subset (fail closed per domain).
type authorization struct {
	// domains are the authorized domains, sorted; the first entry is
	// the primary domain (HELO, certificate SAN, postmaster). Empty
	// when none is authorized.
	domains  []string
	failures []domainFailure
	mode     domainAuthorizationMode
}

// domainFailure is one domain that is not authorized.
type domainFailure struct {
	reason  string
	message string
}

// all reports whether every referenced domain is authorized.
func (a authorization) all() bool { return len(a.failures) == 0 }

// condition returns the DomainsAuthorized reason and message. With
// no failure it is Authorized (or AuthorizationDisabled); with some
// authorized domains left it is PartiallyAuthorized; otherwise it is
// the reason of the first failure. The message lists every failure.
func (a authorization) condition() (string, string) {
	if a.all() {
		if a.mode == AuthorizationUnrestricted {
			return ReasonAuthorizationDisabled, "domain authorization is disabled (--domain-authorization=unrestricted)"
		}
		return ReasonAuthorized, "all domains are granted for this namespace"
	}
	msgs := make([]string, 0, len(a.failures))
	for _, f := range a.failures {
		msgs = append(msgs, f.message)
	}
	reason := a.failures[0].reason
	msg := strings.Join(msgs, "; ")
	if len(a.domains) > 0 {
		reason = ReasonPartiallyAuthorized
		msg = fmt.Sprintf("only %s served, the rest are blocked: %s",
			strings.Join(a.domains, ", "), msg)
	}
	return reason, msg
}

// Evaluate checks every domain the server references (ADR 0003).
func (e *authorizationEvaluator) Evaluate() authorization {
	res := authorization{mode: e.mode}
	fail := func(reason, format string, args ...any) {
		res.failures = append(res.failures, domainFailure{reason, fmt.Sprintf(format, args...)})
	}
	for _, ref := range e.server.Spec.Domains {
		claim := e.claim(ref.Name)
		switch {
		case claim == nil:
			fail(ReasonDomainNotFound, "MailDomain %q not found in namespace %q", ref.Name, e.server.Namespace)
		case e.mode == AuthorizationAllowlist && !e.granted(claim.domain):
			fail(ReasonNotGranted, "no MailDomainGrant covering %q for namespace %q", claim.domain, e.server.Namespace)
		default:
			if n := e.claimsFor(claim.domain); n > 1 {
				fail(ReasonDuplicateDomain,
					"domain %q is claimed by %d MailDomain objects; every claimant is blocked until the duplicate is resolved",
					claim.domain, n)
				continue
			}
			res.domains = append(res.domains, claim.domain)
		}
	}
	slices.Sort(res.domains)
	return res
}

// claim looks up the MailDomain object ref in the server's namespace.
func (e *authorizationEvaluator) claim(name string) *domainClaim {
	for i := range e.claims {
		if e.claims[i].name == name && e.claims[i].namespace == e.server.Namespace {
			return &e.claims[i]
		}
	}
	return nil
}

// claimsFor counts the MailDomains claiming domain that compete for
// it. In allowlist mode only the claims in the namespace that owns
// the domain count: a claim from a namespace without the grant is
// unauthorized anyway and must not block the owner (ADR 0003). In
// unrestricted mode there is no owner, so every namespace counts.
func (e *authorizationEvaluator) claimsFor(domain string) int {
	ownerNS := ""
	if e.mode == AuthorizationAllowlist {
		g, found := e.owner(domain)
		if !found {
			return 0
		}
		ownerNS = g.namespace
	}
	n := 0
	for i := range e.claims {
		if e.claims[i].domain == domain && (ownerNS == "" || e.claims[i].namespace == ownerNS) {
			n++
		}
	}
	return n
}

// granted reports whether a grant covering domain names the server's
// namespace. The most specific matching grant owns the domain: a grant
// for sub.example.org carves it out of a grant for example.org with
// includeSubdomains (DNS-delegation semantics, ADR 0003).
func (e *authorizationEvaluator) granted(domain string) bool {
	owner, found := e.owner(domain)
	return found && owner.namespace == e.server.Namespace
}

// owner returns the most specific grant covering domain.
func (e *authorizationEvaluator) owner(domain string) (grantRef, bool) {
	var best grantRef
	found := false
	for _, g := range e.grants {
		if g.domain != domain &&
			(!g.includeSubdomains || !strings.HasSuffix(domain, "."+g.domain)) {
			continue
		}
		if !found || len(g.domain) > len(best.domain) {
			best, found = g, true
		}
	}
	return best, found
}

// grantRefs converts MailDomainGrants to the evaluation input.
func grantRefs(list *cnmsv1alpha1.MailDomainGrantList) []grantRef {
	refs := make([]grantRef, 0, len(list.Items))
	for _, g := range list.Items {
		refs = append(refs, grantRef{
			domain:            g.Name,
			namespace:         g.Spec.Namespace,
			includeSubdomains: g.Spec.IncludeSubdomains != nil && *g.Spec.IncludeSubdomains,
		})
	}
	return refs
}

// domainClaims converts MailDomains to the evaluation input.
func domainClaims(list *cnmsv1alpha1.MailDomainList) []domainClaim {
	claims := make([]domainClaim, 0, len(list.Items))
	for _, d := range list.Items {
		claims = append(claims, domainClaim{
			name:      d.Name,
			namespace: d.Namespace,
			domain:    d.Spec.Domain,
		})
	}
	return claims
}
