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
	"strconv"
	"testing"
)

// The domain objects of the fixtures, and the names of the accounts.
const (
	refA      = "dom-a"    // a.example, ok
	refB      = "dom-b"    // b.example, ok
	refRev    = "dom-rev"  // rev.example, revoked
	refOther  = "dom-oth"  // oth.example, served by another server
	refNone   = "dom-none" // no MailDomain
	alice     = "alice"
	bob       = "bob"
	carol     = "carol"
	sales     = "sales"
	addrAlice = "alice@a.example"
	addrBob   = "bob@a.example"
	addrSales = "sales@b.example"
)

func domains() map[string]Domain {
	return map[string]Domain{
		refA:     {"a.example", DomainOK},
		refB:     {"b.example", DomainOK},
		refRev:   {"rev.example", DomainNotAuthorized},
		refOther: {"oth.example", DomainNotServed},
	}
}

func addr(local, ref string) Address { return Address{local, ref} }

func user(name, ref string, aliases ...Address) User {
	return User{Name: name, Primary: addr(name, ref), Aliases: aliases}
}

func input(users []User, senders ...Sender) Input {
	return Input{Users: users, Senders: senders, Domains: domains()}
}

func sender(owner, local, ref string) Sender { return Sender{owner, addr(local, ref)} }

// shuffled returns a copy of in with every list in another order.
func shuffled(in Input, r *rand.Rand) Input {
	out := Input{Domains: in.Domains}
	for _, u := range in.Users {
		u.Aliases = slices.Clone(u.Aliases)
		r.Shuffle(len(u.Aliases), func(i, j int) { u.Aliases[i], u.Aliases[j] = u.Aliases[j], u.Aliases[i] })
		out.Users = append(out.Users, u)
	}
	out.Senders = slices.Clone(in.Senders)
	r.Shuffle(len(out.Users), func(i, j int) { out.Users[i], out.Users[j] = out.Users[j], out.Users[i] })
	r.Shuffle(len(out.Senders), func(i, j int) { out.Senders[i], out.Senders[j] = out.Senders[j], out.Senders[i] })
	return out
}

// decide runs Decide and requires the same answer for the input in
// several other orders, and that the input is left alone.
func decide(t *testing.T, in Input) Result {
	t.Helper()
	before := deepCopy(in)
	res := Decide(in)
	if !reflect.DeepEqual(before, in) {
		t.Fatal("Decide changed its input")
	}
	for seed := range uint64(8) {
		other := Decide(shuffled(in, rand.New(rand.NewPCG(seed, 7))))
		if !reflect.DeepEqual(res, other) {
			t.Fatalf("the verdict depends on the order of the input (seed %d):\n%+v\n%+v", seed, res, other)
		}
	}
	return res
}

func deepCopy(in Input) Input {
	out := Input{Domains: in.Domains, Senders: slices.Clone(in.Senders)}
	for _, u := range in.Users {
		u.Aliases = slices.Clone(u.Aliases)
		out.Users = append(out.Users, u)
	}
	return out
}

// row finds the row of an address of an account.
func row(t *testing.T, res Result, name, address string) Row {
	t.Helper()
	for _, u := range res.Users {
		if u.Name != name {
			continue
		}
		for _, r := range u.Rows {
			if r.Address == address {
				return r
			}
		}
	}
	t.Fatalf("no row %s for %s in %+v", address, name, res)
	return Row{}
}

