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
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

const (
	day         = 24 * time.Hour
	rsaFirst    = "cnms-20260101"
	rsaSecond   = "cnms-20260131"
	edFirst     = "cnms-ed25519-20260101"
	rsaEarlier  = "cnms-20251101"
	rsaRestored = "cnms-20251201"
	edRestored  = "cnms-ed25519-20251201"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// sim applies plans the way the controller does: create the Secrets,
// delete the Secrets, then record the annotation.
type sim struct {
	t     *testing.T
	keys  []KeyInfo
	state State
	have  bool
	spec  RotationSpec
	dns   map[string]DNSState
	now   time.Time
}

func newSim(t *testing.T, spec RotationSpec) *sim {
	return &sim{t: t, spec: spec, dns: map[string]DNSState{}, now: t0}
}

func (s *sim) input() Input {
	return Input{Keys: s.keys, State: s.state, HaveState: s.have, Spec: s.spec, DNS: s.dns, Now: s.now}
}

func (s *sim) apply(p Plan) {
	for _, c := range p.Create {
		s.keys = append(s.keys, KeyInfo{c.Selector, c.Algorithm, s.now})
	}
	s.keys = slices.DeleteFunc(s.keys, func(k KeyInfo) bool { return slices.Contains(p.Delete, k.Selector) })
	s.state, s.have = p.State, true
}

// step runs one pass, applies it and checks that a second pass at the
// same time has nothing left to do.
func (s *sim) step() Plan {
	s.t.Helper()
	p := Rotate(s.input())
	s.apply(p)
	again := Rotate(s.input())
	if len(again.Create) != 0 || len(again.Delete) != 0 || Encode(again.State) != Encode(s.state) {
		s.t.Fatalf("not idempotent at %s: second pass %+v after state %s", s.now, again, Encode(s.state))
	}
	return p
}

func (s *sim) advance(d time.Duration) { s.now = s.now.Add(d) }

func (s *sim) selectors() []string {
	out := make([]string, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k.Selector)
	}
	slices.Sort(out)
	return out
}

func mustEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func TestBootstrap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ed25519    bool
		wantCreate []NewKey
		wantActive []string
	}{
		{"rsa only", false, []NewKey{{rsaFirst, RSA}}, []string{rsaFirst}},
		{"rsa and ed25519", true, []NewKey{{rsaFirst, RSA}, {edFirst, Ed25519}}, []string{rsaFirst, edFirst}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSim(t, RotationSpec{Ed25519: tc.ed25519, Interval: 30 * day})
			p := s.step()
			mustEqual(t, "create", p.Create, tc.wantCreate)
			mustEqual(t, "active", p.State.Active, tc.wantActive)
			mustEqual(t, "pending", p.Pending, []string(nil))
			mustEqual(t, "next rotation", p.NextRotation, t0.Add(30*day))
		})
	}
}

