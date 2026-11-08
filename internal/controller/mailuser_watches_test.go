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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

func userIn(ns, name, server, domain string, aliases ...cnmsv1alpha1.MailUserAlias) *cnmsv1alpha1.MailUser {
	return &cnmsv1alpha1.MailUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: cnmsv1alpha1.MailUserSpec{
			ServerRef: cnmsv1alpha1.NameReference{Name: server},
			DomainRef: cnmsv1alpha1.NameReference{Name: domain},
			Aliases:   aliases,
		},
	}
}

func aliasOn(local, domain string) cnmsv1alpha1.MailUserAlias {
	return cnmsv1alpha1.MailUserAlias{LocalPart: local, DomainRef: cnmsv1alpha1.NameReference{Name: domain}}
}

func userReconciler(t *testing.T, objs ...*cnmsv1alpha1.MailUser) *MailUserReconciler {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailUser{}, mailUserServerIndex, indexMailUserByServer).
		WithIndex(&cnmsv1alpha1.MailUser{}, mailUserDomainIndex, indexMailUserByDomain).
		WithIndex(&cnmsv1alpha1.MailUser{}, mailUserAddressIndex, indexMailUserByAddress).
		WithIndex(&cnmsv1alpha1.MailDomain{}, mailDomainDomainIndex, indexMailDomainByDomain)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &MailUserReconciler{Client: b.Build()}
}

// A change of an account reaches the accounts that claim an address of it,
// on the same server, and no others.
func TestMapClaimantsReachesOnlyTheClaimants(t *testing.T) {
	r := userReconciler(t,
		userIn("a", apiUserName, testServerName, objA, aliasOn("team", objB)),
		userIn("a", "bob", testServerName, objA, aliasOn("team", objB)),
		userIn("a", "carol", testServerName, objA, aliasOn(apiUserName, objA)),
		userIn("a", "dave", testServerName, objA, aliasOn(otherServer, objB)),
		userIn("a", "erin", "second", objA, aliasOn("team", objB)),        // another server
		userIn("b", "frank", testServerName, objA, aliasOn("team", objB)), // another namespace
	)
	got := requestKeys(r.mapClaimants(context.Background(), userIn("a", apiUserName, testServerName, objA, aliasOn("team", objB))))
	slices.Sort(got)
	if want := []string{reqBob, "a/carol"}; !slices.Equal(got, want) {
		t.Errorf("mapClaimants = %v, want %v", got, want)
	}
	// A deleted account (the last object) still reaches those it was in
	// conflict with.
	got = requestKeys(r.mapClaimants(context.Background(), userIn("a", "gone", testServerName, objA, aliasOn("team", objB))))
	slices.Sort(got)
	if want := []string{reqAlice, reqBob}; !slices.Equal(got, want) {
		t.Errorf("mapClaimants(deleted) = %v, want %v", got, want)
	}
}

func TestMapServerAndDomainUsers(t *testing.T) {
	r := userReconciler(t,
		userIn("a", apiUserName, testServerName, objA, aliasOn("team", objB)),
		userIn("a", "bob", testServerName, objB),
		userIn("a", "carol", "second", objA),
		userIn("b", "dave", testServerName, objA),
	)
	ctx := context.Background()
	got := requestKeys(r.mapServerUsers(ctx, serverIn("a", testServerName)))
	if want := []string{reqAlice, reqBob}; !slices.Equal(got, want) {
		t.Errorf("mapServerUsers = %v, want %v", got, want)
	}
	got = requestKeys(r.mapDomainUsers(ctx, domainClaimIn("a", objB, "b.example", testServerName)))
	if want := []string{reqAlice, reqBob}; !slices.Equal(got, want) {
		t.Errorf("mapDomainUsers = %v, want %v", got, want)
	}
	if got := requestKeys(r.mapGrantUsers(ctx, nil)); len(got) != 4 {
		t.Errorf("mapGrantUsers = %v, want every account", got)
	}
}

// A MailDomain of another namespace for the same domain decides the
// uniqueness of the domain: it reaches the accounts of the namespace that
// names the other claimant.
func TestMapDomainUsersReachesTheOtherClaimantsOfTheDomain(t *testing.T) {
	r := &MailUserReconciler{Client: fake.NewClientBuilder().WithScheme(watchTestScheme(t)).
		WithIndex(&cnmsv1alpha1.MailUser{}, mailUserDomainIndex, indexMailUserByDomain).
		WithIndex(&cnmsv1alpha1.MailDomain{}, mailDomainDomainIndex, indexMailDomainByDomain).
		WithObjects(
			userIn("a", apiUserName, testServerName, objA),
			userIn("b", "bob", testServerName, objX),
			domainClaimIn("a", objA, "shared.example", testServerName),
			domainClaimIn("b", objX, "shared.example", testServerName),
		).Build()}
	got := requestKeys(r.mapDomainUsers(context.Background(), domainClaimIn("a", objA, "shared.example", testServerName)))
	slices.Sort(got)
	if want := []string{reqAlice, "b/bob"}; !slices.Equal(got, want) {
		t.Errorf("mapDomainUsers = %v, want %v", got, want)
	}
}

const (
	reqAlice = "a/alice"
	reqBob   = "a/bob"
)

