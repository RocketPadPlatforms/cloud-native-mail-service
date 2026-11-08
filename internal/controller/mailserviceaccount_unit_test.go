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
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

// Names of the unit tests of the accounts.
const (
	nsLimited   = "limited"
	unitDomainA = "a.example"
	unitDomainB = "b.example"
	unitApp     = "app"
	reqOne      = "a/one"
	reqTwo      = "b/two"
)

func accountIn(ns, name, serverNS, server, sender string) *cnmsv1alpha1.MailServiceAccount {
	return &cnmsv1alpha1.MailServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cnmsv1alpha1.MailServiceAccountSpec{
			ServerRef: cnmsv1alpha1.NamespacedNameReference{Name: server, Namespace: serverNS},
			Sender:    sender,
		},
	}
}

func accountReconciler(t *testing.T, objs ...*cnmsv1alpha1.MailServiceAccount) *MailServiceAccountReconciler {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailServiceAccount{}, mailServiceAccountServerIndex, indexAccountByServer).
		WithIndex(&cnmsv1alpha1.MailServiceAccount{}, mailServiceAccountSenderIndex, indexAccountBySender).
		WithIndex(&cnmsv1alpha1.MailServer{}, mailServerHostnameIndex, indexMailServerByHostname).
		WithIndex(&cnmsv1alpha1.MailDomain{}, mailDomainDomainIndex, indexMailDomainByDomain)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &MailServiceAccountReconciler{Client: b.Build()}
}

// The login is the reserved form of ADR 0015, section 4.9: "sa." and 16 hex
// characters of the SHA-256 of "<namespace>/<name>" (the digest of "a/app" is
// fac8ca698542c087...).
func TestServiceAccountLogin(t *testing.T) {
	if got, want := serviceAccountLogin("a", unitApp), "sa.fac8ca698542c087"; got != want {
		t.Errorf("login = %q, want %q", got, want)
	}
	if serviceAccountLogin("a", unitApp) == serviceAccountLogin("b", unitApp) || serviceAccountLogin("a", unitApp) == serviceAccountLogin("a", "pp") {
		t.Error("the login does not tell accounts apart")
	}
	// "ab"/"c" and "a"/"bc" must not run together.
	if serviceAccountLogin("ab", "c") == serviceAccountLogin("a", "bc") {
		t.Error("the separator is missing")
	}
}

func TestAccountConsent(t *testing.T) {
	server := serverIn("srv", testServerName)
	if ok, domains := accountConsent(server, "srv"); !ok || domains != nil {
		t.Errorf("own namespace = %v, %v; want allowed without a limit", ok, domains)
	}
	if ok, _ := accountConsent(server, unitApp); ok {
		t.Error("a namespace that is not listed is allowed")
	}
	server.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{
		{Name: unitApp}, {Name: nsLimited, SenderDomains: []string{unitDomainA}}, {Name: "srv", SenderDomains: []string{unitDomainB}},
	}}
	if ok, domains := accountConsent(server, unitApp); !ok || domains != nil {
		t.Errorf("listed = %v, %v", ok, domains)
	}
	if ok, domains := accountConsent(server, nsLimited); !ok || !slices.Equal(domains, []string{unitDomainA}) {
		t.Errorf("limited = %v, %v", ok, domains)
	}
	if ok, domains := accountConsent(server, "srv"); !ok || !slices.Equal(domains, []string{unitDomainB}) {
		t.Errorf("an entry for the namespace of the server limits it too: %v, %v", ok, domains)
	}
	if !senderDomainAllowed(nil, "any.example") || !senderDomainAllowed([]string{unitDomainA}, unitDomainA) || senderDomainAllowed([]string{unitDomainA}, unitDomainB) {
		t.Error("senderDomainAllowed")
	}
}

