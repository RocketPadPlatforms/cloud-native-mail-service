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
	"reflect"
	"strings"
	"testing"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

const (
	domA         = "a.example"
	domB         = "b.example"
	nmZed        = "zed"
	nmAmy        = "amy"
	nmBob        = "bob"
	addrBob      = "bob@a.example"
	addrAmyUpper = "AMY@a.example"
	addrOff      = "off@a.example"
	hashA        = "{X}a"
	addrSales    = "sales@b.example"
	addrAmy      = "amy@a.example"
	addrShared   = "shared@b.example"
	addrX        = "x@b.example"
	errClaimed   = "claimed by"
	errBlank     = "blank"
	errInvalid   = "not a valid address"
)

var authorized = []string{domA, domB}

func plan(t *testing.T, accounts []Account, domains, conflicted []string) *AccountPlan {
	t.Helper()
	p, err := PlanAccounts(accounts, domains, conflicted)
	if err != nil {
		t.Fatalf("PlanAccounts() = %v", err)
	}
	return p
}

func TestPlanAccountsSplitsLoginsFromDelivery(t *testing.T) {
	p := plan(t, []Account{
		{Name: nmZed, Address: "Zed@a.example", PasswordHash: "{X}z"},
		{Name: nmAmy, Address: addrAmy, PasswordHash: hashA, Aliases: []string{"Amy@b.example", addrSales}},
		{Name: "off", Address: addrOff, PasswordHash: "{X}o", Disabled: true, Aliases: []string{"off2@b.example"}},
		{Name: "gone", Address: "gone@revoked.example", PasswordHash: "{X}g", Aliases: []string{"gone@a.example", "gone@foreign.example"}},
	}, authorized, nil)

	// Sorted by name, and lower case.
	if got := names(p.Users); !reflect.DeepEqual(got, []string{nmAmy, "gone", "off", nmZed}) {
		t.Errorf("users = %v: every account must be in the userdb", got)
	}
	if got := names(p.Logins); !reflect.DeepEqual(got, []string{nmAmy, nmZed}) {
		t.Errorf("logins = %v, want the accounts that are neither disabled nor suspended", got)
	}
	if want := []string{addrAmy, addrOff, "zed@a.example"}; !reflect.DeepEqual(p.Mailboxes, want) {
		t.Errorf("mailboxes = %v, want %v (a disabled account receives, a suspended one does not)", p.Mailboxes, want)
	}
	if want := []Pair{{"amy@b.example", addrAmy}, {"off2@b.example", addrOff}, {addrSales, addrAmy}}; !reflect.DeepEqual(p.Aliases, want) {
		t.Errorf("aliases = %v, want %v", p.Aliases, want)
	}
	// Owners: the primary and the aliases of an account that may send.
	if want := []Pair{{addrAmy, nmAmy}, {"amy@b.example", nmAmy}, {addrSales, nmAmy}, {"zed@a.example", nmZed}}; !reflect.DeepEqual(p.Senders, want) {
		t.Errorf("senders = %v, want %v", p.Senders, want)
	}
	// The alias of a suspended account is deferred; the one in a domain
	// that is not served is simply not served.
	if want := []string{"gone@a.example"}; !reflect.DeepEqual(p.Deferred, want) {
		t.Errorf("deferred = %v, want %v", p.Deferred, want)
	}
}

func names(as []Account) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

func TestPlanAccountsConflictedAddressesAreServedByNobody(t *testing.T) {
	p := plan(t, []Account{
		{Name: nmAmy, Address: addrAmy, PasswordHash: hashA, Aliases: []string{addrShared, "amy2@b.example"}},
	}, authorized, []string{"Shared@b.example", "elsewhere@a.example", "foreign@other.example"})
	if want := []Pair{{"amy2@b.example", addrAmy}}; !reflect.DeepEqual(p.Aliases, want) {
		t.Errorf("aliases = %v, want %v", p.Aliases, want)
	}
	for _, s := range p.Senders {
		if s.Key == addrShared {
			t.Error("a conflicted address is owned by an account")
		}
	}
	// Deferred where the domain is served, even when no account claims it.
	if want := []string{"elsewhere@a.example", addrShared}; !reflect.DeepEqual(p.Deferred, want) {
		t.Errorf("deferred = %v, want %v", p.Deferred, want)
	}
}