// The input of the decision is built in one place, for the status of an
// account and for the tables of a server alike: only the accounts of the
// server, and the state of every domain they name.
func TestServerAddressInput(t *testing.T) {
	domain := func(name, domainName, server string) cnmsv1alpha1.MailDomain {
		return *domainClaimIn("a", name, domainName, server)
	}
	domains := map[string]cnmsv1alpha1.MailDomain{
		objA: domain(objA, "a.example", testServerName),
		objB: domain(objB, "b.example", testServerName),
		objX: domain(objX, "x.example", otherServer),
	}
	// A claim of the domain by a second object blocks both (ADR 0003).
	claims := &cnmsv1alpha1.MailDomainList{Items: []cnmsv1alpha1.MailDomain{
		domains[objA], domains[objB], domains[objX],
		*domainClaimIn("z", "dup", "b.example", testServerName),
	}}
	ev := &authorizationEvaluator{claims: domainClaims(claims), mode: AuthorizationUnrestricted}

	disabled := userIn("a", "bob", testServerName, objA)
	disabled.Spec.Disabled = new(true)
	users := []cnmsv1alpha1.MailUser{
		*userIn("a", apiUserName, testServerName, objA, aliasOn(userSales, objB), aliasOn("far", objX), aliasOn("gone", "dom-none")),
		*disabled,
		*userIn("a", "carol", otherServer, objA), // another server: left out
	}
	in, failures := serverAddressInput(testServerName, users, nil, domains, ev)

	if len(in.Users) != 2 || in.Users[0].Name != apiUserName || in.Users[1].Name != userBob {
		t.Fatalf("users = %+v, want alice and bob", in.Users)
	}
	if !in.Users[1].Disabled || in.Users[0].Disabled {
		t.Error("the disabled flag is not carried")
	}
	if got := in.Users[0].Primary; got.Local != apiUserName || got.Domain != objA {
		t.Errorf("primary = %+v", got)
	}
	if len(in.Users[0].Aliases) != 3 {
		t.Errorf("aliases = %+v", in.Users[0].Aliases)
	}
	for ref, want := range map[string]addresses.DomainState{
		objA:       addresses.DomainOK,
		objB:       addresses.DomainNotAuthorized, // claimed twice
		objX:       addresses.DomainNotServed,
		"dom-none": addresses.DomainNotFound,
	} {
		if got := in.Domains[ref].State; got != want {
			t.Errorf("domain %s = %v, want %v", ref, got, want)
		}
	}
	if in.Domains[objA].Name != "a.example" {
		t.Errorf("the DNS name is not carried: %+v", in.Domains[objA])
	}
	for _, ref := range []string{objB, objX, "dom-none"} {
		if failures[ref] == "" {
			t.Errorf("no message for the failing domain %s", ref)
		}
	}
	if len(failures) != 3 {
		t.Errorf("failures = %v", failures)
	}
	// The verdict of this input is the one the status shows.
	res := addresses.Decide(in)
	if len(res.Users) != 2 || res.Users[0].Name != apiUserName {
		t.Errorf("Decide = %+v", res)
	}
	// The arguments are only read.
	if users[0].Spec.Aliases[0].LocalPart != userSales || len(users) != 3 {
		t.Error("the input changed")
	}
}

func TestNamespaceDomainsKeepsOneNamespace(t *testing.T) {
	list := &cnmsv1alpha1.MailDomainList{Items: []cnmsv1alpha1.MailDomain{
		*domainClaimIn("a", "one", "a.example", testServerName), *domainClaimIn("b", "two", "b.example", testServerName),
	}}
	got := namespaceDomains(list, "a")
	if _, ok := got["one"]; !ok || len(got) != 1 {
		t.Errorf("namespaceDomains = %v", got)
	}
}

// Names of the MailDomain objects in the tests of the watches and the input.
const (
	objA = "dom-a"
	objB = "dom-b"
	objX = "dom-x"
)

// The MailServer is woken by its accounts: the one a MailUser names, and for
// an update the old one as well.
func TestMapUserServerNamesTheServerOfTheAccount(t *testing.T) {
	r := &MailServerReconciler{}
	got := requestKeys(r.mapUserServer(context.Background(), userIn("a", apiUserName, testServerName, objA)))
	if want := []string{"a/" + testServerName}; !slices.Equal(got, want) {
		t.Errorf("mapUserServer = %v, want %v", got, want)
	}
	if got := r.mapUserServer(context.Background(), userIn("a", apiUserName, "", objA)); len(got) != 0 {
		t.Errorf("mapUserServer of an account without server = %v, want none", got)
	}
	if got := r.mapUserServer(context.Background(), serverIn("a", "s")); len(got) != 0 {
		t.Errorf("mapUserServer of another kind = %v, want none", got)
	}
}

// A burst of events is one request, after the delay, and a status-only update
// is an event too (no generation predicate: a new credentialRevision is a
// status write).
func TestDebouncedEnqueuesOnceAfterTheDelay(t *testing.T) {
	r := &MailServerReconciler{}
	h := debounced(r.mapUserServer, 50*time.Millisecond)
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	old, updated := userIn("a", apiUserName, testServerName, objA), userIn("a", apiUserName, testServerName, objA)
	updated.Status.CredentialRevision = "42"
	ctx := context.Background()
	for range 5 {
		h.Update(ctx, event.UpdateEvent{ObjectOld: old, ObjectNew: updated}, q)
	}
	h.Create(ctx, event.CreateEvent{Object: userIn("a", userBob, testServerName, objA)}, q)
	if q.Len() != 0 {
		t.Fatalf("the request is queued at once: %d", q.Len())
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if q.Len() != 1 {
		t.Errorf("queue length = %d, want one request for the server", q.Len())
	}
}
