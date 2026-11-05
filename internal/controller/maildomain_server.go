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
)

// serverView is what a MailDomain needs from the MailServer in its
// spec.serverRef (ADR 0014): whether the server serves the domain, the
// hostname the MX record and the MTA-STS policy point at, and the
// sending sources that join the SPF record. It is the verdict the
// MailServer controller reaches, because it is the same evaluation.
type serverView struct {
	// found is false when the server does not exist.
	found bool
	// serves is true when the server authorizes the domain, that is,
	// renders it.
	serves bool
	// hostname is the authorized hostname of the server; empty while it
	// is not authorized, which hostnameFailure then explains.
	hostname        string
	hostnameFailure *domainFailure
	// spf holds the sending sources of the server.
	spf *cnmsv1alpha1.ServerSPF
}

// presented returns the hostname the domain's records point at: the
// authorized hostname of the server, when it serves the domain. The
// in-cluster name of the Service is never one.
func (v serverView) presented() string {
	if !v.serves {
		return ""
	}
	return v.hostname
}

// noHost returns the reason and message of an MTASTSReady condition
// without a policy, for a view that has no presented hostname.
func (v serverView) noHost() (reason, message string) {
	if v.found && v.serves && v.hostnameFailure != nil {
		return ReasonHostnameNotAuthorized, "the MailServer hostname is not authorized, so there is no mx host to put in the policy: " + v.hostnameFailure.message
	}
	return ReasonNoMailServer, "the MailServer in spec.serverRef does not exist or does not serve the domain, so there is no mx host to put in the policy"
}

// serverView evaluates the MailServer in spec.serverRef of md, the way
// the MailServer controller does (ADR 0003, 0014).
func (r *MailDomainReconciler) serverView(ctx context.Context, md *cnmsv1alpha1.MailDomain) (serverView, error) {
	s := &cnmsv1alpha1.MailServer{}
	err := r.Get(ctx, types.NamespacedName{Namespace: md.Namespace, Name: md.Spec.ServerRef.Name}, s)
	if errors.IsNotFound(err) {
		return serverView{}, nil
	}
	if err != nil {
		return serverView{}, err
	}
	grants, claims, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return serverView{}, err
	}
	servers := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, servers); err != nil {
		return serverView{}, err
	}
	auth := (&authorizationEvaluator{
		server: s, grants: grantRefs(grants), claims: domainClaims(claims),
		servers: serverClaims(servers), mode: r.DomainAuthorization,
	}).Evaluate()
	return serverView{
		found:           true,
		serves:          slices.Contains(auth.domains, md.Spec.Domain),
		hostname:        auth.hostname,
		hostnameFailure: auth.hostnameFailure,
		spf:             s.Spec.SPF,
	}, nil
}

// mapServerDomains enqueues the MailDomains that reference a
// MailServer, and those of every other MailServer that uses the same
// hostname: a server that appears, disappears, is edited or changes its
// hostname changes the MX record and the mx host of the policy of its
// domains, their SPF record, their ServerFound condition, and, through
// the uniqueness of the hostname, the verdict on the other servers of
// that name. For a renamed server the handler also maps the old
// object, and a deleted server is mapped by its last content.
func (r *MailDomainReconciler) mapServerDomains(ctx context.Context, obj client.Object) []reconcile.Request {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok {
		return nil
	}
	servers := []types.NamespacedName{client.ObjectKeyFromObject(s)}
	if s.Spec.Hostname != "" {
		peers := &cnmsv1alpha1.MailServerList{}
		if err := r.List(ctx, peers, client.MatchingFields{mailServerHostnameIndex: s.Spec.Hostname}); err != nil {
			log.FromContext(ctx).Error(err, "Could not list MailServers", "hostname", s.Spec.Hostname)
		}
		for i := range peers.Items {
			servers = append(servers, client.ObjectKeyFromObject(&peers.Items[i]))
		}
	}
	var reqs []reconcile.Request
	seen := map[types.NamespacedName]bool{}
	for _, key := range servers {
		list := &cnmsv1alpha1.MailDomainList{}
		if err := r.List(ctx, list, client.InNamespace(key.Namespace),
			client.MatchingFields{mailDomainServerIndex: key.Name}); err != nil {
			log.FromContext(ctx).Error(err, "Could not list MailDomains", "server", key.Name)
			continue
		}
		for i := range list.Items {
			if k := client.ObjectKeyFromObject(&list.Items[i]); !seen[k] {
				seen[k] = true
				reqs = append(reqs, reconcile.Request{NamespacedName: k})
			}
		}
	}
	return reqs
}
