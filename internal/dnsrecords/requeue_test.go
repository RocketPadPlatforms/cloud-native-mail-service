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

package dnsrecords

import (
	"testing"
	"time"
)

func TestNextCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
		attempt  int
		want     time.Duration
	}{
		{"verified", true, 0, time.Hour},
		{"verified ignores attempts", true, 9, time.Hour},
		{"first unverified check", false, 0, time.Minute},
		{"retry 2", false, 1, 2 * time.Minute},
		{"retry 3", false, 2, 4 * time.Minute},
		{"retry 4", false, 3, 8 * time.Minute},
		{"capped", false, 4, 10 * time.Minute},
		{"far beyond the cap", false, 1000, 10 * time.Minute},
		{"negative attempt", false, -3, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextCheck(tc.verified, tc.attempt); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
