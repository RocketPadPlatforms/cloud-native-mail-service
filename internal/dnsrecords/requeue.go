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

import "time"

// The check intervals of ADR 0012: the controller watches nothing in
// DNS, so these requeues are the mechanism.
const (
	// UnverifiedMin is the first retry of a record that is not verified
	// (or of a lookup that failed temporarily).
	UnverifiedMin = time.Minute
	// UnverifiedMax is where the backoff stops.
	UnverifiedMax = 10 * time.Minute
	// VerifiedInterval is the drift check once everything is verified.
	VerifiedInterval = time.Hour
	// FailuresBeforeUnknown is the number of consecutive failed lookups
	// after which RecordsVerified becomes Unknown (reason LookupFailing),
	// never False.
	FailuresBeforeUnknown = 3
)

// NextCheck returns the delay before the records are looked up again.
// With every record verified it is VerifiedInterval. Otherwise it starts
// at UnverifiedMin for attempt 0 (the first check that left something
// unverified), doubles with every further attempt and stops at
// UnverifiedMax.
func NextCheck(allVerified bool, attempt int) time.Duration {
	if allVerified {
		return VerifiedInterval
	}
	d := UnverifiedMin
	for range max(attempt, 0) {
		d *= 2
		if d >= UnverifiedMax {
			return UnverifiedMax
		}
	}
	return d
}
