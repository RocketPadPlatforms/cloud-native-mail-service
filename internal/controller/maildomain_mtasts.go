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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// mtaSTSHosts returns the mx hosts of the MTA-STS policy of md (ADR
// 0012, 0014): the server hostname of the MailServer in spec.serverRef,
// when it exists and serves the domain. The hostname is the name the
// server presents (HELO, certificate), so it is the name the MX record
// has to point at. A server that does not serve the domain (it is not
// authorized for it) contributes nothing.
func (r *MailDomainReconciler) mtaSTSHosts(ctx context.Context, md *cnmsv1alpha1.MailDomain) ([]string, error) {
	s := &cnmsv1alpha1.MailServer{}
	err := r.Get(ctx, types.NamespacedName{Namespace: md.Namespace, Name: md.Spec.ServerRef.Name}, s)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	grants, claims, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return nil, err
	}
	auth := (&authorizationEvaluator{
		server: s, grants: grantRefs(grants), claims: domainClaims(claims), mode: r.DomainAuthorization,
	}).Evaluate()
	if !slices.Contains(auth.domains, md.Spec.Domain) {
		return nil, nil
	}
	req := render.RenderRequest{Server: s, AuthorizedDomains: auth.domains}
	return []string{req.ServerHostname()}, nil
}

// mapServerDomains enqueues the MailDomains that reference a
// MailServer: a server that appears, disappears or changes which
// domains it serves changes the mx host of their policy and their
// ServerFound condition. A deleted server is mapped by its last
// content.
func (r *MailDomainReconciler) mapServerDomains(ctx context.Context, obj client.Object) []reconcile.Request {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok {
		return nil
	}
	list := &cnmsv1alpha1.MailDomainList{}
	if err := r.List(ctx, list, client.InNamespace(s.Namespace),
		client.MatchingFields{mailDomainServerIndex: s.Name}); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailDomains", "server", s.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
