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

package render

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

// PlanAccounts applies the verdict of addresses.Decide and decides nothing:
// the plan must equal what the rows of the verdict say about who receives,
// who may send, who logs in and what answers 450. These tests give the
// accounts of NewAccounts (the verdict attached, as the MailServer controller
// does) to PlanAccounts, and compare the plan with the rows on scenarios and
// on random servers.

func domainStates() map[string]addresses.Domain {
	return map[string]addresses.Domain{
		"dom-a":   {Name: domA, State: addresses.DomainOK},
		"dom-b":   {Name: domB, State: addresses.DomainOK},
		"dom-rev": {Name: "rev.example", State: addresses.DomainNotAuthorized},
		"dom-oth": {Name: "oth.example", State: addresses.DomainNotServed},
	}
}

func au(name, ref string, disabled bool, aliases ...addresses.Address) addresses.User {
	return addresses.User{Name: name, Disabled: disabled, Primary: addresses.Address{Local: name, Domain: ref}, Aliases: aliases}
}

func aa(local, ref string) addresses.Address { return addresses.Address{Local: local, Domain: ref} }

// consistent compares the two decisions for one input. The expected lists
// of the plan are built from the rows of the verdict, and the plan must
// equal them: nothing missing, nothing extra, the right targets and owners.
func consistent(t *testing.T, label string, in addresses.Input) {
	t.Helper()
	res := addresses.Decide(in)
	var authorized []string
	for _, d := range in.Domains {
		if d.State == addresses.DomainOK {
			authorized = append(authorized, d.Name)
		}
	}
	accounts := NewAccounts(res, in.Users, func(string) string { return "{X}h" })
	wantMailboxes := make([]string, 0, len(res.Users))
	wantLogins := make([]string, 0, len(res.Users))
	wantUsers := make([]string, 0, len(res.Users))
	var wantAliases, wantSenders []Pair
	for _, u := range res.Users {
		src := in.Users[indexOf(in.Users, u.Name)]
		primary := u.Rows[0]
		wantUsers = append(wantUsers, u.Name)
		if !u.Suspended && !src.Disabled {
			wantLogins = append(wantLogins, u.Name)
		}
		for _, r := range u.Rows {
			switch {
			case r.Kind == addresses.KindPrimary && r.Served:
				wantMailboxes = append(wantMailboxes, r.Address)
			case r.Kind == addresses.KindAlias && r.Served:
				wantAliases = append(wantAliases, Pair{r.Address, primary.Address})
			}
			if r.MaySend {
				wantSenders = append(wantSenders, Pair{r.Address, u.Name})
			}
		}
	}
	byKey := func(a, b Pair) int { return strings.Compare(a.Key, b.Key) }
	slices.Sort(wantMailboxes)
	slices.SortFunc(wantAliases, byKey)
	slices.SortFunc(wantSenders, byKey)

	plan, err := PlanAccounts(accounts, authorized, res.Conflicted)
	if err != nil {
		t.Fatalf("%s: PlanAccounts() = %v", label, err)
	}
	if !slices.Equal(plan.Mailboxes, wantMailboxes) {
		t.Errorf("%s: mailboxes = %v in the plan, %v in the verdict", label, plan.Mailboxes, wantMailboxes)
	}
	if !slices.Equal(plan.Aliases, wantAliases) {
		t.Errorf("%s: aliases = %v in the plan, %v in the verdict", label, plan.Aliases, wantAliases)
	}
	if !slices.Equal(plan.Senders, wantSenders) {
		t.Errorf("%s: senders = %v in the plan, %v in the verdict", label, plan.Senders, wantSenders)
	}
	if !slices.Equal(plan.Deferred, res.Deferred) {
		t.Errorf("%s: deferred = %v in the plan, %v in the verdict", label, plan.Deferred, res.Deferred)
	}
	if got := accountNames(plan.Users); !slices.Equal(got, wantUsers) {
		t.Errorf("%s: userdb = %v in the plan, %v in the verdict", label, got, wantUsers)
	}
	if got := accountNames(plan.Logins); !slices.Equal(got, wantLogins) {
		t.Errorf("%s: passdb = %v in the plan, %v in the verdict", label, got, wantLogins)
	}
}

func accountNames(as []Account) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

func indexOf(us []addresses.User, name string) int {
	return slices.IndexFunc(us, func(u addresses.User) bool { return u.Name == name })
}

