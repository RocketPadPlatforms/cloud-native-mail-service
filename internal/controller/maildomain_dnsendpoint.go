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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

// crdDNSEndpoint is the external-dns CRD of the optional DNSEndpoint
// integration (ADR 0012).
const crdDNSEndpoint = "dnsendpoints.externaldns.k8s.io"

// dnsEndpointGVK is the DNSEndpoint of external-dns v0.24.0
// (unstructured: the operator does not depend on its Go module).
var dnsEndpointGVK = schema.GroupVersionKind{Group: "externaldns.k8s.io", Version: "v1alpha1", Kind: "DNSEndpoint"}

// recordTTL is the TTL of every published record, in seconds.
const recordTTL = int64(3600)

// revocationGrace is how long the DNSEndpoint of a domain stays after
// its authorization is lost (ADR 0003): a GitOps sync that briefly
// prunes and re-creates a grant must not delete DKIM records worldwide.
// Signing stops at once; only the records wait. The clock is the
// transition time of the Authorized condition, so a restore, which has
// no status, restarts the grace period: the safe direction.
const revocationGrace = 15 * time.Minute

// Reasons of the DNSPublished condition.
const (
	// ReasonDNSEndpointApplied is set when the DNSEndpoint is managed.
	ReasonDNSEndpointApplied = "DNSEndpointApplied"
	// ReasonCRDCheckFailed is set when the DNSEndpoint CRD could not be
	// looked up.
	ReasonCRDCheckFailed = "CRDCheckFailed"
	// ReasonAuthorizationRevoked is set while the DNSEndpoint of a
	// revoked domain waits for the end of the grace period.
	ReasonAuthorizationRevoked = "AuthorizationRevoked"
)

func dnsEndpointName(md *cnmsv1alpha1.MailDomain) string { return md.Name + "-dns" }

// desiredDNSEndpoint builds the DNSEndpoint of md: one endpoint per
// record, type TXT, one plain target with the whole value (ADR 0012;
// how a provider handles a value over 255 octets is not verified).
func desiredDNSEndpoint(md *cnmsv1alpha1.MailDomain, records []dnsrecords.Record) *unstructured.Unstructured {
	endpoints := make([]any, 0, len(records))
	for _, rec := range records {
		endpoints = append(endpoints, map[string]any{
			"dnsName":    rec.Name,
			"recordType": rec.Type,
			"recordTTL":  recordTTL,
			"targets":    []any{rec.Value()},
		})
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"endpoints": endpoints}}}
	u.SetGroupVersionKind(dnsEndpointGVK)
	u.SetName(dnsEndpointName(md))
	u.SetNamespace(md.Namespace)
	u.SetLabels(keySecretLabels(md))
	return u
}

// reconcileDNSEndpoint publishes the records through a DNSEndpoint in
// externalDNS mode and sets DNSPublished. In manual mode there is no
// DNSEndpoint: one the MailDomain controls (the mode was switched) is
// removed and the condition is dropped. An object of that name that the
// MailDomain does not control is never touched (ownership conflict).
// It returns the delay after which the integration has to be looked at
// again: the CRD may be installed later (principle 10).
func (r *MailDomainReconciler) reconcileDNSEndpoint(ctx context.Context, md *cnmsv1alpha1.MailDomain, records []dnsrecords.Record) time.Duration {
	installed, err := r.crds.installed(ctx, r.APIReader, crdDNSEndpoint)
	if deref(md.Spec.DNSMode) != "externalDNS" {
		meta.RemoveStatusCondition(&md.Status.Conditions, ConditionDNSPublished)
		if err != nil { // a leftover endpoint is cleaned on a later pass
			return requeueInterval
		}
		if installed {
			if err := r.deleteOwnedEndpoint(ctx, md); err != nil {
				log.FromContext(ctx).Error(err, "Could not delete DNSEndpoint", "name", dnsEndpointName(md))
				return requeueInterval
			}
		}
		return 0
	}
	if err != nil {
		r.setCondition(md, ConditionDNSPublished, metav1.ConditionFalse, ReasonCRDCheckFailed, err.Error())
		return requeueInterval
	}
	if !installed {
		r.setCondition(md, ConditionDNSPublished, metav1.ConditionFalse, ReasonExternalDNSNotInstalled,
			"the DNSEndpoint CRD (externaldns.k8s.io) is not installed; the records are listed in status.records")
		return crdCacheTTL
	}
	desired := desiredDNSEndpoint(md, records)
	if err := applyObject(ctx, r.Client, r.APIReader, r.Scheme, md, desired); err != nil {
		r.setCondition(md, ConditionDNSPublished, metav1.ConditionFalse, applyFailureReason(err), err.Error())
		return requeueInterval
	}
	r.setCondition(md, ConditionDNSPublished, metav1.ConditionTrue, ReasonDNSEndpointApplied,
		fmt.Sprintf("DNSEndpoint %s declares %d records; external-dns publishes them, the verification shows when they are live",
			desired.GetName(), len(records)))
	return 0
}

