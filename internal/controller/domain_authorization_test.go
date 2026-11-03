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
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// Fixture names of the evaluator tests.
const (
	domA    = "a.example.org"
	domB    = "b.example.org"
	domC    = "c.example.org"
	tenantA = "tenant-a"
)

func testServerForAuth(ns string, refs ...string) *cnmsv1alpha1.MailServer {
	server := &cnmsv1alpha1.MailServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: ns},
	}
	for _, name := range refs {
		server.Spec.Domains = append(server.Spec.Domains, cnmsv1alpha1.NameReference{Name: name})
	}
	return server
}

func TestAuthorizationEvaluate(t *testing.T) {
	grant := func(domain, ns string, sub bool) cnmsv1alpha1.MailDomainGrant {
		g := cnmsv1alpha1.MailDomainGrant{
			ObjectMeta: metav1.ObjectMeta{Name: domain},
			Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: ns},
		}
		if sub {
			g.Spec.IncludeSubdomains = new(true)
		}
		return g
	}
	domain := func(ns, name, d string) cnmsv1alpha1.MailDomain {
		return cnmsv1alpha1.MailDomain{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       cnmsv1alpha1.MailDomainSpec{Domain: d},
		}
	}

	tests := []struct {
		name        string
		server      *cnmsv1alpha1.MailServer
		grants      []cnmsv1alpha1.MailDomainGrant
		domains     []cnmsv1alpha1.MailDomain
		mode        domainAuthorizationMode
		wantDomains []string
		wantReason  string
		wantNotOK   bool
	}{
		{
			name:        "granted apex",
			server:      testServerForAuth(tenantA, testDomain),
			grants:      []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, false)},
			domains:     []cnmsv1alpha1.MailDomain{domain(tenantA, testDomain, testDomain)},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{testDomain},
			wantReason:  ReasonAuthorized,
		},
		{
			name:        "subdomain granted via includeSubdomains",
			server:      testServerForAuth(tenantA, "mail.example.org"),
			grants:      []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, true)},
			domains:     []cnmsv1alpha1.MailDomain{domain(tenantA, "mail.example.org", "mail.example.org")},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{"mail.example.org"},
			wantReason:  ReasonAuthorized,
		},
		{
			name:       "subdomain without includeSubdomains",
			server:     testServerForAuth(tenantA, "mail.example.org"),
			grants:     []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, false)},
			domains:    []cnmsv1alpha1.MailDomain{domain(tenantA, "mail.example.org", "mail.example.org")},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonNotGranted,
			wantNotOK:  true,
		},
		{
			name:   "more specific grant wins",
			server: testServerForAuth("tenant-b", "mail.example.org"),
			grants: []cnmsv1alpha1.MailDomainGrant{
				grant(testDomain, tenantA, true),
				grant("mail.example.org", "tenant-b", false),
			},
			domains:     []cnmsv1alpha1.MailDomain{domain("tenant-b", "mail.example.org", "mail.example.org")},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{"mail.example.org"},
			wantReason:  ReasonAuthorized,
		},
		{
			name:       "broad grant does not cover sibling namespace",
			server:     testServerForAuth("tenant-b", testDomain),
			grants:     []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, true)},
			domains:    []cnmsv1alpha1.MailDomain{domain("tenant-b", testDomain, testDomain)},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonNotGranted,
			wantNotOK:  true,
		},
		{
			name:   "foreign claim without grant does not block the owner",
			server: testServerForAuth(tenantA, testDomain),
			grants: []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, false)},
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, testDomain, testDomain),
				domain("tenant-b", "example.org-copy", testDomain),
			},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{testDomain},
			wantReason:  ReasonAuthorized,
		},
		{
			name:   "foreign claimant is not granted, not a duplicate",
			server: testServerForAuth("tenant-b", "example.org-copy"),
			grants: []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, false)},
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, testDomain, testDomain),
				domain("tenant-b", "example.org-copy", testDomain),
			},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonNotGranted,
			wantNotOK:  true,
		},
		{
			name:   "foreign claims of a domain without a grant stay unauthorized",
			server: testServerForAuth("tenant-b", "example.org-copy"),
			domains: []cnmsv1alpha1.MailDomain{
				domain("tenant-b", "example.org-copy", testDomain),
				domain("tenant-c", "example.org-copy", testDomain),
			},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonNotGranted,
			wantNotOK:  true,
		},
		{
			name:   "duplicate claim in one namespace",
			server: testServerForAuth(tenantA, testDomain),
			grants: []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, false)},
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, testDomain, testDomain),
				domain(tenantA, "example.org-b", testDomain),
			},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonDuplicateDomain,
			wantNotOK:  true,
		},
		{
			name:       "referenced MailDomain missing",
			server:     testServerForAuth(tenantA, testDomain),
			grants:     []cnmsv1alpha1.MailDomainGrant{grant(testDomain, tenantA, false)},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonDomainNotFound,
			wantNotOK:  true,
		},
		{
			name:        "unrestricted without grant",
			server:      testServerForAuth(tenantA, testDomain),
			domains:     []cnmsv1alpha1.MailDomain{domain(tenantA, testDomain, testDomain)},
			mode:        AuthorizationUnrestricted,
			wantDomains: []string{testDomain},
			wantReason:  ReasonAuthorizationDisabled,
		},
		{
			name:   "unrestricted still enforces uniqueness",
			server: testServerForAuth(tenantA, testDomain),
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, testDomain, testDomain),
				domain("tenant-b", "example.org-copy", testDomain),
			},
			mode:       AuthorizationUnrestricted,
			wantReason: ReasonDuplicateDomain,
			wantNotOK:  true,
		},
		{
			// One revoked domain must not take the granted one down.
			name:   "partial authorization keeps the granted subset",
			server: testServerForAuth(tenantA, domA, domB, domC),
			grants: []cnmsv1alpha1.MailDomainGrant{
				grant(domA, tenantA, false),
				grant(domC, tenantA, false),
			},
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, domA, domA),
				domain(tenantA, domB, domB),
				domain(tenantA, domC, domC),
			},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{domA, domC},
			wantReason:  ReasonPartiallyAuthorized,
		},
		{
			name:   "partial authorization with a missing MailDomain",
			server: testServerForAuth(tenantA, domA, "gone"),
			grants: []cnmsv1alpha1.MailDomainGrant{grant(domA, tenantA, false)},
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, domA, domA),
			},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{domA},
			wantReason:  ReasonPartiallyAuthorized,
		},
		{
			name:   "multiple domains sorted with first as primary",
			server: testServerForAuth(tenantA, domB, domA),
			grants: []cnmsv1alpha1.MailDomainGrant{
				grant(domA, tenantA, false),
				grant(domB, tenantA, false),
			},
			domains: []cnmsv1alpha1.MailDomain{
				domain(tenantA, domB, domB),
				domain(tenantA, domA, domA),
			},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{domA, domB},
			wantReason:  ReasonAuthorized,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &authorizationEvaluator{
				server: tc.server,
				grants: grantRefs(&cnmsv1alpha1.MailDomainGrantList{Items: tc.grants}),
				claims: domainClaims(&cnmsv1alpha1.MailDomainList{Items: tc.domains}),
				mode:   tc.mode,
			}
			res := e.Evaluate()
			got := res.domains
			reason, _ := res.condition()
			if tc.wantNotOK {
				if len(got) != 0 {
					t.Fatalf("Evaluate() = %v, want none", got)
				}
				if reason != tc.wantReason {
					t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.wantDomains) {
				t.Fatalf("Evaluate() = %v, want %v", got, tc.wantDomains)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestAuthorizationConditionListsEveryFailure(t *testing.T) {
	e := &authorizationEvaluator{
		server: testServerForAuth(tenantA, domA, domB, "gone"),
		grants: grantRefs(&cnmsv1alpha1.MailDomainGrantList{Items: []cnmsv1alpha1.MailDomainGrant{{
			ObjectMeta: metav1.ObjectMeta{Name: domA},
			Spec:       cnmsv1alpha1.MailDomainGrantSpec{Namespace: tenantA},
		}}}),
		claims: domainClaims(&cnmsv1alpha1.MailDomainList{Items: []cnmsv1alpha1.MailDomain{
			{ObjectMeta: metav1.ObjectMeta{Name: domA, Namespace: tenantA},
				Spec: cnmsv1alpha1.MailDomainSpec{Domain: domA}},
			{ObjectMeta: metav1.ObjectMeta{Name: domB, Namespace: tenantA},
				Spec: cnmsv1alpha1.MailDomainSpec{Domain: domB}},
		}}),
		mode: AuthorizationAllowlist,
	}
	reason, msg := e.Evaluate().condition()
	if reason != ReasonPartiallyAuthorized {
		t.Fatalf("reason = %q, want %q", reason, ReasonPartiallyAuthorized)
	}
	for _, want := range []string{"only a.example.org authorized", "once the new configuration is rolled out", `"b.example.org"`, `"gone"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q misses %q", msg, want)
		}
	}
}
