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
	"net/netip"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// ConditionAddressRecords is true when status.records lists the A and
// AAAA records of the hostname, that is, when the inbound Service has an
// address. It is false with a reason that says why none is required or
// known yet (ADR 0014). It is a warning only: it never feeds into
// Ready, and neither does the verification of the records.
const ConditionAddressRecords = "AddressRecords"

// AddressRecords condition reasons.
const (
	// ReasonAddressesListed is set when the inbound Service has
	// addresses.
	ReasonAddressesListed = "AddressesListed"
	// ReasonNoExternalAddress is set when the inbound Service is of
	// type ClusterIP: it is not reachable from outside the cluster, so
	// no A or AAAA record is required.
	ReasonNoExternalAddress = "NoExternalAddress"
	// ReasonAddressPending is set while the LoadBalancer Service has no
	// ingress address yet.
	ReasonAddressPending = "AddressPending"
	// ReasonLoadBalancerHostname is set when the LoadBalancer reports a
	// DNS name instead of an address: no A or AAAA record is
	// required for it, the hostname needs a CNAME or alias to that
	// name.
	ReasonLoadBalancerHostname = "LoadBalancerHostname"
)

// maxServerRecords is the MaxItems of status.records. An inbound Service
// does not have anywhere near that many addresses; the address records
// are cut to one less, which leaves the room of the SPF record.
const maxServerRecords = 64

// reconcileRecords sets status.records and the conditions that describe
// them: the A and AAAA records of the hostname to the addresses of the
// inbound Service, and the SPF record of the hostname (ADR 0014). They
// are reported and verified, never published, and never feed into
// Ready. It returns the delay after which the records are looked up
// again.
//
// Without an authorized hostname nothing is required, so the status
// lists no record. The verification state lives in memory only (like the
// one of a MailDomain): after a restart the records are looked up again.
func (r *MailServerReconciler) reconcileRecords(ctx context.Context, server *cnmsv1alpha1.MailServer, auth authorization) (time.Duration, error) {
	conds, key := &server.Status.Conditions, client.ObjectKeyFromObject(server)
	if auth.hostname == "" {
		r.dns.forget(key)
		server.Status.Records = nil
		meta.RemoveStatusCondition(conds, ConditionAddressRecords)
		meta.RemoveStatusCondition(conds, ConditionRecordsVerified)
		return 0, nil
	}
	addrs, status, reason, message, err := r.inboundAddresses(ctx, server)
	if err != nil {
		return 0, err
	}
	setCondition(conds, server.Generation, ConditionAddressRecords, status, reason, message)

	in := dnsrecords.ServerInput{Hostname: auth.hostname, Addresses: addrs[:min(len(addrs), maxServerRecords-1)]}
	if s := server.Spec.SPF; s != nil {
		in.SPF = &dnsrecords.SPF{IP4: s.IP4, IP6: s.IP6}
	}
	// A name holds one SPF record: a MailDomain of the hostname itself
	// carries it, with the sources of the server among its own. Only
	// a domain this server serves does: one of another server or an
	// unauthorized one publishes nothing for it.
	in.SkipSPF = slices.Contains(auth.domains, auth.hostname)
	recs, err := dnsrecords.ComposeServer(in)
	if err != nil {
		return 0, err
	}

	now := r.now()
	st := r.dns.get(key)
	ran := st.due(now, recs)
	if ran {
		st.lookup(ctx, r.Resolver, recs, server.Status.Records, now)
	}
	records, unverified, unknown := statusRecords(recs, server.Status.Records, st)
	server.Status.Records = records
	vStatus, vReason, vMessage := recordsVerifiedCondition(len(records), unverified, unknown, st)
	setCondition(conds, server.Generation, ConditionRecordsVerified, vStatus, vReason, vMessage)

	allVerified := true
	for _, rec := range recs {
		if res, ok := st.results[recordKey(rec)]; !ok || res.unknown || res.verified == nil || !*res.verified {
			allVerified = false
		}
	}
	switch {
	case st.missing(recs):
		st.nextDue = now
	case ran:
		st.schedule(now, allVerified)
	}
	return max(st.nextDue.Sub(now), time.Second), nil
}

// inboundAddresses returns the addresses of the inbound Service and the
// AddressRecords condition that describes them. The Service is read from
// the cache (the operator labels it). Addresses are the ingress IPs of a
// LoadBalancer; a ClusterIP Service has none to publish.
func (r *MailServerReconciler) inboundAddresses(ctx context.Context, server *cnmsv1alpha1.MailServer) (addrs []string, status metav1.ConditionStatus, reason, message string, err error) {
	svc := &corev1.Service{}
	name := render.ServiceName(server.Name, render.ComponentPostfix)
	err = r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, svc)
	if errors.IsNotFound(err) {
		return nil, metav1.ConditionFalse, ReasonAddressPending, fmt.Sprintf("the inbound Service %s does not exist yet", name), nil
	}
	if err != nil {
		return nil, "", "", "", err
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return nil, metav1.ConditionFalse, ReasonNoExternalAddress, fmt.Sprintf(
			"the inbound Service is of type %s, which is not reachable from outside the cluster, so no A or AAAA record is required", svc.Spec.Type), nil
	}
	var names []string
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		switch {
		case ing.IP != "":
			if a, perr := netip.ParseAddr(ing.IP); perr == nil {
				addrs = append(addrs, a.String())
			}
		case ing.Hostname != "":
			names = append(names, ing.Hostname)
		}
	}
	slices.Sort(addrs)
	addrs = slices.Compact(addrs)
	switch {
	case len(addrs) > 0:
		return addrs, metav1.ConditionTrue, ReasonAddressesListed,
			fmt.Sprintf("%d addresses of the inbound Service are listed", len(addrs)), nil
	case len(names) > 0:
		slices.Sort(names)
		return nil, metav1.ConditionFalse, ReasonLoadBalancerHostname, fmt.Sprintf(
			"the LoadBalancer reports the DNS name %s instead of an address, so no A or AAAA record is listed; point the hostname at that name with a CNAME or an alias record",
			strings.Join(names, ", ")), nil
	}
	return nil, metav1.ConditionFalse, ReasonAddressPending, "the LoadBalancer of the inbound Service has no address yet", nil
}
