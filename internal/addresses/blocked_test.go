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

package addresses

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

const (
	hash1 = "{ARGON2ID}one"
	hash2 = "{ARGON2ID}two"
)

func entry(name, credential string, aliases ...string) Entry {
	return Entry{Name: name, Credential: credential, Primary: name + "@a.example", Aliases: aliases}
}

func names(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func kinds(cs []Change) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name+":"+string(c.Kind)+":"+c.Address)
	}
	return out
}

func disabled(e Entry) Entry { e.Disabled = true; return e }

func suspended(e Entry) Entry { e.Suspended = true; return e }

func withPrimary(e Entry, p string) Entry { e.Primary = p; return e }

// What a blocked revision still applies and what it holds back (ADR 0015,
// section 4.7).
func TestSettleBlocked(t *testing.T) {
	prev := []Entry{
		entry(alice, hash1, "s@b.example"),
		entry(bob, hash1),
		entry(carol, hash1),
		disabled(entry("dave", hash1)),
		entry("erin", hash1),
		entry("fred", hash1),
	}
	next := []Entry{
		entry(alice, hash2, "s@b.example"), // password changed
		disabled(entry(bob, hash1)),        // disabled
		// carol is gone
		entry("dave", hash1),                             // enabled again
		suspended(entry("erin", hash1)),                  // primary domain revoked
		withPrimary(entry("fred", hash1), "f@b.example"), // new primary address
		entry("gina", hash1),                             // new account
		entry("hank", hash1, "h2@b.example"),             // new account with an alias
	}
	s := Settle(prev, next, true)

	// Postfix tables, Rspamd data and the userdb: the running revision.
	if !reflect.DeepEqual(s.Maps, sortedEntries(prev)) || !reflect.DeepEqual(s.Mailboxes, sortedEntries(prev)) {
		t.Errorf("maps and mailboxes are not the running revision:\n%v\n%v", names(s.Maps), names(s.Mailboxes))
	}
	// The passdb: lines already in it, the new password, none for an
	// account that is removed, disabled or suspended, and none added.
	if got := names(s.Logins); !slices.Equal(got, []string{alice, "fred"}) {
		t.Errorf("logins = %v, want alice and fred", got)
	}
	if s.Logins[0].Credential != hash2 {
		t.Errorf("alice keeps the old password: %q", s.Logins[0].Credential)
	}
	if s.Logins[1].Primary != "fred@a.example" {
		t.Errorf("fred's login moved to a new address: %q", s.Logins[1].Primary)
	}
	wantApplied := []string{
		"alice:PasswordChanged:", "bob:LoginDisabled:", "carol:UserRemoved:", "erin:LoginDisabled:",
	}
	if got := kinds(s.Applied); !slices.Equal(got, wantApplied) {
		t.Errorf("applied = %v, want %v", got, wantApplied)
	}
	wantHeld := []string{
		"dave:LoginEnabled:", "fred:PrimaryChanged:f@b.example",
		"gina:UserAdded:", "hank:UserAdded:",
	}
	if got := kinds(s.Held); !slices.Equal(got, wantHeld) {
		t.Errorf("held = %v, want %v", got, wantHeld)
	}
}

// An alias that is added or removed waits: the frozen valias and
// sender_login of the running revision still list it.
func TestSettleBlockedHoldsAliasChanges(t *testing.T) {
	prev := []Entry{entry(alice, hash1, "old@b.example", "keep@b.example")}
	next := []Entry{entry(alice, hash1, "keep@b.example", "new@b.example")}
	s := Settle(prev, next, true)
	if got, want := kinds(s.Held), []string{"alice:AliasAdded:new@b.example", "alice:AliasRemoved:old@b.example"}; !slices.Equal(got, want) {
		t.Errorf("held = %v, want %v", got, want)
	}
	if len(s.Applied) != 0 {
		t.Errorf("applied = %v", kinds(s.Applied))
	}
	if !reflect.DeepEqual(s.Maps, prev) {
		t.Errorf("maps changed: %+v", s.Maps)
	}
	if len(s.Logins) != 1 || !slices.Equal(s.Logins[0].Aliases, prev[0].Aliases) {
		t.Errorf("logins = %+v", s.Logins)
	}
}