func TestRotationTimeline(t *testing.T) {
	s := newSim(t, RotationSpec{Interval: 30 * day, Overlap: 5 * day})
	s.step()

	s.advance(30*day - time.Second)
	p := s.step()
	if len(p.Create) != 0 || p.NextWake != t0.Add(30*day) || p.NextRotation != t0.Add(30*day) {
		t.Fatalf("one second before the interval: got %+v", p)
	}

	s.advance(time.Second)
	p = s.step()
	mustEqual(t, "create at the interval", p.Create, []NewKey{{rsaSecond, RSA}})
	mustEqual(t, "pending", p.Pending, []string{rsaSecond})
	mustEqual(t, "active still the old key", p.State.Active, []string{rsaFirst})

	// The record is not published yet: the old key keeps signing, no
	// second key is created, however long it takes.
	s.dns[rsaSecond] = DNSUnverified
	s.advance(3 * day)
	p = s.step()
	if len(p.Create) != 0 || !reflect.DeepEqual(p.State.Active, []string{rsaFirst}) || !reflect.DeepEqual(p.Pending, []string{rsaSecond}) {
		t.Fatalf("stalled rotation: got %+v", p)
	}

	s.dns[rsaSecond] = DNSVerified
	p = s.step()
	mustEqual(t, "hold starts", p.State.VerifiedAt, map[string]time.Time{rsaSecond: s.now})
	mustEqual(t, "wake at the end of the hold", p.NextWake, s.now.Add(HoldPeriod))
	mustEqual(t, "still the old key", p.State.Active, []string{rsaFirst})

	s.advance(HoldPeriod - time.Second)
	if p = s.step(); !reflect.DeepEqual(p.State.Active, []string{rsaFirst}) {
		t.Fatalf("activated before the hold ended: %+v", p)
	}

	s.advance(time.Second)
	switched := s.now
	p = s.step()
	mustEqual(t, "active after the hold", p.State.Active, []string{rsaSecond})
	mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{rsaFirst, switched.Add(5 * day)}})
	mustEqual(t, "pending", p.Pending, []string(nil))
	mustEqual(t, "verifiedAt dropped", p.State.VerifiedAt, map[string]time.Time(nil))
	mustEqual(t, "next rotation", p.NextRotation, t0.Add(30*day).Add(30*day))
	mustEqual(t, "both published", s.selectors(), []string{rsaFirst, rsaSecond})

	s.advance(5*day - time.Second)
	if p = s.step(); len(p.Delete) != 0 {
		t.Fatalf("deleted before the overlap ended: %+v", p)
	}
	s.advance(time.Second)
	p = s.step()
	mustEqual(t, "delete", p.Delete, []string{rsaFirst})
	mustEqual(t, "retiring cleared", p.State.Retiring, []RetiringKey(nil))
	mustEqual(t, "keys", s.selectors(), []string{rsaSecond})
}

func TestHoldIsRestartedByAFailedLookupOnly(t *testing.T) {
	s := newSim(t, RotationSpec{Interval: 30 * day})
	s.step()
	s.advance(30 * day)
	s.step() // second key, pending

	s.dns[rsaSecond] = DNSVerified
	s.step()
	s.advance(10 * time.Minute)
	s.dns[rsaSecond] = DNSUnverified
	if p := s.step(); len(p.State.VerifiedAt) != 0 {
		t.Fatalf("a failed lookup must restart the hold: %+v", p.State)
	}

	s.dns[rsaSecond] = DNSVerified
	s.step()
	verified := s.now
	s.advance(10 * time.Minute)
	s.dns[rsaSecond] = DNSUnknown // SERVFAIL: leave the hold alone
	if p := s.step(); !p.State.VerifiedAt[rsaSecond].Equal(verified) {
		t.Fatalf("an unknown lookup must keep the hold: %+v", p.State)
	}
	s.advance(5 * time.Minute)
	if p := s.step(); !reflect.DeepEqual(p.State.Active, []string{rsaSecond}) {
		t.Fatalf("the hold ended while lookups were failing: %+v", p)
	}
}

func TestNoRotationWithoutInterval(t *testing.T) {
	s := newSim(t, RotationSpec{})
	s.step()
	s.advance(3650 * day)
	p := s.step()
	if len(p.Create) != 0 || len(p.Delete) != 0 || !p.NextRotation.IsZero() || !p.NextWake.IsZero() {
		t.Fatalf("got %+v", p)
	}
}

func TestEnableEd25519Later(t *testing.T) {
	s := newSim(t, RotationSpec{})
	s.step()
	s.advance(2 * day)
	s.spec.Ed25519 = true
	p := s.step()
	wantEd := "cnms-ed25519-20260103"
	mustEqual(t, "create", p.Create, []NewKey{{wantEd, Ed25519}})
	// RSA keeps signing; the new key waits for its record.
	mustEqual(t, "active", p.State.Active, []string{rsaFirst})
	mustEqual(t, "pending", p.Pending, []string{wantEd})

	s.dns[wantEd] = DNSVerified
	s.step()
	s.advance(HoldPeriod)
	p = s.step()
	mustEqual(t, "active", p.State.Active, []string{rsaFirst, wantEd})
	mustEqual(t, "nothing retires", p.State.Retiring, []RetiringKey(nil))
}

