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
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
)

// aliasLocalRE is the local part of an alias (the pattern of
// MailUserAlias.localPart), checked again here because the value ends up
// in a table file.
var aliasLocalRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)

// domainRE is a lower-case DNS name with at least two labels.
var domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?[.])+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Pair is one line of a table: a key and its value.
type Pair struct {
	Key, Value string
}

// AccountPlan is what the engines read from the accounts of a request:
// one decision, made once, so that Postfix (who may send as what, where
// mail goes) and Dovecot (who may log in, where the mailbox is) cannot
// disagree (ADR 0015, sections 4.3, 4.4, 4.6). It is a pure function of
// the verdicts of the accounts and the conflicted addresses.
type AccountPlan struct {
	// Mailboxes are the canonical addresses that receive mail
	// (virtual_mailbox_maps): every account whose primary domain is
	// authorized, a disabled one too (a disabled account keeps receiving).
	Mailboxes []string
	// Aliases map an alias to the canonical address (virtual_alias_maps),
	// for the aliases in an authorized domain of an account that is not
	// suspended.
	Aliases []Pair
	// Senders map every address an account owns, the primary and the
	// aliases, to the account name (smtpd_sender_login_maps). Disabled and
	// suspended accounts own no sender.
	Senders []Pair
	// Deferred are the addresses that answer 450 4.2.1 (recipient_defer):
	// the aliases of a suspended account and the conflicted addresses.
	Deferred []string
	// Users are the accounts Dovecot must find for a delivery or a
	// session: all of them, disabled and suspended ones too, so that mail
	// that Postfix accepted is not bounced (the userdb file).
	Users []Account
	// Logins are the accounts that may log in: not disabled, not
	// suspended (the passdb file).
	Logins []Account
}

// Suspended reports whether the account is suspended as a whole because
// its primary domain fails (ADR 0015, section 4.6).
func (a Account) Suspended() bool { return a.Verdict.Suspended }

func domainOf(addr string) string {
	_, d, _ := strings.Cut(addr, "@")
	return d
}

// DecideAccounts gives the accounts that come without a verdict one: the
// static test users have no MailDomain objects, so the domain of each
// address is named by its DNS name and is ok when it is in authorized
// (ADR 0015, sections 3 and 4.2). It runs addresses.Decide, the function
// that decides the addresses of the MailUsers, so there is one decision
// for every producer of accounts. The second result is the list of
// conflicted addresses. The accounts it is given are not changed.
func DecideAccounts(accounts []Account, authorized []string) ([]Account, []string) {
	authorized = lowerAll(authorized)
	in := addresses.Input{Domains: map[string]addresses.Domain{}}
	state := func(domain string) string {
		domain = strings.ToLower(domain)
		s := addresses.DomainNotAuthorized
		if slices.Contains(authorized, domain) {
			s = addresses.DomainOK
		}
		in.Domains[domain] = addresses.Domain{Name: domain, State: s}
		return domain
	}
	for _, a := range accounts {
		local, domain, _ := strings.Cut(strings.ToLower(a.Address), "@")
		u := addresses.User{Name: strings.ToLower(a.Name), Disabled: a.Disabled, Primary: addresses.Address{Local: local, Domain: state(domain)}}
		for _, al := range a.Aliases {
			local, domain, _ := strings.Cut(strings.ToLower(al), "@")
			u.Aliases = append(u.Aliases, addresses.Address{Local: local, Domain: state(domain)})
		}
		in.Users = append(in.Users, u)
	}
	res := addresses.Decide(in)
	out := slices.Clone(accounts)
	for i := range out {
		for _, u := range res.Users {
			if u.Name == strings.ToLower(out[i].Name) {
				out[i].Verdict = u
			}
		}
	}
	return out, res.Conflicted
}