// Not blocked, the desired accounts are the revision, and every change
// applies.
func TestSettleNotBlocked(t *testing.T) {
	prev := []Entry{entry(alice, hash1), entry(bob, hash1)}
	next := []Entry{entry(alice, hash2, "s@b.example"), disabled(entry(bob, hash1)), entry(carol, hash1)}
	s := Settle(prev, next, false)
	if !reflect.DeepEqual(s.Maps, sortedEntries(next)) || !reflect.DeepEqual(s.Mailboxes, sortedEntries(next)) {
		t.Error("maps and mailboxes are not the desired accounts")
	}
	if got := names(s.Logins); !slices.Equal(got, []string{alice, carol}) {
		t.Errorf("logins = %v, want alice and carol (bob is disabled)", got)
	}
	if len(s.Held) != 0 {
		t.Errorf("held = %v", kinds(s.Held))
	}
	want := []string{"alice:AliasAdded:s@b.example", "alice:PasswordChanged:", "bob:LoginDisabled:", "carol:UserAdded:"}
	if got := kinds(s.Applied); !slices.Equal(got, want) {
		t.Errorf("applied = %v, want %v", got, want)
	}
}

func TestSettleEdges(t *testing.T) {
	t.Run("nothing blocked, nothing changed", func(t *testing.T) {
		s := Settle(nil, nil, true)
		if len(s.Maps)+len(s.Mailboxes)+len(s.Logins)+len(s.Applied)+len(s.Held) != 0 {
			t.Errorf("Settle(nil, nil) = %+v", s)
		}
	})
	t.Run("a first revision that is blocked adds nothing", func(t *testing.T) {
		s := Settle(nil, []Entry{entry(alice, hash1)}, true)
		if len(s.Logins) != 0 || len(s.Maps) != 0 || len(s.Held) != 1 {
			t.Errorf("Settle() = %+v", s)
		}
	})
	t.Run("the domain revocation is not a blocked revision", func(t *testing.T) {
		// Fail closed: no domains, no users, and no limit applies.
		s := Settle([]Entry{entry(alice, hash1)}, nil, false)
		if len(s.Maps)+len(s.Mailboxes)+len(s.Logins) != 0 || kinds(s.Applied)[0] != "alice:UserRemoved:" {
			t.Errorf("Settle() = %+v", s)
		}
	})
	t.Run("a password change of an account that cannot log in has no line to update", func(t *testing.T) {
		s := Settle([]Entry{disabled(entry(alice, hash1))}, []Entry{disabled(entry(alice, hash2))}, true)
		if len(s.Logins) != 0 || len(s.Applied) != 0 || len(s.Held) != 0 {
			t.Errorf("Settle() = %+v", s)
		}
	})
	t.Run("a login and a password change at once", func(t *testing.T) {
		s := Settle([]Entry{disabled(entry(alice, hash1))}, []Entry{entry(alice, hash2)}, true)
		if len(s.Logins) != 0 || kinds(s.Held)[0] != "alice:LoginEnabled:" {
			t.Errorf("Settle() = %+v", s)
		}
	})
}

// The order of the input does not change the settlement.
func TestSettleIsDeterministic(t *testing.T) {
	prev := []Entry{entry(alice, hash1, "a@b.example", "b@b.example"), entry(bob, hash1), entry(carol, hash1), entry("dave", hash1)}
	next := []Entry{entry(alice, hash2, "b@b.example", "c@b.example"), disabled(entry(bob, hash1)), entry("erin", hash1), entry("dave", hash1)}
	for _, blocked := range []bool{true, false} {
		want := Settle(prev, next, blocked)
		for seed := range uint64(10) {
			r := rand.New(rand.NewPCG(seed, 3))
			p, n := slices.Clone(prev), slices.Clone(next)
			r.Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
			r.Shuffle(len(n), func(i, j int) { n[i], n[j] = n[j], n[i] })
			if got := Settle(p, n, blocked); !reflect.DeepEqual(got, want) {
				t.Fatalf("blocked=%v seed %d: %+v != %+v", blocked, seed, got, want)
			}
		}
	}
}