// Without an authorized domain nothing is served, not even the users.
func TestPlanAccountsFailClosed(t *testing.T) {
	p := plan(t, []Account{{Name: nmAmy, Address: addrAmy, PasswordHash: hashA, Aliases: []string{addrX}}}, nil, []string{"c@a.example"})
	if !reflect.DeepEqual(*p, AccountPlan{}) {
		t.Errorf("plan = %+v, want it empty", *p)
	}
}

func TestPlanAccountsRefusals(t *testing.T) {
	base := Account{Name: nmAmy, Address: addrAmy, PasswordHash: hashA}
	with := func(mutate func(*Account)) []Account {
		a := base
		mutate(&a)
		return []Account{a}
	}
	for name, tc := range map[string]struct {
		accounts []Account
		want     string
	}{
		"name twice":              {[]Account{base, {Name: "AMY", Address: "amy2@a.example", PasswordHash: hashA}}, "used twice"},
		"name with a slash":       {with(func(a *Account) { a.Name = "a/b" }), "not valid"},
		"empty name":              {with(func(a *Account) { a.Name = "" }), "not valid"},
		"hash with a colon":       {with(func(a *Account) { a.PasswordHash = "{X}a:b" }), "must not contain"},
		"primary with a blank":    {with(func(a *Account) { a.Address = "a b@a.example" }), errBlank},
		"primary without domain":  {with(func(a *Account) { a.Address = nmAmy }), "exactly one @"},
		"alias with a newline":    {with(func(a *Account) { a.Aliases = []string{"x\ny@b.example"} }), errBlank},
		"alias with a comma":      {with(func(a *Account) { a.Aliases = []string{"x,y@b.example"} }), errBlank},
		"alias with a plus":       {with(func(a *Account) { a.Aliases = []string{"x+y@b.example"} }), errInvalid},
		"alias with two dots":     {with(func(a *Account) { a.Aliases = []string{"x..y@b.example"} }), errInvalid},
		"alias with a bad domain": {with(func(a *Account) { a.Aliases = []string{"x@b_example"} }), errInvalid},
		"alias too long":          {with(func(a *Account) { a.Aliases = []string{strings.Repeat("x", 65) + "@b.example"} }), errInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PlanAccounts(tc.accounts, authorized, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PlanAccounts() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	// An alias of another account's primary, the same alias twice and an alias
	// that is the primary address are decided by addresses.Decide: nothing
	// reaches a table that is refused here (TestPlanAccountsAppliesTheDecision).
	// A primary address never loses a conflict (ADR 0015, section 4.2):
	// a verdict that names one would give a half-served account.
	t.Run("conflicted address is a primary", func(t *testing.T) {
		_, err := PlanAccounts([]Account{base}, authorized, []string{addrAmyUpper})
		if err == nil || !strings.Contains(err.Error(), "never loses") {
			t.Fatalf("PlanAccounts() = %v, want an error containing %q", err, "never loses")
		}
	})
	if _, err := PlanAccounts(nil, authorized, []string{"x y@a.example"}); err == nil {
		t.Error("a conflicted address with a blank must be refused")
	}
}

// The plan is a pure function: it neither changes its input nor depends
// on its order.
func TestPlanAccountsIsDeterministic(t *testing.T) {
	in := []Account{
		{Name: "b", Address: "B@a.example", PasswordHash: "{X}b", Aliases: []string{"Y@b.example", addrX}},
		{Name: "a", Address: "a@a.example", PasswordHash: hashA},
	}
	snapshot := []Account{in[0], in[1]}
	first := plan(t, in, authorized, nil)
	if in[0].Address != "B@a.example" || in[0].Aliases[0] != "Y@b.example" || !reflect.DeepEqual(in[1], snapshot[1]) {
		t.Error("PlanAccounts changed its input")
	}
	in[0], in[1] = in[1], in[0]
	if second := plan(t, in, []string{domB, domA}, nil); !reflect.DeepEqual(first, second) {
		t.Errorf("the plan depends on the order of the input:\n%+v\n%+v", first, second)
	}
}

// The claims that used to be refused are decided by addresses.Decide: an
// alias never takes another's primary address, an alias named twice is one,
// and an alias that is the primary address is the primary address.
func TestPlanAccountsAppliesTheDecision(t *testing.T) {
	p := plan(t, []Account{
		{Name: nmAmy, Address: addrAmy, PasswordHash: hashA, Aliases: []string{addrX, "X@b.example", addrAmy}},
		{Name: nmBob, Address: addrBob, PasswordHash: hashA, Aliases: []string{addrAmyUpper, addrBob}},
	}, authorized, nil)
	if want := []Pair{{addrX, addrAmy}}; !reflect.DeepEqual(p.Aliases, want) {
		t.Errorf("aliases = %v, want %v", p.Aliases, want)
	}
	if want := []Pair{{addrAmy, nmAmy}, {addrBob, nmBob}, {addrX, nmAmy}}; !reflect.DeepEqual(p.Senders, want) {
		t.Errorf("senders = %v, want %v", p.Senders, want)
	}
	if len(p.Deferred) != 0 {
		t.Errorf("deferred = %v: a lost claim of a lower rank is no conflict", p.Deferred)
	}
}

// An account whose verdict is given is served exactly as its rows say; a
// verdict that two accounts share an address in, or that does not belong to
// the account, is refused.
func TestPlanAccountsRefusesAVerdictThatDoesNotFit(t *testing.T) {
	row := func(addr string, kind addresses.Kind, served bool) addresses.Row {
		return addresses.Row{Address: addr, Kind: kind, Served: served, MaySend: served}
	}
	decided := func(name, primary string, rows ...addresses.Row) Account {
		return Account{Name: name, Address: primary, PasswordHash: hashA, Verdict: addresses.UserResult{
			Name: name, Rows: append([]addresses.Row{row(primary, addresses.KindPrimary, true)}, rows...)}}
	}
	for name, tc := range map[string]struct {
		accounts []Account
		want     string
	}{
		"an alias served twice": {[]Account{
			decided(nmAmy, addrAmy, row(addrX, addresses.KindAlias, true)),
			decided(nmBob, addrBob, row(addrX, addresses.KindAlias, true)),
		}, errClaimed},
		"a verdict of another primary": {[]Account{decided(nmAmy, "other@a.example")}, ""},
		"some accounts undecided": {[]Account{
			decided(nmAmy, addrAmy),
			{Name: nmBob, Address: addrBob, PasswordHash: hashA},
		}, "no verdict"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "a verdict of another primary" {
				tc.accounts[0].Address = addrAmy // the row says other@
			}
			_, err := PlanAccounts(tc.accounts, authorized, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PlanAccounts() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// An account without a password hash has no login and still receives mail:
// its credential may be missing for a while, and mail must not be lost.
func TestPlanAccountsWithoutAHashKeepsReceiving(t *testing.T) {
	p := plan(t, []Account{{Name: nmAmy, Address: addrAmy}, {Name: nmBob, Address: addrBob, PasswordHash: hashA}}, authorized, nil)
	if got := names(p.Logins); !reflect.DeepEqual(got, []string{nmBob}) {
		t.Errorf("logins = %v, want only the account with a hash", got)
	}
	if want := []string{addrAmy, addrBob}; !reflect.DeepEqual(p.Mailboxes, want) {
		t.Errorf("mailboxes = %v, want %v", p.Mailboxes, want)
	}
}

// The same primary address twice is a tie of the decision (two objects that
// name one DNS name): both lose, nothing is served, the address is deferred,
// and the render does not fail.
func TestPlanAccountsTwoEqualPrimariesAreDeferred(t *testing.T) {
	p := plan(t, []Account{
		{Name: nmAmy, Address: addrAmy, PasswordHash: hashA},
		{Name: "amy2", Address: addrAmyUpper, PasswordHash: hashA},
	}, authorized, nil)
	if len(p.Mailboxes) != 0 || len(p.Senders) != 0 {
		t.Errorf("mailboxes %v, senders %v: a tied primary address is served by nobody", p.Mailboxes, p.Senders)
	}
	if want := []string{addrAmy}; !reflect.DeepEqual(p.Deferred, want) {
		t.Errorf("deferred = %v, want %v", p.Deferred, want)
	}
}