func TestDisableEd25519(t *testing.T) {
	s := newSim(t, RotationSpec{Ed25519: true, Overlap: 2 * day})
	s.step()
	s.advance(day)
	s.spec.Ed25519 = false
	p := s.step()
	mustEqual(t, "active", p.State.Active, []string{rsaFirst})
	mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{edFirst, s.now.Add(2 * day)}})
	s.advance(2 * day)
	p = s.step()
	mustEqual(t, "delete", p.Delete, []string{edFirst})

	t.Run("a pending key that never signed is dropped at once", func(t *testing.T) {
		s := newSim(t, RotationSpec{})
		s.step()
		s.spec.Ed25519 = true
		s.step()
		s.spec.Ed25519 = false
		p := s.step()
		mustEqual(t, "delete", p.Delete, []string{edFirst})
		mustEqual(t, "retiring", p.State.Retiring, []RetiringKey(nil))
	})
}

func TestDualAlgorithmsRotateIndependently(t *testing.T) {
	s := newSim(t, RotationSpec{Ed25519: true, Interval: 30 * day, Overlap: day})
	s.step()
	s.advance(30 * day)
	p := s.step()
	mustEqual(t, "create", p.Create, []NewKey{{rsaSecond, RSA}, {"cnms-ed25519-20260131", Ed25519}})
	s.dns[rsaSecond] = DNSVerified // only RSA is published
	s.step()
	s.advance(HoldPeriod)
	p = s.step()
	mustEqual(t, "active", p.State.Active, []string{rsaSecond, edFirst})
	mustEqual(t, "pending", p.Pending, []string{"cnms-ed25519-20260131"})
	mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{rsaFirst, s.now.Add(day)}})
}

func TestRestoreWithoutAnnotation(t *testing.T) {
	keys := func(spec RotationSpec) *sim {
		s := newSim(t, spec)
		s.keys = []KeyInfo{
			{rsaRestored, RSA, t0.Add(-30 * day)},
			{rsaEarlier, RSA, t0.Add(-60 * day)},
			{edRestored, Ed25519, t0.Add(-30 * day)},
		}
		return s
	}

	t.Run("the oldest key per algorithm is active, nothing is regenerated", func(t *testing.T) {
		s := keys(RotationSpec{Ed25519: true, Interval: 90 * day})
		p := s.step()
		mustEqual(t, "create", p.Create, []NewKey(nil))
		mustEqual(t, "delete", p.Delete, []string(nil))
		mustEqual(t, "active", p.State.Active, []string{rsaEarlier, edRestored})
		mustEqual(t, "pending", p.Pending, []string{rsaRestored})
	})

	t.Run("a newer verified key takes over after one hold", func(t *testing.T) {
		s := keys(RotationSpec{Ed25519: true, Interval: 90 * day, Overlap: 5 * day})
		s.dns[rsaRestored] = DNSVerified
		s.step()
		s.advance(HoldPeriod)
		p := s.step()
		mustEqual(t, "active", p.State.Active, []string{rsaRestored, edRestored})
		mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{rsaEarlier, s.now.Add(5 * day)}})
	})

	t.Run("the recorded state wins over the derivation", func(t *testing.T) {
		s := keys(RotationSpec{Ed25519: true, Interval: 90 * day})
		s.state, s.have = State{Active: []string{rsaRestored}}, true
		p := s.step()
		mustEqual(t, "active", p.State.Active, []string{rsaRestored})
		// The older key is not active and not retiring: it never signs
		// again and is retired like any superseded key.
		mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{rsaEarlier, s.now.Add(DefaultOverlap)}})
	})

	t.Run("the domain is bootstrapped when the Secrets are gone", func(t *testing.T) {
		s := newSim(t, RotationSpec{})
		s.state, s.have = State{Active: []string{rsaFirst}, Retiring: []RetiringKey{{"old", t0.Add(day)}}, VerifiedAt: map[string]time.Time{"x": t0}}, true
		p := s.step()
		mustEqual(t, "create", p.Create, []NewKey{{rsaFirst, RSA}})
		mustEqual(t, "active", p.State.Active, []string{rsaFirst})
		mustEqual(t, "stale state dropped", p.State.Retiring, []RetiringKey(nil))
		mustEqual(t, "stale hold dropped", p.State.VerifiedAt, map[string]time.Time(nil))
	})
}

