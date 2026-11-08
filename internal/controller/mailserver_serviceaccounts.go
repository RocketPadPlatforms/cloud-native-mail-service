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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// serverServiceAccounts lists the MailServiceAccounts, of every namespace, that
// name the server (ADR 0015, section 7): through the index of the cache, and
// only read. They are claimants of the address space whether or not the
// server consents to them; serviceSenders and acceptedAccounts say which.
func (r *MailServerReconciler) serverServiceAccounts(ctx context.Context, server *cnmsv1alpha1.MailServer) ([]cnmsv1alpha1.MailServiceAccount, error) {
	list := &cnmsv1alpha1.MailServiceAccountList{}
	if err := r.List(ctx, list, client.MatchingFields{mailServiceAccountServerIndex: server.Namespace + "/" + server.Name},
		client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// acceptedAccounts are the accounts the server may serve: it consents to their
// namespace and the domain of their sender (the claimants of serviceSenders),
// and the account admitted the sender for this server and its directory (the
// check against the directory of ADR 0015, section 4.9, point 3, which only
// the account controller makes). Whether the server serves one is decided by
// addresses.Decide with every claimant, not by this.
func acceptedAccounts(server *cnmsv1alpha1.MailServer, accounts []cnmsv1alpha1.MailServiceAccount) []cnmsv1alpha1.MailServiceAccount {
	var out []cnmsv1alpha1.MailServiceAccount
	for i := range accounts {
		a := &accounts[i]
		allowed, domains := accountConsent(server, a.Namespace)
		_, domain := senderParts(a.Spec.Sender)
		if allowed && senderDomainAllowed(domains, domain) && admitted(a, server) {
			out = append(out, *a)
		}
	}
	return out
}

// senderClaims are the senders of the claimants in the form of the static
// users' decision, which names domains by their DNS names.
func senderClaims(senders []serviceSender) []render.SenderClaim {
	out := make([]render.SenderClaim, 0, len(senders))
	for _, s := range senders {
		out = append(out, render.SenderClaim{Owner: s.owner, Local: s.local, Domain: s.domain})
	}
	return out
}

// serviceCredentialHashes reads the password hashes of the accounts from their
// credential Secrets, with one label-selected list (uncached) per namespace of
// an account, not one get per account: the namespaces are few (ADR 0015,
// section 6). The result is by "<namespace>/<name>" of the account. A hash is
// taken only from a Secret that the account controls (the owner reference
// names its UID) and only if it has the form the operator writes: the Secret
// is in a namespace of the tenant, and whatever is in it ends in a file that
// Dovecot parses. The login is never read from it: it is derived from the
// account object (serviceAccountLogin). An account without such a hash has no
// login.
func (r *MailServerReconciler) serviceCredentialHashes(ctx context.Context, server *cnmsv1alpha1.MailServer, accounts []cnmsv1alpha1.MailServiceAccount) (map[string]string, error) {
	hashes := make(map[string]string, len(accounts))
	byNamespace := map[string][]*cnmsv1alpha1.MailServiceAccount{}
	for i := range accounts {
		byNamespace[accounts[i].Namespace] = append(byNamespace[accounts[i].Namespace], &accounts[i])
	}
	for ns, group := range byNamespace {
		sel := labels.NewSelector()
		for _, req := range []struct{ key, val string }{
			{manifests.ServerLabel, server.Name},
			{labelServerNamespace, server.Namespace},
			{manifests.ManagedByLabel, manifests.ManagedByValue},
		} {
			requirement, err := labels.NewRequirement(req.key, selection.Equals, []string{req.val})
			if err != nil {
				return nil, err
			}
			sel = sel.Add(*requirement)
		}
		list := &corev1.SecretList{}
		if err := r.APIReader.List(ctx, list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: sel}); err != nil {
			return nil, err
		}
		byName := make(map[string]*corev1.Secret, len(list.Items))
		for i := range list.Items {
			byName[list.Items[i].Name] = &list.Items[i]
		}
		for _, a := range group {
			s := byName[accountCredentialName(a.Name)]
			if s == nil || !metav1.IsControlledBy(s, a) {
				continue
			}
			if hash := string(s.Data[keyPasswordHash]); validStoredHash(hash) {
				hashes[a.Namespace+"/"+a.Name] = hash
			}
		}
	}
	return hashes, nil
}

// serviceAccountAccounts turns the verdict of addresses.Decide for the senders
// into the accounts of a render: one send-only account per accepted account
// whose sender is served, with the login of the account object and the hash of
// its credential (empty: no login, the sender is still owned). An account whose
// sender is not served (a conflict, a revoked domain) is not rendered at all:
// it owns nothing and cannot log in. Two accounts with the same login (a
// collision of the 64-bit prefix, which ADR 0015 section 4.9 treats like any
// other conflict) are both left out.
func serviceAccountAccounts(res addresses.Result, accepted []cnmsv1alpha1.MailServiceAccount, hashes map[string]string) []render.Account {
	rows := make(map[string]addresses.Row, len(res.Senders))
	for _, s := range res.Senders {
		rows[s.Owner] = s.Row
	}
	logins := map[string]int{}
	for i := range accepted {
		logins[serviceAccountLogin(accepted[i].Namespace, accepted[i].Name)]++
	}
	var out []render.Account
	for i := range accepted {
		a := &accepted[i]
		key := a.Namespace + "/" + a.Name
		login := serviceAccountLogin(a.Namespace, a.Name)
		row, ok := rows[key]
		if !ok || !row.Served || logins[login] > 1 {
			continue
		}
		account := render.Account{
			Name: login, PasswordHash: hashes[key], Address: row.Address, SendOnly: true,
			Verdict: addresses.UserResult{Name: login, Rows: []addresses.Row{{
				Address: row.Address, Kind: addresses.KindPrimary, Served: true, Reason: addresses.ReasonServed, MaySend: true,
			}}},
		}
		// An account that the renderers would refuse is left out, so that it
		// cannot stop the render of the others (and of the whole server).
		if render.ValidateAccount(account) != nil {
			continue
		}
		out = append(out, account)
	}
	slices.SortFunc(out, func(x, y render.Account) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// mapAccountServer enqueues the MailServer that a MailServiceAccount names,
// in the namespace serverRef says. For an update the handler maps the old
// object too, so an account that moves to another server reaches the one it
// left.
func (r *MailServerReconciler) mapAccountServer(_ context.Context, obj client.Object) []reconcile.Request {
	a, ok := obj.(*cnmsv1alpha1.MailServiceAccount)
	if !ok || a.Spec.ServerRef.Name == "" {
		return nil
	}
	ns := a.Spec.ServerRef.Namespace
	if ns == "" {
		ns = a.Namespace
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: a.Spec.ServerRef.Name}}}
}
