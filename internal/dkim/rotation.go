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
	"cmp"
	"slices"
	"strings"
	"time"
)

const (
	// HoldPeriod is how long a pending key's DNS record has to be
	// verified before the key becomes active: it covers the lag between
	// authoritative servers (ADR 0011, section 6).
	HoldPeriod = 15 * time.Minute
	// DefaultOverlap is the overlap when the spec sets none: it covers
	// the Postfix queue lifetime of 5 days.
	DefaultOverlap = 120 * time.Hour
)

// KeyInfo describes a generated key that exists as a Secret.
type KeyInfo struct {
	Selector  string
	Algorithm Algorithm
	// CreatedAt comes from the Secret's creation annotation.
	CreatedAt time.Time
}

// DNSState is what the last lookup of a key's record found.
type DNSState int

const (
	// DNSUnknown is the zero value: no lookup yet, or a temporary error
	// (ADR 0012: the previous value is left alone).
	DNSUnknown DNSState = iota
	// DNSVerified means the record matches the key.
	DNSVerified
	// DNSUnverified means the lookup answered and the record is missing
	// or different.
	DNSUnverified
)

// RotationSpec is the part of the MailDomain spec the rotation reads.
type RotationSpec struct {
	// Prefix is the selector prefix; empty means DefaultPrefix.
	Prefix string
	// Ed25519 enables the second signature.
	Ed25519 bool
	// Interval is the rotation interval; zero disables rotation (keys
	// are still bootstrapped, activated and retired).
	Interval time.Duration
	// Overlap is how long a replaced key stays published; zero means
	// DefaultOverlap.
	Overlap time.Duration
}

// Input is everything the rotation decides on. It is meant for
// generated keys; with bring-your-own keys the spec is the state and
// the function is not used.
type Input struct {
	// Keys are the existing key Secrets of the domain.
	Keys []KeyInfo
	// State is the decoded rotation annotation; HaveState is false when
	// it is absent, which derives the state from Keys (ADR 0011,
	// section 8).
	State     State
	HaveState bool
	Spec      RotationSpec
	// DNS maps a selector to its lookup result; a missing entry is
	// DNSUnknown.
	DNS map[string]DNSState
	Now time.Time
}

// NewKey is a key to create, with CreatedAt = Input.Now.
type NewKey struct {
	Selector  string
	Algorithm Algorithm
}

// Plan is the outcome of one rotation pass. Applying it (creating the
// keys, deleting the Secrets, writing State) and running Rotate again
// with the same Now yields an empty change.
type Plan struct {
	// Create lists the keys to generate. Their selectors are in
	// State.Active when they start active, in Pending otherwise.
	Create []NewKey
	// Delete lists the selectors whose Secret is deleted and whose
	// record leaves the required set.
	Delete []string
	// State is the desired annotation content.
	State State
	// Pending lists the selectors that are published and wait for their
	// record to be held verified. A non-empty list is DKIMRotation=False
	// with reason AwaitingDNS.
	Pending []string
	// NextRotation is when the next key is created; zero without rotation
	// or while a key is pending.
	NextRotation time.Time
	// NextWake is the earliest time the plan changes without any new
	// input (a hold ends, an overlap ends, a key is due); zero if none.
	NextWake time.Time
}

// Rotate computes the next rotation step as a pure function (ADR 0011,
// section 6):
//
//   - bootstrap: a domain without keys gets a key per enabled algorithm,
//     active at once;
//   - start: a key is created, pending, when the active key of an
//     algorithm is older than the interval and none is pending;
//   - activate: a pending key whose record has been verified for
//     HoldPeriod becomes active, the previous key retires for the
//     overlap;
//   - retire: at its until a retiring key is deleted.
//
// Selector collision: a key is never created under a selector that
// already exists (a restored Secret is adopted, not replaced). If
// today's selector is taken, the creation waits for the next UTC day
// (NextWake); changing selectorPrefix avoids the wait.
func Rotate(in Input) Plan {
	r := newRotation(in)
	for _, alg := range []Algorithm{RSA, Ed25519} {
		r.algorithm(alg)
	}
	return r.plan()
}

