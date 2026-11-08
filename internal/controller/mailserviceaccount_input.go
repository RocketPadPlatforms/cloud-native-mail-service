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
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// serviceAccountLoginPrefix starts the login of every service account: the
// reserved form of ADR 0015, section 4.9, point 1, which no MailUser name
// (a CEL rule) and no directory login (the lookup filters) can have.
const serviceAccountLoginPrefix = "sa."

// serviceAccountLogin is the account name and login of a service account:
// "sa." and the first 16 hexadecimal characters of the SHA-256 of
// "<namespace>/<name>" (ADR 0015, section 4.9, point 1).
func serviceAccountLogin(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return serviceAccountLoginPrefix + hex.EncodeToString(sum[:])[:16]
}

// accountConsent says whether a namespace may use a MailServer through a
// MailServiceAccount, and which sender domains the server limits it to
// (nil: every domain the server serves). A namespace is consented to by an
// entry of spec.serviceAccounts.allowedNamespaces; the namespace of the
// server needs none, unless an entry limits its domains (ADR 0008, 0015
// section 4.9, point 4).
func accountConsent(server *cnmsv1alpha1.MailServer, namespace string) (allowed bool, senderDomains []string) {
	if sa := server.Spec.ServiceAccounts; sa != nil {
		for _, ns := range sa.AllowedNamespaces {
			if ns.Name == namespace {
				return true, ns.SenderDomains
			}
		}
	}
	return namespace == server.Namespace, nil
}

// senderParts splits a sender into its local part and its domain. The
// API pattern allows exactly one "@".
func senderParts(sender string) (local, domain string) {
	local, domain, _ = strings.Cut(strings.ToLower(sender), "@")
	return local, domain
}

// senderDomainAllowed reports whether the domain of the sender is one the
// entry of the namespace allows: any domain when the entry names none.
func senderDomainAllowed(senderDomains []string, domain string) bool {
	return len(senderDomains) == 0 || slices.Contains(senderDomains, domain)
}

// serviceSender is the sender of one service account that takes part in the
// address decision of a server. Owner is "<namespace>/<name>" of the
// account.
type serviceSender struct {
	owner, local, domain string
}

// serviceSenders returns the senders that claim an address on the server:
// the accounts that name it, whose namespace it consents to and whose sender
// domain the entry of the namespace allows. An account that fails one of
// those checks claims nothing, so a namespace that is not consented to
// cannot take an address from one that is (ADR 0015, section 4.2, point 2).
// The list is sorted by owner and never shares the objects it is given.
func serviceSenders(server *cnmsv1alpha1.MailServer, accounts []cnmsv1alpha1.MailServiceAccount) []serviceSender {
	key := server.Namespace + "/" + server.Name
	var out []serviceSender
	for i := range accounts {
		a := &accounts[i]
		if accountServerKey(a) != key {
			continue
		}
		allowed, domains := accountConsent(server, a.Namespace)
		local, domain := senderParts(a.Spec.Sender)
		if !allowed || !senderDomainAllowed(domains, domain) {
			continue
		}
		out = append(out, serviceSender{owner: a.Namespace + "/" + a.Name, local: local, domain: domain})
	}
	slices.SortFunc(out, func(x, y serviceSender) int { return strings.Compare(x.owner, y.owner) })
	return out
}