// PlanAccounts derives the AccountPlan from the verdict of addresses.Decide
// that each account carries (Account.Verdict): who receives, who may send,
// who logs in and what answers 450 are read from its rows, and nothing is
// decided here. An account that has none (the static test users) is
// decided first by DecideAccounts. Without an authorized domain nothing is
// served (the fail-closed render of ADR 0003): the plan is empty, users
// included. conflicted are the addresses that lost a conflict (ADR 0015,
// section 4.2), Result.Conflicted of the decision; they answer 450 and are
// never served, whoever claims them.
//
// It validates every value that reaches a table file again (the values
// come from validated API fields, but the renderers are the last line
// before a file that Postfix and Dovecot parse by whitespace and
// colons) and refuses an address that two accounts serve: the decision
// for conflicts is addresses.Decide's, so a conflict that gets here is a
// bug and must not be rendered.
func PlanAccounts(accounts []Account, authorized, conflicted []string) (*AccountPlan, error) {
	authorized = lowerAll(authorized)
	accounts, conflicted, err := withVerdicts(accounts, authorized, conflicted)
	if err != nil {
		return nil, err
	}
	lostSet := map[string]bool{}
	for _, c := range conflicted {
		c = strings.ToLower(c)
		if err := validateAddress(c, true); err != nil {
			return nil, fmt.Errorf("conflicted address: %w", err)
		}
		lostSet[c] = true
	}
	if err := checkVerdicts(accounts, lostSet); err != nil {
		return nil, err
	}
	plan := &AccountPlan{}
	if len(authorized) == 0 {
		return plan, nil
	}
	slices.SortFunc(accounts, func(x, y Account) int { return strings.Compare(x.Name, y.Name) })
	deferred := map[string]bool{}
	for _, a := range accounts {
		plan.Users = append(plan.Users, a)
		if !a.Disabled && !a.Suspended() && a.PasswordHash != "" {
			plan.Logins = append(plan.Logins, a)
		}
		for _, r := range a.Verdict.Rows {
			switch {
			case r.Kind == addresses.KindPrimary && r.Served:
				plan.Mailboxes = append(plan.Mailboxes, r.Address)
			case r.Kind == addresses.KindAlias && r.Served:
				plan.Aliases = append(plan.Aliases, Pair{r.Address, a.Address})
			}
			if r.MaySend {
				plan.Senders = append(plan.Senders, Pair{r.Address, a.Name})
			}
			if r.Deferred {
				deferred[r.Address] = true
			}
		}
	}
	for c := range lostSet {
		if slices.Contains(authorized, domainOf(c)) {
			deferred[c] = true
		}
	}
	// A conflicted address is never served, even if an account claims it
	// as an alias (a primary address cannot lose, see checkVerdicts).
	plan.Aliases = slices.DeleteFunc(plan.Aliases, func(p Pair) bool { return lostSet[p.Key] })
	plan.Senders = slices.DeleteFunc(plan.Senders, func(p Pair) bool { return lostSet[p.Key] })

	for a := range deferred {
		plan.Deferred = append(plan.Deferred, a)
	}
	slices.Sort(plan.Mailboxes)
	slices.Sort(plan.Deferred)
	byKey := func(x, y Pair) int { return strings.Compare(x.Key, y.Key) }
	slices.SortFunc(plan.Aliases, byKey)
	slices.SortFunc(plan.Senders, byKey)
	return plan, nil
}

// withVerdicts returns the accounts with their verdicts, and the conflicted
// addresses to apply. Account names are unique first; accounts that come with
// a verdict are used as they are, accounts without one (all of them, or none)
// are decided by DecideAccounts.
func withVerdicts(accounts []Account, authorized, conflicted []string) ([]Account, []string, error) {
	accounts = slices.Clone(accounts)
	names := map[string]bool{}
	for _, a := range accounts {
		n := strings.ToLower(a.Name)
		if names[n] {
			return nil, nil, fmt.Errorf("account name %q is used twice", n)
		}
		names[n] = true
	}
	undecided := slices.IndexFunc(accounts, func(a Account) bool { return len(a.Verdict.Rows) == 0 })
	if undecided < 0 {
		return accounts, conflicted, nil
	}
	if slices.ContainsFunc(accounts, func(a Account) bool { return len(a.Verdict.Rows) > 0 }) {
		return nil, nil, fmt.Errorf("account %q has no verdict, and others have one", accounts[undecided].Name)
	}
	// The claims of such an account are values from outside the decision:
	// check them before they are decided.
	for _, a := range accounts {
		for _, al := range a.Aliases {
			if err := validateAddress(strings.ToLower(al), true); err != nil {
				return nil, nil, fmt.Errorf("account %q: alias: %w", a.Name, err)
			}
		}
	}
	accounts, lost := DecideAccounts(accounts, authorized)
	return accounts, append(slices.Clone(conflicted), lost...), nil
}

