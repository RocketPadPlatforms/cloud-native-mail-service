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

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

// namespaceDomains indexes the MailDomains of one namespace by the name of
// the object, which is what a MailUser names in domainRef.
func namespaceDomains(claims *cnmsv1alpha1.MailDomainList, namespace string) map[string]cnmsv1alpha1.MailDomain {
	out := map[string]cnmsv1alpha1.MailDomain{}
	for i := range claims.Items {
		if claims.Items[i].Namespace == namespace {
			out[claims.Items[i].Name] = claims.Items[i]
		}
	}
	return out
}

// serverAddressInput builds the input of the address decision for one
// MailServer from the objects: the accounts of the namespace that name the
// server (users may hold others, which are left out), and the state of
// every MailDomain they name (ADR 0015, sections 4.1 and 4.6): missing,
// served by another server, not authorized, or ok. It is the one place that
// maps objects to addresses.Input, so that the status of an account (the
// MailUser controller) and the tables that are rendered (the MailServer
// controller) come from the same decision. The second result holds, for each
// MailDomain that fails, the message for a condition, by the name of the
// object. It reads its arguments and changes none of them.
//
// senders are the senders of the service accounts that claim an address on
// the server (serviceSenders). A sender names its domain by its DNS name,
// which is resolved to the MailDomain of the server's namespace (domains) so
// that it shares the state of a MailUser that names the same object; a
// domain no object of the namespace carries is not found, under a key that
// no object name can have (ADR 0015, section 4.9).
func serverAddressInput(server string, users []cnmsv1alpha1.MailUser, senders []serviceSender, domains map[string]cnmsv1alpha1.MailDomain, ev *authorizationEvaluator) (addresses.Input, map[string]string) {
	in := addresses.Input{Domains: map[string]addresses.Domain{}}
	failures := map[string]string{}
	state := func(ref string) {
		if _, done := in.Domains[ref]; done {
			return
		}
		md, ok := domains[ref]
		switch {
		case !ok:
			in.Domains[ref] = addresses.Domain{State: addresses.DomainNotFound}
			failures[ref] = fmt.Sprintf("the MailDomain %q does not exist", ref)
		case md.Spec.ServerRef.Name != server:
			in.Domains[ref] = addresses.Domain{Name: md.Spec.Domain, State: addresses.DomainNotServed}
			failures[ref] = fmt.Sprintf("the MailDomain %q is served by the MailServer %q, not by %q", ref, md.Spec.ServerRef.Name, server)
		default:
			if f := ev.authorize(md.Namespace, md.Spec.Domain); f != nil {
				in.Domains[ref] = addresses.Domain{Name: md.Spec.Domain, State: addresses.DomainNotAuthorized}
				failures[ref] = fmt.Sprintf("the MailDomain %q is not authorized: %s", ref, f.message)
				return
			}
			in.Domains[ref] = addresses.Domain{Name: md.Spec.Domain, State: addresses.DomainOK}
		}
	}
	for i := range users {
		u := &users[i]
		if u.Spec.ServerRef.Name != server {
			continue
		}
		in.Users = append(in.Users, addresses.User{
			Name: u.Name, Disabled: u.Spec.Disabled != nil && *u.Spec.Disabled,
			Primary: addresses.Address{Local: u.Name, Domain: u.Spec.DomainRef.Name},
			Aliases: aliasAddresses(u),
		})
		state(u.Spec.DomainRef.Name)
		for _, a := range u.Spec.Aliases {
			state(a.DomainRef.Name)
		}
	}
	for _, s := range senders {
		ref := senderDomainRef(s.domain, domains, state, &in)
		if _, found := domains[ref]; !found {
			failures[ref] = fmt.Sprintf("no MailDomain of the namespace of the MailServer is for the domain %q", s.domain)
		}
		in.Senders = append(in.Senders, addresses.Sender{Owner: s.owner, Address: addresses.Address{Local: s.local, Domain: ref}})
	}
	return in, failures
}

// senderDomainKeyPrefix starts the key of a sender domain that no MailDomain
// carries: an object name is a DNS subdomain and has no colon.
const senderDomainKeyPrefix = "dns:"

// senderDomainRef returns the key into in.Domains for the DNS name of a
// sender: the name of the MailDomain object that carries it (the one that is
// ok if several do, else the first by name), after state has judged it, or a
// key of senderDomainKeyPrefix and the name when none does.
func senderDomainRef(domain string, domains map[string]cnmsv1alpha1.MailDomain, state func(string), in *addresses.Input) string {
	var refs []string
	for name, md := range domains {
		if md.Spec.Domain == domain {
			refs = append(refs, name)
		}
	}
	if len(refs) == 0 {
		ref := senderDomainKeyPrefix + domain
		in.Domains[ref] = addresses.Domain{State: addresses.DomainNotFound}
		return ref
	}
	slices.Sort(refs)
	for _, ref := range refs {
		state(ref)
		if in.Domains[ref].State == addresses.DomainOK {
			return ref
		}
	}
	return refs[0]
}

func aliasAddresses(u *cnmsv1alpha1.MailUser) []addresses.Address {
	out := make([]addresses.Address, 0, len(u.Spec.Aliases))
	for _, a := range u.Spec.Aliases {
		out = append(out, addresses.Address{Local: a.LocalPart, Domain: a.DomainRef.Name})
	}
	return out
}