type rotation struct {
	in       Input
	now      time.Time
	overlap  time.Duration
	keys     []KeyInfo // sorted by creation time, then selector
	exist    map[string]KeyInfo
	active   map[Algorithm]KeyInfo
	retiring map[string]time.Time
	verified map[string]time.Time // first verified, pending keys only
	deleted  map[string]bool
	created  []NewKey
	pending  []string // created this pass
	next     time.Time
	nextRot  time.Time
}

func newRotation(in Input) *rotation {
	r := &rotation{
		in:       in,
		now:      in.Now,
		overlap:  cmp.Or(in.Spec.Overlap, DefaultOverlap),
		keys:     slices.Clone(in.Keys),
		exist:    map[string]KeyInfo{},
		active:   map[Algorithm]KeyInfo{},
		retiring: map[string]time.Time{},
		verified: map[string]time.Time{},
		deleted:  map[string]bool{},
	}
	slices.SortFunc(r.keys, func(a, b KeyInfo) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.Selector, b.Selector))
	})
	for _, k := range r.keys {
		r.exist[k.Selector] = k
	}
	if !in.HaveState {
		// Restore without the annotation: per algorithm the oldest key is
		// active, the rest pending; nothing is regenerated.
		for _, k := range r.keys {
			if _, ok := r.active[k.Algorithm]; !ok {
				r.active[k.Algorithm] = k
			}
		}
		return r
	}
	for _, sel := range in.State.Active {
		if k, ok := r.exist[sel]; ok {
			if cur, has := r.active[k.Algorithm]; !has || k.CreatedAt.After(cur.CreatedAt) {
				r.active[k.Algorithm] = k
			}
		}
	}
	for _, e := range in.State.Retiring {
		if _, ok := r.exist[e.Selector]; ok && !r.isActive(e.Selector) {
			// A clock that moved backwards must not stretch the overlap.
			r.retiring[e.Selector] = minTime(e.Until, r.now.Add(r.overlap))
		}
	}
	for sel, t := range in.State.VerifiedAt {
		if _, ok := r.exist[sel]; ok {
			r.verified[sel] = minTime(t, r.now)
		}
	}
	return r
}

func (r *rotation) isActive(sel string) bool {
	k, ok := r.exist[sel]
	return ok && r.active[k.Algorithm].Selector == sel
}

// pendingKeys are the existing keys of alg that are neither active,
// retiring nor deleted, oldest first.
func (r *rotation) pendingKeys(alg Algorithm) []KeyInfo {
	var out []KeyInfo
	for _, k := range r.keys {
		if _, retiring := r.retiring[k.Selector]; k.Algorithm == alg && !retiring && !r.deleted[k.Selector] && !r.isActive(k.Selector) {
			out = append(out, k)
		}
	}
	return out
}

func (r *rotation) retire(sel string) {
	r.retiring[sel] = r.now.Add(r.overlap)
	delete(r.verified, sel)
}

func (r *rotation) algorithm(alg Algorithm) {
	if alg == Ed25519 && !r.in.Spec.Ed25519 {
		// Switched off: the signing key stays published for the overlap,
		// a key that never signed is dropped.
		if cur, ok := r.active[alg]; ok {
			delete(r.active, alg)
			r.retire(cur.Selector)
		}
		for _, k := range r.pendingKeys(alg) {
			r.deleted[k.Selector] = true
			delete(r.verified, k.Selector)
		}
		r.expire(alg)
		return
	}
	r.expire(alg)
	r.trackDNS(alg)
	r.activate(alg)
	cur, hasActive := r.active[alg]
	if hasActive {
		for _, k := range r.pendingKeys(alg) {
			if k.CreatedAt.Before(cur.CreatedAt) { // superseded, never to be activated
				r.retire(k.Selector)
			}
		}
	}
	r.create(alg, cur, hasActive)
}

// expire deletes the retiring keys of alg whose overlap is over.
func (r *rotation) expire(alg Algorithm) {
	for sel, until := range r.retiring {
		if r.exist[sel].Algorithm == alg && !until.After(r.now) {
			delete(r.retiring, sel)
			r.deleted[sel] = true
		}
	}
}