// checkVerdicts normalizes and validates the accounts (in place) and refuses
// a verdict that cannot be rendered: one that does not belong to its
// account, an address that two accounts serve, a primary address in the
// conflicted set (it never loses, ADR 0015, section 4.2, point 2: a verdict
// that says otherwise is a bug of the caller and would give a half-served
// account).
func checkVerdicts(accounts []Account, lostSet map[string]bool) error {
	owner := map[string]string{} // address -> account name
	claim := func(addr, name string) error {
		if prev, ok := owner[addr]; ok {
			return fmt.Errorf("address %q is claimed by accounts %q and %q", addr, prev, name)
		}
		owner[addr] = name
		return nil
	}
	for i := range accounts {
		a := &accounts[i]
		a.Name = strings.ToLower(a.Name)
		a.Address = strings.ToLower(a.Address)
		if err := validateAccount(*a); err != nil {
			return err
		}
		rows := a.Verdict.Rows
		if len(rows) == 0 || rows[0].Kind != addresses.KindPrimary || rows[0].Address != a.Address {
			return fmt.Errorf("account %q: the verdict does not start with its primary address %q", a.Name, a.Address)
		}
		// An unserved primary address (its domain fails) is no claim: for a
		// domain that does not exist the verdict names the address after the
		// reference, which may be the text of an address that another account
		// serves, and one such account must not stop the render of the others.
		if rows[0].Served {
			if err := claim(a.Address, a.Name); err != nil {
				return err
			}
			if lostSet[a.Address] {
				return fmt.Errorf("conflicted address %q is the primary address of account %q: a primary address never loses", a.Address, a.Name)
			}
		}
		for _, r := range rows[1:] {
			if !r.Served && !r.Deferred {
				continue
			}
			if err := validateAddress(r.Address, true); err != nil {
				return fmt.Errorf("account %q: alias: %w", a.Name, err)
			}
			if r.Served {
				if err := claim(r.Address, a.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

// validateAccount refuses a value that would break a table or passwd-file
// line: separators, control characters, or a shape the API would not have
// let through.
func validateAccount(a Account) error {
	if a.Name == "" || len(a.Name) > 64 || !usernameRE.MatchString(a.Name) || strings.Contains(a.Name, "..") {
		return fmt.Errorf("account name %q is not valid", a.Name)
	}
	if hasForbiddenChar(a.PasswordHash) {
		return fmt.Errorf("account %q: the password hash must not contain : or control characters", a.Name)
	}
	if err := validateAddress(a.Address, false); err != nil {
		return fmt.Errorf("account %q: primary address: %w", a.Name, err)
	}
	return nil
}

// validateAddress checks an address for a table. A strict address is the
// form the API allows for an alias (lower-case local part pattern of
// MailUserAlias, DNS domain); the primary address of a static test user
// only has to be one address without a separator.
func validateAddress(addr string, strict bool) error {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return fmt.Errorf("%q is not an address with exactly one @", addr)
	}
	if strings.ContainsFunc(addr, func(r rune) bool {
		return r <= ' ' || r == 0x7f || r == ':' || r == ',' || r == '#' || r > 0x7e
	}) {
		return fmt.Errorf("%q contains a blank, a control character, one of : , # or a non-ASCII character", addr)
	}
	if strict && (!aliasLocalRE.MatchString(local) || strings.Contains(local, "..") || !domainRE.MatchString(domain)) {
		return fmt.Errorf("%q is not a valid address", addr)
	}
	return nil
}

// NewAccounts joins the verdict of addresses.Decide for the users of a
// server with what the decision does not know: the password hash (empty for
// an account whose credential is missing, which has no login and still
// receives mail) and the disabled flag. There is one Account per user of the
// verdict, so the status of a MailUser and the tables that are rendered come
// from the same rows.
func NewAccounts(res addresses.Result, users []addresses.User, hash func(name string) string) []Account {
	disabled := make(map[string]bool, len(users))
	for _, u := range users {
		disabled[u.Name] = u.Disabled
	}
	accounts := make([]Account, 0, len(res.Users))
	for _, u := range res.Users {
		accounts = append(accounts, Account{
			Name: u.Name, PasswordHash: hash(u.Name), Address: u.Rows[0].Address,
			Disabled: disabled[u.Name], Verdict: u,
		})
	}
	return accounts
}