// Only an account that the server consents to, for a domain the entry allows,
// claims a sender: a namespace that is not consented to cannot take an
// address away from one that is (ADR 0015, section 4.2, point 2).
func TestServiceSenders(t *testing.T) {
	server := serverIn("srv", testServerName)
	server.Spec.ServiceAccounts = &cnmsv1alpha1.ServerServiceAccounts{AllowedNamespaces: []cnmsv1alpha1.AllowedNamespace{
		{Name: unitApp}, {Name: nsLimited, SenderDomains: []string{unitDomainA}},
	}}
	accounts := []cnmsv1alpha1.MailServiceAccount{
		*accountIn("srv", "own", "", testServerName, "own@a.example"), // the namespace of the server: no namespace in serverRef
		*accountIn(unitApp, "ok", "srv", testServerName, "ok@a.example"),
		*accountIn(unitApp, "b", "srv", testServerName, "Up@B.example"),
		*accountIn(nsLimited, "in", "srv", testServerName, "in@a.example"),
		*accountIn(nsLimited, "out", "srv", testServerName, "out@b.example"), // domain not allowed
		*accountIn("stranger", "x", "srv", testServerName, "x@a.example"),    // not consented to
		*accountIn(unitApp, "elsewhere", "srv", otherServer, "e@a.example"),  // another server
	}
	senders := serviceSenders(server, accounts)
	got := make([]string, 0, len(senders))
	for _, s := range senders {
		got = append(got, s.owner+" "+s.local+"@"+s.domain)
	}
	want := []string{"app/b up@b.example", "app/ok ok@a.example", nsLimited + "/in in@a.example", "srv/own own@a.example"}
	if !slices.Equal(got, want) {
		t.Errorf("senders = %v, want %v", got, want)
	}
}

// A sender names its domain by DNS name; it is resolved to the MailDomain of
// the server's namespace, so it shares the verdict of a MailUser that names the
// same object, and a domain no object carries is not found.
func TestServerAddressInputWithSenders(t *testing.T) {
	domains := map[string]cnmsv1alpha1.MailDomain{
		objA: *domainClaimIn("srv", objA, unitDomainA, testServerName),
		objB: *domainClaimIn("srv", objB, unitDomainB, otherServer),
		objX: *domainClaimIn("srv", objX, "x.example", testServerName),
	}
	claims := &cnmsv1alpha1.MailDomainList{Items: []cnmsv1alpha1.MailDomain{domains[objA], domains[objB], domains[objX]}}
	ev := &authorizationEvaluator{claims: domainClaims(claims), mode: AuthorizationUnrestricted}
	users := []cnmsv1alpha1.MailUser{*userIn("srv", apiUserName, testServerName, objA)}
	senders := []serviceSender{
		{owner: "app/a", local: apiUserName, domain: unitDomainA}, // the address of the user
		{owner: "app/b", local: unitApp, domain: unitDomainB},     // served by another server
		{owner: "app/c", local: unitApp, domain: "c.example"},     // no MailDomain
		{owner: "app/x", local: unitApp, domain: "x.example"},
	}
	in, failures := serverAddressInput(testServerName, users, senders, domains, ev)
	if len(in.Senders) != 4 || in.Senders[0].Address.Domain != objA || in.Senders[1].Address.Domain != objB ||
		in.Senders[2].Address.Domain != senderDomainKeyPrefix+"c.example" || in.Senders[3].Address.Domain != objX {
		t.Fatalf("senders = %+v", in.Senders)
	}
	if failures[senderDomainKeyPrefix+"c.example"] == "" || failures[objB] == "" {
		t.Errorf("failures = %v, want the unserved and the unknown domain", failures)
	}
	res := addresses.Decide(in)
	got := map[string]string{}
	for _, s := range res.Senders {
		got[s.Owner] = s.Row.Reason
	}
	want := map[string]string{
		"app/a": addresses.ReasonSenderConflict, // rank 1 wins
		"app/b": addresses.ReasonDomainNotServed,
		"app/c": addresses.ReasonDomainNotFound,
		"app/x": addresses.ReasonServed,
	}
	for owner, reason := range want {
		if got[owner] != reason {
			t.Errorf("%s = %q, want %q", owner, got[owner], reason)
		}
	}
}