// trackDNS keeps the first-verified time of the pending keys: a failed
// lookup restarts the hold, an unknown result leaves it alone.
func (r *rotation) trackDNS(alg Algorithm) {
	for _, k := range r.pendingKeys(alg) {
		switch r.in.DNS[k.Selector] {
		case DNSVerified:
			if _, ok := r.verified[k.Selector]; !ok {
				r.verified[k.Selector] = r.now
			}
		case DNSUnverified:
			delete(r.verified, k.Selector)
		case DNSUnknown:
		}
	}
}

// activate switches to the newest pending key held verified for
// HoldPeriod. RSA is mandatory: without an active RSA key the oldest
// pending one signs at once, because waiting would defer all mail.
func (r *rotation) activate(alg Algorithm) {
	pending := r.pendingKeys(alg)
	var next *KeyInfo
	for i, k := range pending {
		if t, ok := r.verified[k.Selector]; ok && r.now.Sub(t) >= HoldPeriod {
			next = &pending[i]
		}
	}
	cur, hasActive := r.active[alg]
	if next == nil && !hasActive && alg == RSA && len(pending) > 0 {
		next = &pending[0]
	}
	if next == nil {
		return
	}
	if hasActive {
		r.retire(cur.Selector)
	}
	r.active[alg] = *next
	delete(r.verified, next.Selector)
}

func (r *rotation) create(alg Algorithm, cur KeyInfo, hasActive bool) {
	pending := r.pendingKeys(alg)
	switch {
	case len(r.in.Keys) == 0:
		r.add(alg, true) // bootstrap
	case !hasActive && len(pending) == 0:
		// No usable key: RSA signs at once, a newly enabled Ed25519
		// waits for its record like any pending key.
		r.add(alg, alg == RSA)
	case hasActive && len(pending) == 0 && r.in.Spec.Interval > 0:
		due := cur.CreatedAt.Add(r.in.Spec.Interval)
		if r.now.Before(due) {
			r.wake(due)
			r.nextRotation(due)
			return
		}
		r.add(alg, false)
	}
}

// add creates a key unless the selector of today is taken.
func (r *rotation) add(alg Algorithm, active bool) {
	sel := Selector(r.in.Spec.Prefix, alg, r.now)
	if _, taken := r.exist[sel]; taken {
		midnight := r.now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
		r.wake(midnight)
		r.nextRotation(midnight)
		return
	}
	r.created = append(r.created, NewKey{sel, alg})
	if active {
		r.active[alg] = KeyInfo{sel, alg, r.now}
		if r.in.Spec.Interval > 0 {
			r.wake(r.now.Add(r.in.Spec.Interval))
			r.nextRotation(r.now.Add(r.in.Spec.Interval))
		}
	} else {
		r.pending = append(r.pending, sel)
	}
}

func (r *rotation) wake(t time.Time) {
	if !t.After(r.now) {
		return
	}
	if r.next.IsZero() || t.Before(r.next) {
		r.next = t
	}
}

func (r *rotation) nextRotation(t time.Time) {
	if r.nextRot.IsZero() || t.Before(r.nextRot) {
		r.nextRot = t
	}
}

func (r *rotation) plan() Plan {
	p := Plan{Create: r.created, NextRotation: r.nextRot}
	for _, alg := range []Algorithm{RSA, Ed25519} {
		if k, ok := r.active[alg]; ok {
			p.State.Active = append(p.State.Active, k.Selector)
		}
		for _, k := range r.pendingKeys(alg) {
			p.Pending = append(p.Pending, k.Selector)
		}
	}
	p.Pending = append(p.Pending, r.pending...)
	for sel, until := range r.retiring {
		p.State.Retiring = append(p.State.Retiring, RetiringKey{sel, until})
		r.wake(until)
	}
	slices.SortFunc(p.State.Retiring, func(a, b RetiringKey) int { return strings.Compare(a.Selector, b.Selector) })
	for _, sel := range p.Pending {
		if t, ok := r.verified[sel]; ok {
			if p.State.VerifiedAt == nil {
				p.State.VerifiedAt = map[string]time.Time{}
			}
			p.State.VerifiedAt[sel] = t
			r.wake(t.Add(HoldPeriod))
		}
	}
	for sel := range r.deleted {
		p.Delete = append(p.Delete, sel)
	}
	slices.Sort(p.Delete)
	p.NextWake = r.next
	return p
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