func account(t *testing.T, res Result, name string) UserResult {
	t.Helper()
	for _, u := range res.Users {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no account %s", name)
	return UserResult{}
}

func senderRow(t *testing.T, res Result, owner string) Row {
	t.Helper()
	for _, s := range res.Senders {
		if s.Owner == owner {
			return s.Row
		}
	}
	t.Fatalf("no sender of %s", owner)
	return Row{}
}

func wantRow(t *testing.T, got Row, served bool, reason string, deferred bool) {
	t.Helper()
	if got.Served != served || got.Reason != reason || got.Deferred != deferred {
		t.Errorf("row %s = served %v, reason %s, deferred %v; want %v, %s, %v (%s)",
			got.Address, got.Served, got.Reason, got.Deferred, served, reason, deferred, got.Detail)
	}
}

func TestEmptyInput(t *testing.T) {
	if res := decide(t, Input{}); !reflect.DeepEqual(res, Result{}) {
		t.Errorf("Decide(empty) = %+v", res)
	}
	if err := CheckLimits(Input{}); err != nil {
		t.Errorf("CheckLimits(empty) = %v", err)
	}
}

// Aliases in the domains of one server, in any case, all served; the rows
// are the primary first, then the aliases by address.
func TestAliasesAcrossDomains(t *testing.T) {
	res := decide(t, input([]User{user(alice, refA, addr("Sales", refB), addr(alice, refB), addr("zed", refA))}))
	a := account(t, res, alice)
	got := make([]string, 0, len(a.Rows))
	for _, r := range a.Rows {
		got = append(got, r.Address)
		wantRow(t, r, true, ReasonServed, false)
		if !r.MaySend {
			t.Errorf("%s may not send", r.Address)
		}
	}
	if want := []string{addrAlice, "alice@b.example", addrSales, "zed@a.example"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if a.Rows[0].Kind != KindPrimary || a.Rows[1].Kind != KindAlias || a.Suspended || a.PartiallyServed() {
		t.Errorf("unexpected account: %+v", a)
	}
	if len(res.Conflicted) != 0 || len(res.Deferred) != 0 {
		t.Errorf("conflicted %v, deferred %v", res.Conflicted, res.Deferred)
	}
	if want := []string{"alice@b.example", addrSales, "zed@a.example"}; !slices.Equal(a.Aliases(), want) {
		t.Errorf("aliases = %v, want %v", a.Aliases(), want)
	}
}

// A primary address always wins: an alias that names it loses, and the
// owner of the primary is untouched (ADR 0015, section 4.2, point 2).
func TestAliasEqualToAnotherPrimaryLoses(t *testing.T) {
	res := decide(t, input([]User{
		user(alice, refA),
		user(bob, refA, addr(alice, refA), addr(sales, refB)),
	}))
	wantRow(t, row(t, res, alice, addrAlice), true, ReasonServed, false)
	loser := row(t, res, bob, addrAlice)
	wantRow(t, loser, false, ReasonAddressConflict, false)
	if loser.Detail == "" {
		t.Error("a conflict has no message")
	}
	wantRow(t, row(t, res, bob, addrSales), true, ReasonServed, false)
	if !account(t, res, bob).PartiallyServed() || account(t, res, alice).PartiallyServed() {
		t.Error("only the loser is partially served")
	}
	// The primary serves it: nothing is conflicted or deferred, and the
	// loser no longer claims it.
	if len(res.Conflicted) != 0 || len(res.Deferred) != 0 {
		t.Errorf("conflicted %v, deferred %v", res.Conflicted, res.Deferred)
	}
	if want := []string{addrSales}; !slices.Equal(account(t, res, bob).Aliases(), want) {
		t.Errorf("aliases of bob = %v, want %v", account(t, res, bob).Aliases(), want)
	}
}

// Two aliases of one rank on one address: both lose, nobody has it, and it
// answers a temporary failure. The other addresses of both are served.
func TestEqualAliasesAcrossUsersAllLose(t *testing.T) {
	res := decide(t, input([]User{
		user(alice, refA, addr("Team", refB), addr("only-alice", refB)),
		user(bob, refA, addr("team", refB)),
		user(carol, refA, addr("TEAM", refB)),
	}))
	for _, name := range []string{alice, bob, carol} {
		wantRow(t, row(t, res, name, "team@b.example"), false, ReasonAddressConflict, true)
		wantRow(t, row(t, res, name, name+"@a.example"), true, ReasonServed, false)
	}
	wantRow(t, row(t, res, alice, "only-alice@b.example"), true, ReasonServed, false)
	if want := []string{"team@b.example"}; !slices.Equal(res.Conflicted, want) || !slices.Equal(res.Deferred, want) {
		t.Errorf("conflicted %v, deferred %v, want %v", res.Conflicted, res.Deferred, want)
	}
	if account(t, res, alice).Suspended {
		t.Error("a conflict on one address suspends the account")
	}
}

// The ranks of ADR 0015, section 4.2: a primary beats an alias, an alias
// beats a sender, claimants of one rank all lose.
func TestRankMixes(t *testing.T) {
	const svc1, svc2, svc3 = "ns1/app", "ns2/app", "ns3/app"
	tests := []struct {
		name string
		in   Input
		// the expectations
		check func(t *testing.T, res Result)
	}{{
		name: "alias beats sender",
		in:   input([]User{user(alice, refA, addr(sales, refB))}, sender(svc1, sales, refB)),
		check: func(t *testing.T, res Result) {
			wantRow(t, row(t, res, alice, addrSales), true, ReasonServed, false)
			wantRow(t, senderRow(t, res, svc1), false, ReasonSenderConflict, false)
			if len(res.Conflicted) != 0 {
				t.Errorf("conflicted = %v", res.Conflicted)
			}
		},
	}, {
		name: "primary beats sender",
		in:   input([]User{user(alice, refA)}, sender(svc1, alice, refA)),
		check: func(t *testing.T, res Result) {
			wantRow(t, row(t, res, alice, addrAlice), true, ReasonServed, false)
			wantRow(t, senderRow(t, res, svc1), false, ReasonSenderConflict, false)
		},
	}, {
		name: "a sender alone has the address",
		in:   input([]User{user(alice, refA)}, sender(svc1, "noreply", refA)),
		check: func(t *testing.T, res Result) {
			wantRow(t, senderRow(t, res, svc1), true, ReasonServed, false)
			if !senderRow(t, res, svc1).MaySend {
				t.Error("the sender may not send")
			}
		},
	}, {
		name: "two senders of one rank both lose",
		in:   input(nil, sender(svc1, "noreply", refA), sender(svc2, "NoReply", refA)),
		check: func(t *testing.T, res Result) {
			wantRow(t, senderRow(t, res, svc1), false, ReasonSenderConflict, true)
			wantRow(t, senderRow(t, res, svc2), false, ReasonSenderConflict, true)
			if want := []string{"noreply@a.example"}; !slices.Equal(res.Conflicted, want) {
				t.Errorf("conflicted = %v", res.Conflicted)
			}
		},
	}, {
		name: "two senders of one owner are not a conflict",
		in:   input(nil, sender(svc1, "noreply", refA), sender(svc1, "noreply", refA)),
		check: func(t *testing.T, res Result) {
			wantRow(t, senderRow(t, res, svc1), true, ReasonServed, false)
			if len(res.Conflicted) != 0 {
				t.Errorf("conflicted = %v", res.Conflicted)
			}
		},
	}, {
		name: "two aliases and a sender: all three lose",
		in: input([]User{user(alice, refA, addr(sales, refB)), user(bob, refA, addr(sales, refB))},
			sender(svc3, sales, refB)),
		check: func(t *testing.T, res Result) {
			wantRow(t, row(t, res, alice, addrSales), false, ReasonAddressConflict, true)
			wantRow(t, row(t, res, bob, addrSales), false, ReasonAddressConflict, true)
			wantRow(t, senderRow(t, res, svc3), false, ReasonSenderConflict, true)
		},
	}, {
		name: "a sender cannot take a primary from one of two aliases",
		in: input([]User{user(alice, refA), user(bob, refA, addr(alice, refA)), user(carol, refA, addr(alice, refA))},
			sender(svc1, alice, refA)),
		check: func(t *testing.T, res Result) {
			wantRow(t, row(t, res, alice, addrAlice), true, ReasonServed, false)
			wantRow(t, senderRow(t, res, svc1), false, ReasonSenderConflict, false)
			if len(res.Conflicted) != 0 || len(res.Deferred) != 0 {
				t.Errorf("conflicted %v, deferred %v", res.Conflicted, res.Deferred)
			}
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, decide(t, tt.in)) })
	}
}

// One account that claims an address twice is no conflict (the CEL rules
// refuse it, the function does not depend on them).
func TestSameAccountTwice(t *testing.T) {
	res := decide(t, input([]User{
		user(alice, refA, addr(alice, refA), addr("ALICE", refA), addr(sales, refB), addr("Sales", refB)),
	}))
	a := account(t, res, alice)
	if len(a.Rows) != 2 {
		t.Fatalf("rows = %+v, want the primary and one alias", a.Rows)
	}
	wantRow(t, a.Rows[0], true, ReasonServed, false)
	if a.Rows[0].Kind != KindPrimary {
		t.Error("the primary row is not the primary")
	}
	wantRow(t, a.Rows[1], true, ReasonServed, false)
	if len(res.Conflicted) != 0 {
		t.Errorf("conflicted = %v", res.Conflicted)
	}
}

// A domain that fails is a reason of the address, not of the account:
// the other addresses stay served (fail closed per address).
func TestRevokedAliasDomain(t *testing.T) {
	res := decide(t, input([]User{user(alice, refA,
		addr("one", refRev), addr("two", refOther), addr("three", refNone), addr("four", refB))}))
	wantRow(t, row(t, res, alice, "one@rev.example"), false, ReasonAliasDomainNotAuthorized, false)
	wantRow(t, row(t, res, alice, "two@oth.example"), false, ReasonAliasDomainNotServed, false)
	wantRow(t, row(t, res, alice, "three@"+refNone), false, ReasonAliasDomainNotFound, false)
	wantRow(t, row(t, res, alice, "four@b.example"), true, ReasonServed, false)
	wantRow(t, row(t, res, alice, addrAlice), true, ReasonServed, false)
	if account(t, res, alice).Suspended || len(res.Deferred) != 0 {
		t.Error("an alias domain suspends the account or defers mail")
	}
	if want := []string{"four@b.example"}; !slices.Equal(account(t, res, alice).Aliases(), want) {
		t.Errorf("aliases = %v, want %v: an alias in a domain that fails is not the renderer's", account(t, res, alice).Aliases(), want)
	}
}

// A claim in a domain that fails takes part in no ranking: it cannot take
// an address from anyone, and it conflicts with no one.
func TestClaimInRevokedDomainShadowsNothing(t *testing.T) {
	res := decide(t, input([]User{
		user(alice, refA, addr("x", refRev), addr("y", refB)),
		user(bob, refA, addr("x", refRev), addr("y", refB)),
	}))
	wantRow(t, row(t, res, alice, "x@rev.example"), false, ReasonAliasDomainNotAuthorized, false)
	wantRow(t, row(t, res, bob, "x@rev.example"), false, ReasonAliasDomainNotAuthorized, false)
	wantRow(t, row(t, res, alice, "y@b.example"), false, ReasonAddressConflict, true)
	if want := []string{"y@b.example"}; !slices.Equal(res.Conflicted, want) {
		t.Errorf("conflicted = %v, want %v", res.Conflicted, want)
	}
}

// ADR 0015, section 4.6: the account is suspended as a whole, its aliases in
// a served domain are deferred for the cause, those in a domain that fails
// keep their own reason.
func TestRevokedPrimaryDomain(t *testing.T) {
	in := input([]User{
		user(carol, refRev, addr(carol, refA), addr("c2", refB), addr("c3", refOther)),
		user(alice, refA),
	})
	res := decide(t, in)
	c := account(t, res, carol)
	if !c.Suspended || c.Reason != ReasonDomainNotAuthorized {
		t.Fatalf("carol = %+v, want suspended with DomainNotAuthorized", c)
	}
	wantRow(t, row(t, res, carol, "carol@rev.example"), false, ReasonDomainNotAuthorized, false)
	wantRow(t, row(t, res, carol, "carol@a.example"), false, ReasonDomainNotAuthorized, true)
	wantRow(t, row(t, res, carol, "c2@b.example"), false, ReasonDomainNotAuthorized, true)
	wantRow(t, row(t, res, carol, "c3@oth.example"), false, ReasonAliasDomainNotServed, false)
	for _, r := range c.Rows {
		if r.MaySend {
			t.Errorf("%s of a suspended account may send", r.Address)
		}
	}
	if want := []string{"c2@b.example", "carol@a.example"}; !slices.Equal(res.Deferred, want) {
		t.Errorf("deferred = %v, want %v", res.Deferred, want)
	}
	if len(res.Conflicted) != 0 {
		t.Errorf("conflicted = %v", res.Conflicted)
	}
	if account(t, res, alice).Suspended {
		t.Error("alice is suspended")
	}

	// The other reasons of the primary domain.
	for ref, want := range map[string]string{refOther: ReasonDomainNotServed, refNone: ReasonDomainNotFound} {
		got := account(t, decide(t, input([]User{user(carol, ref)})), carol)
		if !got.Suspended || got.Reason != want {
			t.Errorf("primary domain %s: %+v, want %s", ref, got, want)
		}
	}
}

// A suspended account still holds its aliases: a second account cannot
// take them while the domain is revoked, it conflicts instead.
func TestSuspendedAccountStillHoldsItsAliases(t *testing.T) {
	res := decide(t, input([]User{
		user(carol, refRev, addr(sales, refB)),
		user(bob, refA, addr(sales, refB)),
	}))
	wantRow(t, row(t, res, carol, addrSales), false, ReasonAddressConflict, true)
	wantRow(t, row(t, res, bob, addrSales), false, ReasonAddressConflict, true)
}

// A disabled account is served and keeps receiving; it may not send.
func TestDisabledAccount(t *testing.T) {
	u := user(alice, refA, addr(sales, refB))
	u.Disabled = true
	res := decide(t, input([]User{u}))
	for _, r := range account(t, res, alice).Rows {
		wantRow(t, r, true, ReasonServed, false)
		if r.MaySend {
			t.Errorf("%s of a disabled account may send", r.Address)
		}
	}
	if account(t, res, alice).Suspended {
		t.Error("a disabled account is suspended")
	}
}

// Static test users have no MailDomain: the domain name is the key.
func TestStaticUsersKeyByDomainName(t *testing.T) {
	in := Input{
		Users:   []User{user("static", "e2e.example.org"), user("other", "gone.example.org")},
		Domains: map[string]Domain{"e2e.example.org": {"e2e.example.org", DomainOK}, "gone.example.org": {"gone.example.org", DomainNotAuthorized}},
	}
	res := decide(t, in)
	wantRow(t, row(t, res, "static", "static@e2e.example.org"), true, ReasonServed, false)
	if !account(t, res, "other").Suspended {
		t.Error("a static user in a domain that is not authorized is not suspended")
	}
}

// Nobody can deny or take the primary address of another account, however
// many claim it; the claimants lose alone.
func TestNoTenantCanShadowAPrimary(t *testing.T) {
	users := make([]User, 1, 51)
	users[0] = user(bob, refA)
	for i := range 50 {
		users = append(users, user("u"+strconv.Itoa(i), refA, addr(bob, refA)))
	}
	res := decide(t, input(users, sender("ns/x", bob, refA), sender("ns/y", bob, refA)))
	wantRow(t, row(t, res, bob, addrBob), true, ReasonServed, false)
	if len(res.Conflicted) != 0 || len(res.Deferred) != 0 {
		t.Errorf("conflicted %v, deferred %v", res.Conflicted, res.Deferred)
	}
	for _, s := range res.Senders {
		wantRow(t, s.Row, false, ReasonSenderConflict, false)
	}
}

// A claim never reaches an address of another domain with the same local
// part.
func TestSameLocalPartInOtherDomainIsNoConflict(t *testing.T) {
	res := decide(t, input([]User{user(alice, refA, addr(sales, refB)), user(bob, refA, addr(sales, refA))}))
	wantRow(t, row(t, res, alice, addrSales), true, ReasonServed, false)
	wantRow(t, row(t, res, bob, "sales@a.example"), true, ReasonServed, false)
}

// Random servers: the invariants hold whatever the claims, and the
// verdict does not depend on the order.
func TestRandomServers(t *testing.T) {
	refs := []string{refA, refB, refRev, refOther, refNone}
	locals := []string{"a", "b", "c", "d"}
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 99))
		var users []User
		for i := range r.IntN(7) {
			name := "u" + strconv.Itoa(i)
			u := user(name, refs[r.IntN(len(refs))])
			u.Disabled = r.IntN(5) == 0
			for range r.IntN(5) {
				u.Aliases = append(u.Aliases, addr(locals[r.IntN(len(locals))], refs[r.IntN(len(refs))]))
			}
			if r.IntN(3) == 0 { // a local part that is also a name
				u.Aliases = append(u.Aliases, addr("u"+strconv.Itoa(r.IntN(7)), refs[r.IntN(2)]))
			}
			users = append(users, u)
		}
		var senders []Sender
		for i := range r.IntN(4) {
			senders = append(senders, sender("ns/s"+strconv.Itoa(i), locals[r.IntN(len(locals))], refs[r.IntN(len(refs))]))
		}
		in := input(users, senders...)
		res := decide(t, in)
		checkInvariants(t, in, res, seed)
	}
}