func TestStaleAnnotationNeverLeavesRSAUnsigned(t *testing.T) {
	s := newSim(t, RotationSpec{Interval: 90 * day})
	s.keys = []KeyInfo{{rsaRestored, RSA, t0.Add(-30 * day)}}
	s.state, s.have = State{Active: []string{"gone"}}, true
	p := s.step()
	mustEqual(t, "create", p.Create, []NewKey(nil))
	mustEqual(t, "active at once, without waiting for DNS", p.State.Active, []string{rsaRestored})
}

func TestSelectorCollisionWaitsForTheNextDay(t *testing.T) {
	s := newSim(t, RotationSpec{Interval: 30 * day})
	s.now = time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	// The active key is due for rotation, but a retiring key already
	// holds today's selector.
	s.keys = []KeyInfo{
		{rsaFirst, RSA, s.now.Add(-30 * day)},
		{rsaSecond, RSA, s.now.Add(-time.Hour)},
	}
	s.state, s.have = State{Active: []string{rsaFirst}, Retiring: []RetiringKey{{rsaSecond, s.now.Add(day)}}}, true
	p := s.step()
	if len(p.Create) != 0 {
		t.Fatalf("created under an existing selector: %+v", p.Create)
	}
	midnight := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	mustEqual(t, "wake at UTC midnight", p.NextWake, midnight)
	mustEqual(t, "next rotation", p.NextRotation, midnight)

	s.now = midnight
	p = s.step()
	mustEqual(t, "create the next day", p.Create, []NewKey{{"cnms-20260201", RSA}})
}

func TestClockSkew(t *testing.T) {
	t.Run("a hold recorded in the future restarts from now", func(t *testing.T) {
		s := newSim(t, RotationSpec{Interval: 30 * day})
		s.step()
		s.advance(30 * day)
		s.step()
		s.state.VerifiedAt = map[string]time.Time{rsaSecond: s.now.Add(2 * time.Hour)}
		s.dns[rsaSecond] = DNSVerified
		p := s.step()
		mustEqual(t, "active", p.State.Active, []string{rsaFirst})
		mustEqual(t, "hold restarts now", p.State.VerifiedAt, map[string]time.Time{rsaSecond: s.now})
	})

	t.Run("a retiring end far in the future is capped at the overlap", func(t *testing.T) {
		s := newSim(t, RotationSpec{Overlap: 5 * day})
		s.keys = []KeyInfo{{rsaFirst, RSA, t0.Add(-day)}, {rsaEarlier, RSA, t0.Add(-60 * day)}}
		s.state, s.have = State{Active: []string{rsaFirst}, Retiring: []RetiringKey{{rsaEarlier, t0.Add(400 * day)}}}, true
		p := s.step()
		mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{rsaEarlier, t0.Add(5 * day)}})
	})

	t.Run("a key created in the future delays the rotation", func(t *testing.T) {
		s := newSim(t, RotationSpec{Interval: 30 * day})
		s.keys = []KeyInfo{{rsaFirst, RSA, t0.Add(10 * day)}}
		s.state, s.have = State{Active: []string{rsaFirst}}, true
		p := s.step()
		if len(p.Create) != 0 || !p.NextRotation.Equal(t0.Add(40*day)) {
			t.Fatalf("got %+v", p)
		}
	})

	t.Run("the clock moving backwards creates nothing new", func(t *testing.T) {
		s := newSim(t, RotationSpec{Interval: 30 * day, Overlap: day})
		s.step()
		s.advance(30 * day)
		s.step()
		s.dns[rsaSecond] = DNSVerified
		s.step()
		s.advance(HoldPeriod)
		s.step()
		s.advance(-40 * day)
		p := s.step()
		if len(p.Create) != 0 || len(p.Delete) != 0 {
			t.Fatalf("got %+v", p)
		}
	})
}

