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

// Package mtasts holds the pure parts of the MTA-STS (RFC 8461)
// record handling.
package mtasts

import (
	"crypto/sha256"
	"encoding/hex"
)

// policyIDLength is the length of a policy id: 32 characters is the
// maximum RFC 8461 allows for the id of the _mta-sts TXT record.
const policyIDLength = 32

// PolicyID returns the policy id for the exact policy text: the first
// 32 hexadecimal characters of its SHA-256. The id depends only on the
// content, so it changes exactly when the policy changes and is
// re-derived after a restore without status.
func PolicyID(policy string) string {
	sum := sha256.Sum256([]byte(policy))
	return hex.EncodeToString(sum[:])[:policyIDLength]
}
