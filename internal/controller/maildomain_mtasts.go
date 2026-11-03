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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// mtaSTSHosts returns the mx hosts of the MTA-STS policy of md (ADR
// 0012): the server hostname of every MailServer in the namespace of md
// that lists it and serves it. The hostname is the name the server
// presents (HELO, certificate), so it is the name the MX record has to
// point at. Several servers give several hosts, which the policy lists
// sorted without duplicates, so the result does not depend on the order
// of the objects. A server that does not serve the domain (it is not
// authorized for it) contributes nothing.
func (r *MailDomainReconciler) mtaSTSHosts(ctx context.Context, md *cnmsv1alpha1.MailDomain) ([]string, error) {
	servers := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, servers, client.InNamespace(md.Namespace)); err != nil {
		return nil, err
	}
	var candidates []*cnmsv1alpha1.MailServer
	for i := range servers.Items {
		if serverLists(&servers.Items[i], md.Name) {
			candidates = append(candidates, &servers.Items[i])
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	grants, claims, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, s := range candidates {
		auth := (&authorizationEvaluator{
			server: s, grants: grantRefs(grants), claims: domainClaims(claims), mode: r.DomainAuthorization,
		}).Evaluate()
		if slices.Contains(auth.domains, md.Spec.Domain) {
			req := render.RenderRequest{Server: s, AuthorizedDomains: auth.domains}
			hosts = append(hosts, req.ServerHostname())
		}
	}
	slices.Sort(hosts)
	return slices.Compact(hosts), nil
}

// serverLists reports whether the MailServer references the MailDomain.
func serverLists(s *cnmsv1alpha1.MailServer, mailDomain string) bool {
	return slices.ContainsFunc(s.Spec.Domains, func(ref cnmsv1alpha1.NameReference) bool {
		return ref.Name == mailDomain
	})
}

// mapServerDomains enqueues the MailDomains a MailServer references: a
// server that starts or stops serving a domain changes the mx hosts of
// its policy. A deleted server is mapped by its last spec.
func (r *MailDomainReconciler) mapServerDomains(_ context.Context, obj client.Object) []reconcile.Request {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(s.Spec.Domains))
	for _, ref := range s.Spec.Domains {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKey{Namespace: s.Namespace, Name: ref.Name}})
	}
	return reqs
}
