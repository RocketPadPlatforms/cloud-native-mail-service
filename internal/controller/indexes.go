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
)

// IndexFields registers the field indexes of MailDomains and
// MailServers. Call it exactly once per manager, before the MailServer
// and MailDomain reconcilers are set up: both need the indexes, and a
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
	return idx.IndexField(context.Background(), &cnmsv1alpha1.MailServer{}, mailServerHostnameIndex, indexMailServerByHostname)
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

// indexMailServerByHostname is the extractor of mailServerHostnameIndex.
func indexMailServerByHostname(obj client.Object) []string {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok || s.Spec.Hostname == "" {
		return nil
	}
	return []string{s.Spec.Hostname}
}
