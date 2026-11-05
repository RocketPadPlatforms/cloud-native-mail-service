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
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

func TestSetDomainsStatusCutsTheListKeepingTheUnservedOnes(t *testing.T) {
	var auth authorization
	for i := range maxStatusDomains + 2 {
		a := attachedDomain{name: fmt.Sprintf("d%03d", i), domain: fmt.Sprintf("d%03d.example.org", i)}
		if i >= maxStatusDomains { // sorts last by name, still listed: not served
			a.failure = &domainFailure{reason: ReasonNotGranted}
		}
		auth.attached = append(auth.attached, a)
	}
	server := &cnmsv1alpha1.MailServer{}
	setDomainsStatus(server, auth, nil)

	got := server.Status.Domains
	if len(got) != maxStatusDomains {
		t.Fatalf("len = %d, want %d", len(got), maxStatusDomains)
	}
	if got[0].Served || got[1].Served || got[0].Name != fmt.Sprintf("d%03d", maxStatusDomains) {
		t.Errorf("the unserved domains must come first: %+v %+v", got[0], got[1])
	}
	if !got[2].Served || got[2].Name != "d000" || got[len(got)-1].Name != fmt.Sprintf("d%03d", maxStatusDomains-3) {
		t.Errorf("the served domains follow by name and the last ones are cut: %+v ... %+v", got[2], got[len(got)-1])
	}
	c := meta.FindStatusCondition(server.Status.Conditions, ConditionDomainsListed)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != reasonTruncated {
		t.Errorf("DomainsListed = %+v, want False/%s", c, reasonTruncated)
	}
}

func TestSetDomainsStatusReasons(t *testing.T) {
	auth := authorization{mode: AuthorizationUnrestricted, attached: []attachedDomain{
		{name: "a", domain: "a.example.org"},
	}}
	server := &cnmsv1alpha1.MailServer{}
	setDomainsStatus(server, auth, nil)
	if d := server.Status.Domains[0]; !d.Served || d.Reason != ReasonAuthorizationDisabled {
		t.Errorf("unrestricted = %+v", d)
	}
	// An authorized domain is not served while the whole render is blocked.
	setDomainsStatus(server, auth, &blockedBy{ReasonDirectoryNotFound, "x"})
	if d := server.Status.Domains[0]; d.Served || d.Reason != ReasonDirectoryNotFound {
		t.Errorf("blocked = %+v", d)
	}
}