func checkInvariants(t *testing.T, in Input, res Result, seed uint64) {
	t.Helper()
	owner := map[string]string{}
	serve := func(who string, r Row) {
		if !r.Served {
			if r.Reason == ReasonServed {
				t.Fatalf("seed %d: %s is not served but says Served", seed, r.Address)
			}
			return
		}
		if prev, ok := owner[r.Address]; ok && prev != who {
			t.Fatalf("seed %d: %s is served for %s and %s", seed, r.Address, prev, who)
		}
		owner[r.Address] = who
		if slices.Contains(res.Conflicted, r.Address) || slices.Contains(res.Deferred, r.Address) {
			t.Fatalf("seed %d: %s is served and conflicted or deferred", seed, r.Address)
		}
	}
	for _, u := range res.Users {
		for _, r := range u.Rows {
			serve("user/"+u.Name, r)
			if r.MaySend && !r.Served {
				t.Fatalf("seed %d: %s may send but is not served", seed, r.Address)
			}
		}
		// A primary in a domain that is ok is served: it has the best
		// rank and the names are unique.
		for _, src := range in.Users {
			if src.Name == u.Name && domainOK(in, src.Primary.Domain) && !u.Rows[0].Served {
				// Only a tie of primaries could take it; the names are
				// unique, so nothing can.
				t.Fatalf("seed %d: the primary of %s is not served: %+v", seed, u.Name, u.Rows[0])
			}
		}
	}
	for _, s := range res.Senders {
		serve("sender/"+s.Owner, s.Row)
	}
	for _, c := range res.Conflicted {
		if !slices.Contains(res.Deferred, c) {
			t.Fatalf("seed %d: %s is conflicted and not deferred", seed, c)
		}
	}
	if !slices.IsSorted(res.Conflicted) || !slices.IsSorted(res.Deferred) {
		t.Fatalf("seed %d: unsorted lists", seed)
	}
}

