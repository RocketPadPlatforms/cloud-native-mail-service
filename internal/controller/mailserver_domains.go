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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// maxStatusDomains is the most entries status.domains holds; it is the
// MaxItems of the field.
const maxStatusDomains = 128

// noDomainsMessage explains the NoDomains reason.
const noDomainsMessage = "no MailDomain of this namespace references this MailServer in spec.serverRef, so there is nothing to serve"

// DomainsListed condition reasons.
const (
	reasonAllListed = "AllListed"
	reasonTruncated = "Truncated"
)

// setDomainsStatus fills status.domains and DomainsListed from the
// evaluation (ADR 0014). A domain is served when it is authorized and
// the normal render is applied (blocked is nil). The list puts the
// domains that are not served first, then goes by name, and is cut at
// maxStatusDomains: the entries that need attention are the ones that
// must not fall off.
func setDomainsStatus(server *cnmsv1alpha1.MailServer, auth authorization, blocked *blockedBy) {
	entries := make([]cnmsv1alpha1.ServerDomainStatus, 0, len(auth.attached))
	for _, a := range auth.attached {
		e := cnmsv1alpha1.ServerDomainStatus{Name: a.name, Domain: a.domain, Served: a.failure == nil && blocked == nil}
		switch {
		case a.failure != nil:
			e.Reason = a.failure.reason
		case blocked != nil:
			e.Reason = blocked.reason
		case auth.mode == AuthorizationUnrestricted:
			e.Reason = ReasonAuthorizationDisabled
		default:
			e.Reason = ReasonAuthorized
		}
		entries = append(entries, e)
	}
	slices.SortStableFunc(entries, func(a, b cnmsv1alpha1.ServerDomainStatus) int {
		switch {
		case a.Served == b.Served:
			return strings.Compare(a.Name, b.Name)
		case !a.Served:
			return -1
		}
		return 1
	})
	total := len(entries)
	status, reason, message := metav1.ConditionTrue, reasonAllListed,
		fmt.Sprintf("status.domains lists all %d MailDomains that reference this MailServer", total)
	if total > maxStatusDomains {
		entries = entries[:maxStatusDomains]
		status, reason, message = metav1.ConditionFalse, reasonTruncated, fmt.Sprintf(
			"status.domains lists %d of the %d MailDomains that reference this MailServer: those not served first, then by name",
			maxStatusDomains, total)
	}
	if total == 0 {
		entries = nil
	}
	server.Status.Domains = entries
	setCondition(&server.Status.Conditions, server.Generation, ConditionDomainsListed, status, reason, message)
}