// deleteOwnedEndpoint deletes the DNSEndpoint of md if it exists and
// the MailDomain controls it; it never touches a foreign one.
func (r *MailDomainReconciler) deleteOwnedEndpoint(ctx context.Context, md *cnmsv1alpha1.MailDomain) error {
	ep := &unstructured.Unstructured{}
	ep.SetGroupVersionKind(dnsEndpointGVK)
	err := r.APIReader.Get(ctx, client.ObjectKey{Name: dnsEndpointName(md), Namespace: md.Namespace}, ep)
	if errors.IsNotFound(err) || (err == nil && !metav1.IsControlledBy(ep, md)) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.Delete(ctx, ep); err != nil && !errors.IsNotFound(err) {
		return err
	}
	r.event(md, corev1.EventTypeNormal, "DNSEndpointRemoved", "RemoveDNSEndpoint",
		fmt.Sprintf("removed DNSEndpoint %s", ep.GetName()))
	return nil
}

// revokeDNSEndpoint removes the DNSEndpoint of an unauthorized domain
// once revocationGrace has passed since the authorization was lost,
// and until then reports the pending removal. It returns the delay of
// the next look.
func (r *MailDomainReconciler) revokeDNSEndpoint(ctx context.Context, md *cnmsv1alpha1.MailDomain) time.Duration {
	installed, err := r.crds.installed(ctx, r.APIReader, crdDNSEndpoint)
	if err != nil {
		return requeueInterval
	}
	if !installed {
		return crdCacheTTL
	}
	ep := &unstructured.Unstructured{}
	ep.SetGroupVersionKind(dnsEndpointGVK)
	err = r.APIReader.Get(ctx, client.ObjectKey{Name: dnsEndpointName(md), Namespace: md.Namespace}, ep)
	if errors.IsNotFound(err) || (err == nil && !metav1.IsControlledBy(ep, md)) {
		return 0
	}
	if err != nil {
		return requeueInterval
	}
	auth := meta.FindStatusCondition(md.Status.Conditions, ConditionAuthorized)
	remaining := auth.LastTransitionTime.Add(revocationGrace).Sub(r.now())
	if remaining > 0 {
		r.setCondition(md, ConditionDNSPublished, metav1.ConditionFalse, ReasonAuthorizationRevoked,
			fmt.Sprintf("the domain is not authorized; DNSEndpoint %s is removed at %s unless the authorization returns",
				ep.GetName(), auth.LastTransitionTime.Add(revocationGrace).UTC().Format(time.RFC3339)))
		return remaining
	}
	if err := r.deleteOwnedEndpoint(ctx, md); err != nil {
		log.FromContext(ctx).Error(err, "Could not delete DNSEndpoint", "name", ep.GetName())
		return requeueInterval
	}
	return 0
}