// A server at the caps is decided correctly; the cost, O(n log n) in the
// number of claims, is measured by the benchmark.
func TestDecideAtTheCaps(t *testing.T) {
	for _, aliases := range []int{4, MaxAliases} {
		in := capsInput(aliases)
		res := Decide(in)
		if len(res.Users) != MaxUsers || len(res.Conflicted) != MaxUsers/2*aliases {
			t.Errorf("users %d, conflicted %d", len(res.Users), len(res.Conflicted))
		}
	}
}

func capsInput(aliases int) Input {
	users := make([]User, 0, MaxUsers)
	for i := range MaxUsers {
		u := user("user"+strconv.Itoa(i), refA)
		for j := range aliases {
			// the accounts 2k and 2k+1 claim the same aliases: all conflict
			u.Aliases = append(u.Aliases, addr("al"+strconv.Itoa(i/2)+"-"+strconv.Itoa(j), refB))
		}
		users = append(users, u)
	}
	return input(users, sender("ns/app", "noreply", refA))
}

func domainOK(in Input, ref string) bool {
	d, ok := in.Domains[ref]
	return ok && d.State == DomainOK
}

func BenchmarkDecideAtTheCaps(b *testing.B) {
	for name, aliases := range map[string]int{"5000 addresses": 4, "33000 addresses": MaxAliases} {
		in := capsInput(aliases)
		b.Run(name, func(b *testing.B) {
			for range b.N {
				Decide(in)
			}
		})
	}
}

