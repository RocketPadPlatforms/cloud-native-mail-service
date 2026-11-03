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

package mtasts

import (
	"regexp"
	"testing"
)

func TestPolicyID(t *testing.T) {
	policy := "version: STSv1\nmode: testing\nmx: mail.example.org\nmax_age: 86400\n"

	id := PolicyID(policy)
	if !regexp.MustCompile(`^[a-zA-Z0-9]{1,32}$`).MatchString(id) {
		t.Fatalf("id %q is not 1-32 alphanumeric characters", id)
	}
	if again := PolicyID(policy); again != id {
		t.Errorf("id is not deterministic: %q != %q", again, id)
	}
	if other := PolicyID(policy + " "); other == id {
		t.Errorf("id %q did not change with the policy", id)
	}
}
