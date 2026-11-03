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

package dkim

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// AnnotationRotation is the MailDomain annotation that records the
// rotation progress (ADR 0011, section 7).
const AnnotationRotation = "cnms.rpp.gmbh/rotation"

// stateVersion is the version of the annotation format this code
// writes and reads.
const stateVersion = 1

// RetiringKey is a key that no longer signs and leaves DNS at Until.
type RetiringKey struct {
	Selector string
	Until    time.Time
}

// State is the rotation progress that cannot be derived from the key
// Secrets alone. Pending keys are implicit: a key that is neither
// active nor retiring.
type State struct {
	// Active lists the selectors that sign, at most one per algorithm.
	Active []string
	// Retiring lists the keys still published after a switch.
	Retiring []RetiringKey
	// VerifiedAt records, per pending selector, when its DNS record was
	// first seen verified (the start of the hold period).
	VerifiedAt map[string]time.Time
}

type wireState struct {
	Version    int                  `json:"v"`
	Active     []string             `json:"active,omitempty"`
	Retiring   []wireRetiring       `json:"retiring,omitempty"`
	VerifiedAt map[string]time.Time `json:"verifiedAt,omitempty"`
}

type wireRetiring struct {
	Selector string    `json:"selector"`
	Until    time.Time `json:"until"`
}

// Encode returns the annotation value. It is deterministic: lists are
// sorted, map keys are sorted by encoding/json and times are UTC with
// second precision, so an unchanged state never rewrites the annotation.
func Encode(s State) string {
	w := wireState{Version: stateVersion, Active: slices.Sorted(slices.Values(s.Active))}
	for _, r := range s.Retiring {
		w.Retiring = append(w.Retiring, wireRetiring{r.Selector, wireTime(r.Until)})
	}
	slices.SortFunc(w.Retiring, func(a, b wireRetiring) int { return strings.Compare(a.Selector, b.Selector) })
	if len(s.VerifiedAt) > 0 {
		w.VerifiedAt = make(map[string]time.Time, len(s.VerifiedAt))
		for sel, t := range s.VerifiedAt {
			w.VerifiedAt[sel] = wireTime(t)
		}
	}
	out, err := json.Marshal(w)
	if err != nil { // unreachable: only strings and times
		panic(err)
	}
	return string(out)
}

func wireTime(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// Decode reads an annotation value. An empty value is absent and not a
// problem. A value that cannot be used (corrupt JSON, an unknown
// version) is treated as absent as well, with the reason returned as
// err so the caller can report a warning; the rotation then derives its
// state from the Secrets (ADR 0011, section 8). Unknown fields are
// ignored.
func Decode(value string) (state State, found bool, err error) {
	if strings.TrimSpace(value) == "" {
		return State{}, false, nil
	}
	var w wireState
	if err := json.Unmarshal([]byte(value), &w); err != nil {
		return State{}, false, fmt.Errorf("ignoring corrupt %s annotation: %w", AnnotationRotation, err)
	}
	if w.Version != stateVersion {
		return State{}, false, fmt.Errorf("ignoring %s annotation: unsupported version %d", AnnotationRotation, w.Version)
	}
	state.Active = slices.Sorted(slices.Values(w.Active))
	for _, r := range w.Retiring {
		state.Retiring = append(state.Retiring, RetiringKey(r))
	}
	slices.SortFunc(state.Retiring, func(a, b RetiringKey) int { return strings.Compare(a.Selector, b.Selector) })
	state.VerifiedAt = w.VerifiedAt
	return state, true, nil
}