// Two MailDomain objects can name one DNS name (one of them is a duplicate
// and not authorized). An account that names an address through both gets
// one row, and the row does not depend on the order of the aliases.
func TestTwoObjectsForOneDNSName(t *testing.T) {
	const refDup = "dom-a-dup"
	for _, tc := range []struct {
		name       string
		dupState   DomainState
		wantServed bool
		wantReason string
	}{
		{"the other object is revoked", DomainNotAuthorized, true, ReasonServed},
		{"the other object is served by another server", DomainNotServed, true, ReasonServed},
		{"the other object is not found", DomainNotFound, true, ReasonServed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := domains()
			ds[refDup] = Domain{"A.example", tc.dupState}
			in := Input{Users: []User{user(alice, refA, addr("x", refA), addr("X", refDup), addr("y", refDup), addr("Y", refA))}, Domains: ds}
			res := decide(t, in)
			a := account(t, res, alice)
			if len(a.Rows) != 3 {
				t.Fatalf("rows = %+v, want the primary and two aliases", a.Rows)
			}
			wantRow(t, row(t, res, alice, "x@a.example"), tc.wantServed, tc.wantReason, false)
			wantRow(t, row(t, res, alice, "y@a.example"), tc.wantServed, tc.wantReason, false)
		})
	}
	// Both objects fail with different states: the pick is by the text of
	// the reason, whatever the order.
	ds := domains()
	ds[refDup] = Domain{"rev.example", DomainNotServed}
	res := decide(t, Input{Users: []User{user(alice, refA, addr("x", refRev), addr("x", refDup))}, Domains: ds})
	wantRow(t, row(t, res, alice, "x@rev.example"), false, ReasonAliasDomainNotAuthorized, false)
}