func TestPlanAccountsAgreesWithTheVerdict(t *testing.T) {
	scenarios := map[string][]addresses.User{
		"aliases across two domains": {au("alice", "dom-a", false, aa("sales", "dom-b"), aa("alice", "dom-b"))},
		"alias equal to a primary":   {au("alice", "dom-a", false), au("bob", "dom-a", false, aa("alice", "dom-a"))},
		"equal aliases tie":          {au("alice", "dom-a", false, aa("t", "dom-b")), au("bob", "dom-a", false, aa("T", "dom-b"))},
		"disabled account":           {au("alice", "dom-a", true, aa("s", "dom-b")), au("bob", "dom-a", false)},
		"revoked primary domain":     {au("carol", "dom-rev", false, aa("c", "dom-a"), aa("d", "dom-oth"), aa("e", "dom-b"))},
		"revoked alias domain":       {au("alice", "dom-a", false, aa("x", "dom-rev"), aa("y", "dom-oth"), aa("z", "dom-none"))},
		"suspended holds an alias":   {au("carol", "dom-rev", false, aa("s", "dom-b")), au("bob", "dom-a", false, aa("s", "dom-b"))},
		"unknown primary domain":     {au("dave", "dom-none", false, aa("d", "dom-a"))},
		"nothing":                    nil,
	}
	for name, users := range scenarios {
		consistent(t, name, addresses.Input{Users: users, Domains: domainStates()})
	}
	// Service accounts take part in the ranking and in the conflicts.
	consistent(t, "senders", addresses.Input{
		Users: []addresses.User{au("alice", "dom-a", false, aa("s", "dom-b"))},
		Senders: []addresses.Sender{
			{Owner: "ns1/app", Address: aa("s", "dom-b")},
			{Owner: "ns1/app", Address: aa("n", "dom-a")},
			{Owner: "ns2/app", Address: aa("n", "dom-a")},
		},
		Domains: domainStates(),
	})
}

// mixed returns s with the case of every letter chosen at random: the
// folding of the addresses and of the domain names is part of the contract.
func mixed(r *rand.Rand, s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' && r.IntN(2) == 0 {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

func TestPlanAccountsAgreesOnRandomServers(t *testing.T) {
	refs := []string{"dom-a", "dom-b", "dom-rev", "dom-oth", "dom-none"}
	locals := []string{"a", "b", "c"}
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 5))
		domains := domainStates()
		for ref, d := range domains {
			d.Name = mixed(r, d.Name)
			domains[ref] = d
		}
		var users []addresses.User
		for i := range r.IntN(7) {
			name := "u" + strconv.Itoa(i)
			u := au(name, refs[r.IntN(len(refs))], r.IntN(4) == 0)
			u.Primary.Local = mixed(r, name)
			for range r.IntN(5) {
				u.Aliases = append(u.Aliases, aa(mixed(r, locals[r.IntN(len(locals))]), refs[r.IntN(len(refs))]))
			}
			if r.IntN(3) == 0 {
				u.Aliases = append(u.Aliases, aa(mixed(r, "u"+strconv.Itoa(r.IntN(7))), refs[r.IntN(2)]))
			}
			users = append(users, u)
		}
		var senders []addresses.Sender
		for i := range r.IntN(3) {
			senders = append(senders, addresses.Sender{Owner: "ns/s" + strconv.Itoa(i), Address: aa(mixed(r, locals[r.IntN(3)]), refs[r.IntN(len(refs))])})
		}
		consistent(t, "seed "+strconv.Itoa(int(seed)), addresses.Input{Users: users, Senders: senders, Domains: domains})
	}
}

// An account whose primary domain does not exist has a primary row named
// after the reference of the missing MailDomain. When that text is an address
// that another account serves as an alias (a MailDomain of that name exists
// for a different object), the unserved row is no claim: the render of the
// others does not fail, and the address stays with the account that serves it.
func TestPlanAccountsAnUnservedPrimaryClaimsNothing(t *testing.T) {
	in := addresses.Input{
		Users: []addresses.User{
			au("alice", "b.example", false), // no MailDomain object of that name
			au("bob", "dom-a", false, aa("alice", "dom-b")),
		},
		Domains: domainStates(),
	}
	res := addresses.Decide(in)
	accounts := NewAccounts(res, in.Users, func(string) string { return "{X}h" })
	if got := res.Users[0].Rows[0]; got.Address != addrAliceB || got.Served {
		t.Fatalf("setup: the primary row of alice = %+v, want an unserved alice@b.example", got)
	}
	p, err := PlanAccounts(accounts, []string{domA, "b.example"}, res.Conflicted)
	if err != nil {
		t.Fatalf("PlanAccounts() = %v: one unserved primary must not fail the render", err)
	}
	if want := []Pair{{addrAliceB, "bob@" + domA}}; !slices.Equal(p.Aliases, want) {
		t.Errorf("aliases = %v, want %v", p.Aliases, want)
	}
	// The conflicted case: the same text is in the conflicted set.
	if _, err := PlanAccounts(accounts, []string{domA, "b.example"}, []string{addrAliceB}); err != nil {
		t.Errorf("PlanAccounts() with the text conflicted = %v, want no error", err)
	}
}

const addrAliceB = "alice@" + domB
