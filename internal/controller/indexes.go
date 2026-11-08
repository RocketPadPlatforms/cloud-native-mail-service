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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// Field indexes of MailDomains and MailServers. The watch map functions
// use them; the reconcilers filter the lists they already load (ADR
// 0014), because a field selector on a List is only valid against the
// cache.
const (
	// mailDomainDomainIndex indexes by spec.domain: uniqueness is
	// evaluated across namespaces, so a claim must find the other
	// claimants of its domain.
	mailDomainDomainIndex = "spec.domain"
	// mailDomainServerIndex indexes by spec.serverRef.name: a MailServer
	// must find the MailDomains that reference it.
	mailDomainServerIndex = "spec.serverRef.name"
	// mailServerHostnameIndex indexes MailServers by spec.hostname: the
	// hostname is unique across namespaces, so a server must find the
	// other claimants of its hostname, and a MailDomain apex the
	// servers presenting it.
	mailServerHostnameIndex = "spec.hostname"
	// mailServerDirectoryIndex indexes MailServers by
	// spec.directoryRef.name: a MailDirectory event must reach the
	// servers that use it, and no others (ADR 0015, section 7).
	mailServerDirectoryIndex = "spec.directoryRef.name"

	// mailUserServerIndex indexes MailUsers by spec.serverRef.name: a
	// MailServer event must reach its accounts (ADR 0015, section 7).
	mailUserServerIndex = "spec.serverRef.name"
	// mailUserDomainIndex indexes MailUsers by every MailDomain they name:
	// the primary domainRef and the domainRef of each alias.
	mailUserDomainIndex = "spec.domainRefs"
	// mailUserAddressIndex indexes MailUsers by every address they claim,
	// as <server>/<local part>@<MailDomain object name> (ADR 0015, section
	// 4.2, point 5), so that a conflict reaches the claimants and not every
	// account of the server.
	mailUserAddressIndex = "spec.claimedAddresses"
)

// IndexFields registers the field indexes of MailDomains, MailServers and
// MailUsers. Call it exactly once per manager, before the MailServer,
// MailDomain and MailUser reconcilers are set up: they need the indexes, and a
// manager rejects the same index twice.
func IndexFields(mgr ctrl.Manager) error {
	idx := mgr.GetFieldIndexer()
	for field, extract := range map[string]client.IndexerFunc{
		mailDomainDomainIndex: indexMailDomainByDomain,
		mailDomainServerIndex: indexMailDomainByServer,
	} {
		if err := idx.IndexField(context.Background(), &cnmsv1alpha1.MailDomain{}, field, extract); err != nil {
			return err
		}
	}
	for field, extract := range map[string]client.IndexerFunc{
		mailServerHostnameIndex:  indexMailServerByHostname,
		mailServerDirectoryIndex: indexMailServerByDirectory,
	} {
		if err := idx.IndexField(context.Background(), &cnmsv1alpha1.MailServer{}, field, extract); err != nil {
			return err
		}
	}
	for field, extract := range map[string]client.IndexerFunc{
		mailUserServerIndex:  indexMailUserByServer,
		mailUserDomainIndex:  indexMailUserByDomain,
		mailUserAddressIndex: indexMailUserByAddress,
	} {
		if err := idx.IndexField(context.Background(), &cnmsv1alpha1.MailUser{}, field, extract); err != nil {
			return err
		}
	}
	return nil
}

// indexMailDomainByDomain is the extractor of mailDomainDomainIndex.
func indexMailDomainByDomain(obj client.Object) []string {
	d, ok := obj.(*cnmsv1alpha1.MailDomain)
	if !ok || d.Spec.Domain == "" {
		return nil
	}
	return []string{d.Spec.Domain}
}

// indexMailDomainByServer is the extractor of mailDomainServerIndex.
func indexMailDomainByServer(obj client.Object) []string {
	d, ok := obj.(*cnmsv1alpha1.MailDomain)
	if !ok || d.Spec.ServerRef.Name == "" {
		return nil
	}
	return []string{d.Spec.ServerRef.Name}
}

// indexMailServerByDirectory is the extractor of mailServerDirectoryIndex.
func indexMailServerByDirectory(obj client.Object) []string {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok || s.Spec.DirectoryRef == nil || s.Spec.DirectoryRef.Name == "" {
		return nil
	}
	return []string{s.Spec.DirectoryRef.Name}
}

// indexMailServerByHostname is the extractor of mailServerHostnameIndex.
func indexMailServerByHostname(obj client.Object) []string {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok || s.Spec.Hostname == "" {
		return nil
	}
	return []string{s.Spec.Hostname}
}

// indexMailUserByServer is the extractor of mailUserServerIndex.
func indexMailUserByServer(obj client.Object) []string {
	u, ok := obj.(*cnmsv1alpha1.MailUser)
	if !ok || u.Spec.ServerRef.Name == "" {
		return nil
	}
	return []string{u.Spec.ServerRef.Name}
}

// indexMailUserByDomain is the extractor of mailUserDomainIndex: one value
// per distinct MailDomain the account names.
func indexMailUserByDomain(obj client.Object) []string {
	u, ok := obj.(*cnmsv1alpha1.MailUser)
	if !ok {
		return nil
	}
	var out []string
	add := func(ref string) {
		if ref != "" && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	add(u.Spec.DomainRef.Name)
	for _, a := range u.Spec.Aliases {
		add(a.DomainRef.Name)
	}
	return out
}

// indexMailUserByAddress is the extractor of mailUserAddressIndex.
func indexMailUserByAddress(obj client.Object) []string {
	u, ok := obj.(*cnmsv1alpha1.MailUser)
	if !ok {
		return nil
	}
	return claimKeys(u)
}

// claimKeys are the addresses an account claims, folded to lower case, in
// the form of mailUserAddressIndex.
func claimKeys(u *cnmsv1alpha1.MailUser) []string {
	prefix := u.Spec.ServerRef.Name + "/"
	keys := []string{prefix + strings.ToLower(u.Name) + "@" + u.Spec.DomainRef.Name}
	for _, a := range u.Spec.Aliases {
		if k := prefix + strings.ToLower(a.LocalPart) + "@" + a.DomainRef.Name; !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}
