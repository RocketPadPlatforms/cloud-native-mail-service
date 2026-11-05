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

const (
	tenantB      = "tenant-b"
	hostMail     = "mail.example.org"
	hostOther    = "mx.example.org"
	hostApexOnly = "example.org"
)

func TestHostnameAuthorization(t *testing.T) {
	grant := func(name, ns string, sub bool) grantRef {
		return grantRef{domain: name, namespace: ns, includeSubdomains: sub}
	}
	server := func(ns, name, host string) *cnmsv1alpha1.MailServer {
		return &cnmsv1alpha1.MailServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       cnmsv1alpha1.MailServerSpec{Hostname: host},
		}
	}
	claim := func(s *cnmsv1alpha1.MailServer) serverClaim {
		return serverClaim{name: s.Name, namespace: s.Namespace, hostname: s.Spec.Hostname}
	}
	self := server(tenantA, authServerName, hostMail)

	for _, tc := range []struct {
		name       string
		server     *cnmsv1alpha1.MailServer
		grants     []grantRef
		others     []*cnmsv1alpha1.MailServer
		mode       domainAuthorizationMode
		wantHost   string
		wantReason string // of a failure; empty means authorized
		// wantMessage is a part of the message of the failure.
		wantMessage string
	}{
		{
			name: "granted exactly", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostMail, tenantA, false)}, wantHost: hostMail,
		},
		{
			name: "granted through the apex with subdomains", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostApexOnly, tenantA, true)}, wantHost: hostMail,
		},
		{
			name: "the apex grant without subdomains does not cover it", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostApexOnly, tenantA, false)}, wantReason: ReasonHostnameNotAuthorized,
		},
		{
			name: "no grant", server: self, mode: AuthorizationAllowlist, wantReason: ReasonHostnameNotAuthorized,
		},
		{
			name: "granted to another namespace", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostMail, tenantB, false)}, wantReason: ReasonHostnameNotAuthorized,
		},
		{
			name: "a carve-out for the hostname takes it from the apex owner", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostApexOnly, tenantA, true), grant(hostMail, tenantB, false)}, wantReason: ReasonHostnameNotAuthorized,
		},
		{
			name: "a public suffix is never a hostname, even unrestricted", server: server(tenantA, authServerName, "co.uk"), mode: AuthorizationUnrestricted,
			wantReason: ReasonHostnameNotAuthorized,
		},
		{
			name: "a hostname that is not set", server: server(tenantA, authServerName, ""), mode: AuthorizationUnrestricted,
			wantReason: ReasonHostnameNotAuthorized, wantMessage: "spec.hostname is not set",
		},
		{
			name: "a cluster-internal name is never a hostname, even unrestricted", server: server(tenantA, authServerName, "mail-postfix.tenant-a.svc.cluster.local"),
			mode: AuthorizationUnrestricted, wantReason: ReasonHostnameNotAuthorized, wantMessage: "cluster-internal",
		},
		{
			name: "bare cluster.local is never a hostname, even unrestricted", server: server(tenantA, authServerName, "cluster.local"),
			mode: AuthorizationUnrestricted, wantReason: ReasonHostnameNotAuthorized, wantMessage: "cluster-internal",
		},
		{
			name: "unrestricted needs no grant", server: self, mode: AuthorizationUnrestricted, wantHost: hostMail,
		},
		{
			name: "two servers of the owner namespace use it", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostMail, tenantA, false)},
			others: []*cnmsv1alpha1.MailServer{server(tenantA, "second", hostMail)}, wantReason: ReasonDuplicateHostname,
		},
		{
			name: "a server of another namespace without the grant does not block the owner", server: self, mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostMail, tenantA, false)},
			others: []*cnmsv1alpha1.MailServer{server(tenantB, "second", hostMail)}, wantHost: hostMail,
		},
		{
			name: "the server without the grant is not authorized, not a duplicate", server: server(tenantB, "second", hostMail), mode: AuthorizationAllowlist,
			grants: []grantRef{grant(hostMail, tenantA, false)},
			others: []*cnmsv1alpha1.MailServer{self}, wantReason: ReasonHostnameNotAuthorized,
		},
		{
			name: "unrestricted counts every namespace", server: self, mode: AuthorizationUnrestricted,
			others: []*cnmsv1alpha1.MailServer{server(tenantB, "second", hostMail)}, wantReason: ReasonDuplicateHostname,
		},
		{
			name: "another hostname is no duplicate", server: self, mode: AuthorizationUnrestricted,
			others: []*cnmsv1alpha1.MailServer{server(tenantB, "second", hostOther)}, wantHost: hostMail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			servers := make([]serverClaim, 0, 1+len(tc.others))
			servers = append(servers, claim(tc.server))
			for _, o := range tc.others {
				servers = append(servers, claim(o))
			}
			res := (&authorizationEvaluator{server: tc.server, grants: tc.grants, servers: dedupe(servers), mode: tc.mode}).Evaluate()
			if res.hostname != tc.wantHost {
				t.Fatalf("authorized hostname %q, want %q", res.hostname, tc.wantHost)
			}
			status, reason, message := res.hostnameCondition()
			if tc.wantReason == "" {
				if res.hostnameFailure != nil || status != metav1.ConditionTrue {
					t.Fatalf("got %s/%s: %s", status, reason, message)
				}
				return
			}
			if res.hostnameFailure == nil || status != metav1.ConditionFalse || reason != tc.wantReason || message == "" {
				t.Fatalf("got %s/%s: %s; want False/%s", status, reason, message, tc.wantReason)
			}
			if !strings.Contains(message, tc.wantMessage) {
				t.Fatalf("message %q does not contain %q", message, tc.wantMessage)
			}
		})
	}

	t.Run("the verdict on the hostname leaves the domains alone", func(t *testing.T) {
		ev := &authorizationEvaluator{
			server:  self,
			grants:  []grantRef{grant(testDomain, tenantA, false)},
			claims:  []domainClaim{{name: "d", namespace: tenantA, domain: testDomain, serverRef: authServerName}},
			servers: []serverClaim{claim(self)},
			mode:    AuthorizationAllowlist,
		}
		res := ev.Evaluate()
		if res.hostnameFailure == nil || !reflect.DeepEqual(res.domains, []string{testDomain}) || !res.all() {
			t.Fatalf("hostname failure %+v, domains %v", res.hostnameFailure, res.domains)
		}
	})
}

// dedupe drops repeated (namespace, name) entries.
func dedupe(in []serverClaim) []serverClaim {
	seen := map[[2]string]bool{}
	var out []serverClaim
	for _, c := range in {
		k := [2]string{c.namespace, c.name}
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}
