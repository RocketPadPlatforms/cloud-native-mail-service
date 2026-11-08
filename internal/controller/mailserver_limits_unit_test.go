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
	stderrors "errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// checkRendered applies the byte guard to every object of a revision, the live
// users Secret included, and names the object that is too large (an object
// name, no content).
func TestCheckRenderedNamesTheObject(t *testing.T) {
	plane := &manifests.DataPlane{
		Configs: []client.Object{
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mail-postfix-1"}, Data: map[string]string{"k": "small"}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mail-postfix-2"}, Data: map[string][]byte{"k": []byte("small")}},
		},
		LiveUsers: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: liveUsersName}, Data: map[string][]byte{"passdb": make([]byte, 99)}},
	}
	if err := checkRendered(plane, addresses.Limits{ObjectBytes: 200}); err != nil {
		t.Fatalf("checkRendered() = %v, want nil", err)
	}
	err := checkRendered(plane, addresses.Limits{ObjectBytes: 100})
	le, ok := stderrors.AsType[*addresses.LimitError](err)
	if !ok || le.Reason != ReasonConfigTooLarge || !strings.Contains(le.Message, liveUsersName+" holds 105 bytes") {
		t.Fatalf("checkRendered() = %v, want ConfigTooLarge naming the live Secret", err)
	}
	plane.Configs[0].(*corev1.ConfigMap).Data["big"] = strings.Repeat("x", 200)
	if err := checkRendered(plane, addresses.Limits{ObjectBytes: 150}); err == nil || !strings.Contains(err.Error(), "mail-postfix-1 holds") {
		t.Errorf("checkRendered() = %v, want the ConfigMap named", err)
	}
}

// The accounts of a render become the entries that Settle compares: a login
// needs no disabled flag, no suspension and a hash.
func TestAccountEntries(t *testing.T) {
	rows := func(addr string, served bool, aliases ...string) addresses.UserResult {
		r := make([]addresses.Row, 0, 1+len(aliases))
		r = append(r, addresses.Row{Address: addr, Kind: addresses.KindPrimary, Served: served})
		for _, a := range aliases {
			r = append(r, addresses.Row{Address: a, Kind: addresses.KindAlias, Served: true})
		}
		return addresses.UserResult{Rows: r, Suspended: !served}
	}
	got := accountEntries([]render.Account{
		{Name: "a", Address: "a@x.example", PasswordHash: "h", Verdict: rows("a@x.example", true, "b@x.example")},
		{Name: "off", Address: "off@x.example", PasswordHash: "h", Disabled: true, Verdict: rows("off@x.example", true)},
		{Name: "nohash", Address: "n@x.example", Verdict: rows("n@x.example", true)},
		{Name: "suspended", Address: "g@x.example", PasswordHash: "h", Verdict: rows("g@x.example", false)},
	})
	logins := map[string]bool{"a": true, "off": false, "nohash": false, "suspended": false}
	for _, e := range got {
		if e.CanLogin() != logins[e.Name] {
			t.Errorf("%s: CanLogin = %v", e.Name, e.CanLogin())
		}
	}
	if got[0].Primary != "a@x.example" || len(got[0].Aliases) != 1 || got[0].Aliases[0] != "b@x.example" || got[0].Credential != "h" {
		t.Errorf("entry = %+v", got[0])
	}
}