func TestSupersededPendingKeysRetire(t *testing.T) {
	s := newSim(t, RotationSpec{Interval: 90 * day, Overlap: 2 * day})
	s.keys = []KeyInfo{
		{rsaEarlier, RSA, t0.Add(-60 * day)},
		{rsaRestored, RSA, t0.Add(-30 * day)},
		{rsaSecond, RSA, t0.Add(-day)},
	}
	s.dns[rsaSecond] = DNSVerified
	s.step()
	s.advance(HoldPeriod)
	p := s.step()
	mustEqual(t, "active", p.State.Active, []string{rsaSecond})
	mustEqual(t, "retiring", p.State.Retiring, []RetiringKey{{rsaEarlier, s.now.Add(2 * day)}, {rsaRestored, s.now.Add(2 * day)}})
	mustEqual(t, "pending", p.Pending, []string(nil))
}

// TestRotationProperties runs two years of rotation against a
// randomized DNS and checks the invariants that make rotation safe.
func TestRotationProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const overlap = 5 * day
	s := newSim(t, RotationSpec{Ed25519: true, Interval: 30 * day, Overlap: overlap})

	published := map[string]time.Time{} // when the record appears in DNS
	verifiedSince := map[string]time.Time{}
	created := map[Algorithm]int{}
	end := t0.Add(2 * 365 * day)

	for s.now.Before(end) {
		// Model DNS: a record is published 0-6h after the key exists and
		// then stays; lookups fail temporarily 5% of the time.
		for _, k := range s.keys {
			if _, ok := published[k.Selector]; !ok {
				published[k.Selector] = k.CreatedAt.Add(time.Duration(rng.IntN(6*60)) * time.Minute)
			}
			switch {
			case rng.IntN(20) == 0:
				s.dns[k.Selector] = DNSUnknown
			case !s.now.Before(published[k.Selector]):
				s.dns[k.Selector] = DNSVerified
				if _, ok := verifiedSince[k.Selector]; !ok {
					verifiedSince[k.Selector] = s.now
				}
			default:
				s.dns[k.Selector] = DNSUnverified
			}
		}

		before := State{Active: slices.Clone(s.state.Active), Retiring: slices.Clone(s.state.Retiring)}
		p := s.step()
		for _, c := range p.Create {
			created[c.Algorithm]++
		}
		for _, sel := range p.State.Active {
			if slices.Contains(before.Active, sel) || len(before.Active) == 0 {
				continue
			}
			if since, ok := verifiedSince[sel]; !ok || s.now.Sub(since) < HoldPeriod {
				t.Fatalf("%s: %s became active before its record was verified for the hold", s.now, sel)
			}
		}
		for _, sel := range p.Delete {
			for _, r := range before.Retiring {
				if r.Selector == sel && r.Until.After(s.now) {
					t.Fatalf("%s: %s deleted before its overlap ended", s.now, sel)
				}
			}
			if slices.Contains(before.Active, sel) {
				t.Fatalf("%s: active key %s deleted", s.now, sel)
			}
		}
		var rsaActive, rsaKeys, edKeys int
		for _, k := range s.keys {
			if slices.Contains(p.State.Active, k.Selector) && k.Algorithm == RSA {
				rsaActive++
			}
			if k.Algorithm == RSA {
				rsaKeys++
			} else {
				edKeys++
			}
		}
		if rsaActive != 1 || rsaKeys > 3 || edKeys > 3 {
			t.Fatalf("%s: rsa active %d, rsa keys %d, ed25519 keys %d: %v", s.now, rsaActive, rsaKeys, edKeys, s.selectors())
		}
		s.advance(time.Duration(5+rng.IntN(55)) * time.Minute)
	}
	// 2 years at 30 days, each switch after a few hours: 24 to 25 keys.
	for _, alg := range []Algorithm{RSA, Ed25519} {
		if created[alg] < 23 || created[alg] > 26 {
			t.Fatalf("%s: created %d keys in two years", alg, created[alg])
		}
	}
}
