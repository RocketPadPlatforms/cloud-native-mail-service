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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
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
			server:      testServerForAuth("tenant-a", testDomain),
			grants:      []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", false)},
			domains:     []cnmsv1alpha1.MailDomain{domain("tenant-a", testDomain, testDomain)},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{testDomain},
			wantReason:  ReasonAuthorized,
		},
		{
			name:        "subdomain granted via includeSubdomains",
			server:      testServerForAuth("tenant-a", "mail.example.org"),
			grants:      []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", true)},
			domains:     []cnmsv1alpha1.MailDomain{domain("tenant-a", "mail.example.org", "mail.example.org")},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{"mail.example.org"},
			wantReason:  ReasonAuthorized,
		},
		{
			name:       "subdomain without includeSubdomains",
			server:     testServerForAuth("tenant-a", "mail.example.org"),
			grants:     []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", false)},
			domains:    []cnmsv1alpha1.MailDomain{domain("tenant-a", "mail.example.org", "mail.example.org")},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonNotGranted,
			wantNotOK:  true,
		},
		{
			name:   "more specific grant wins",
			server: testServerForAuth("tenant-b", "mail.example.org"),
			grants: []cnmsv1alpha1.MailDomainGrant{
				grant(testDomain, "tenant-a", true),
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
			grants:     []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", true)},
			domains:    []cnmsv1alpha1.MailDomain{domain("tenant-b", testDomain, testDomain)},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonNotGranted,
			wantNotOK:  true,
		},
		{
			name:   "duplicate claim blocks every claimant",
			server: testServerForAuth("tenant-a", testDomain),
			grants: []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", false)},
			domains: []cnmsv1alpha1.MailDomain{
				domain("tenant-a", testDomain, testDomain),
				domain("tenant-b", "example.org-copy", testDomain),
			},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonDuplicateDomain,
			wantNotOK:  true,
		},
		{
			name:   "duplicate claim in one namespace",
			server: testServerForAuth("tenant-a", testDomain),
			grants: []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", false)},
			domains: []cnmsv1alpha1.MailDomain{
				domain("tenant-a", testDomain, testDomain),
				domain("tenant-a", "example.org-b", testDomain),
			},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonDuplicateDomain,
			wantNotOK:  true,
		},
		{
			name:       "referenced MailDomain missing",
			server:     testServerForAuth("tenant-a", testDomain),
			grants:     []cnmsv1alpha1.MailDomainGrant{grant(testDomain, "tenant-a", false)},
			mode:       AuthorizationAllowlist,
			wantReason: ReasonDomainNotFound,
			wantNotOK:  true,
		},
		{
			name:        "unrestricted without grant",
			server:      testServerForAuth("tenant-a", testDomain),
			domains:     []cnmsv1alpha1.MailDomain{domain("tenant-a", testDomain, testDomain)},
			mode:        AuthorizationUnrestricted,
			wantDomains: []string{testDomain},
			wantReason:  ReasonAuthorizationDisabled,
		},
		{
			name:   "unrestricted still enforces uniqueness",
			server: testServerForAuth("tenant-a", testDomain),
			domains: []cnmsv1alpha1.MailDomain{
				domain("tenant-a", testDomain, testDomain),
				domain("tenant-b", "example.org-copy", testDomain),
			},
			mode:       AuthorizationUnrestricted,
			wantReason: ReasonDuplicateDomain,
			wantNotOK:  true,
		},
		{
			name:   "multiple domains sorted with first as primary",
			server: testServerForAuth("tenant-a", "b.example.org", "a.example.org"),
			grants: []cnmsv1alpha1.MailDomainGrant{
				grant("a.example.org", "tenant-a", false),
				grant("b.example.org", "tenant-a", false),
			},
			domains: []cnmsv1alpha1.MailDomain{
				domain("tenant-a", "b.example.org", "b.example.org"),
				domain("tenant-a", "a.example.org", "a.example.org"),
			},
			mode:        AuthorizationAllowlist,
			wantDomains: []string{"a.example.org", "b.example.org"},
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
			got, reason, _ := e.Evaluate()
			if tc.wantNotOK {
				if got != nil {
					t.Fatalf("Evaluate() = %v, want nil", got)
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