// The watches reach the accounts of one server through the indexes.
func TestAccountMaps(t *testing.T) {
	r := accountReconciler(t,
		accountIn("a", "one", "srv", testServerName, "x@a.example"),
		accountIn("b", "two", "srv", testServerName, "x@a.example"), // the same sender
		accountIn("b", "three", "srv", testServerName, "y@a.example"),
		accountIn("srv", "own", "", testServerName, "z@a.example"),
		accountIn("a", "far", "srv", otherServer, "x@a.example"), // another server
		accountIn("a", "away", "other-ns", testServerName, "x@a.example"),
	)
	ctx := context.Background()

	got := requestKeys(r.mapClaimants(ctx, accountIn("a", "one", "srv", testServerName, "X@a.example")))
	if want := []string{reqTwo}; !slices.Equal(got, want) {
		t.Errorf("mapClaimants = %v, want %v", got, want)
	}
	// A deleted or changed account still reaches those it was in conflict with.
	got = requestKeys(r.mapClaimants(ctx, accountIn("a", "gone", "srv", testServerName, "x@a.example")))
	if want := []string{reqOne, reqTwo}; !slices.Equal(got, want) {
		t.Errorf("mapClaimants(deleted) = %v, want %v", got, want)
	}

	want := []string{reqOne, "b/three", reqTwo, "srv/own"}
	if got := requestKeys(r.mapServerAccounts(ctx, serverIn("srv", testServerName))); !slices.Equal(got, want) {
		t.Errorf("mapServerAccounts = %v, want %v", got, want)
	}
	if got := requestKeys(r.mapDomainAccounts(ctx, domainClaimIn("srv", objA, unitDomainA, testServerName))); !slices.Equal(got, want) {
		t.Errorf("mapDomainAccounts = %v, want %v", got, want)
	}
	if got := requestKeys(r.mapUserAccounts(ctx, userIn("srv", apiUserName, testServerName, objA))); !slices.Equal(got, want) {
		t.Errorf("mapUserAccounts = %v, want %v", got, want)
	}
	if got := requestKeys(r.mapGrantAccounts(ctx, nil)); len(got) != 6 {
		t.Errorf("mapGrantAccounts = %v, want every account", got)
	}
}

// A MailDomain of another namespace for the same domain decides the uniqueness
// of the domain: it reaches the accounts of the server that the other claimant
// names, and a server of the same hostname reaches the accounts of its peer.
func TestAccountMapsReachThePeers(t *testing.T) {
	r := &MailServiceAccountReconciler{Client: fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailServiceAccount{}, mailServiceAccountServerIndex, indexAccountByServer).
		WithIndex(&cnmsv1alpha1.MailServer{}, mailServerHostnameIndex, indexMailServerByHostname).
		WithIndex(&cnmsv1alpha1.MailDomain{}, mailDomainDomainIndex, indexMailDomainByDomain).
		WithObjects(
			accountIn("a", "one", "a", testServerName, "x@shared.example"),
			accountIn("b", "two", "b", testServerName, "x@shared.example"),
			domainClaimIn("a", objA, "shared.example", testServerName),
			domainClaimIn("b", objB, "shared.example", testServerName),
			serverWithHostname("a", testServerName, "mail.shared.example"),
			serverWithHostname("b", testServerName, "mail.shared.example"),
		).Build()}
	ctx := context.Background()
	want := []string{reqOne, reqTwo}
	if got := requestKeys(r.mapDomainAccounts(ctx, domainClaimIn("a", objA, "shared.example", testServerName))); !slices.Equal(got, want) {
		t.Errorf("mapDomainAccounts = %v, want %v", got, want)
	}
	if got := requestKeys(r.mapServerAccounts(ctx, serverWithHostname("a", testServerName, "mail.shared.example"))); !slices.Equal(got, want) {
		t.Errorf("mapServerAccounts = %v, want %v", got, want)
	}
}

func TestAccountIndexes(t *testing.T) {
	a := accountIn(unitApp, "one", "srv", testServerName, "X@A.example")
	if got := indexAccountByServer(a); !slices.Equal(got, []string{"srv/" + testServerName}) {
		t.Errorf("server key = %v", got)
	}
	a.Spec.ServerRef.Namespace = ""
	if got := indexAccountByServer(a); !slices.Equal(got, []string{"app/" + testServerName}) {
		t.Errorf("the namespace of the account stands in: %v", got)
	}
	if got := indexAccountBySender(a); !slices.Equal(got, []string{"app/" + testServerName + "/x@a.example"}) {
		t.Errorf("sender key = %v", got)
	}
	if indexAccountByServer(&cnmsv1alpha1.MailUser{}) != nil || indexAccountBySender(&cnmsv1alpha1.MailServiceAccount{}) != nil {
		t.Error("an object of another kind or an empty account has a key")
	}
}
