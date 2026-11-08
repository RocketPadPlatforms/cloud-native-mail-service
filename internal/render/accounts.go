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
// the accounts, the authorized domains and the conflicted addresses.
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

// Suspended reports whether the primary address of the account is in a
// domain that is not authorized (ADR 0015, section 4.6): the account is
// suspended as a whole.
func (a Account) Suspended(authorized []string) bool {
	return !slices.Contains(authorized, domainOf(strings.ToLower(a.Address)))
}

func domainOf(addr string) string {
	_, d, _ := strings.Cut(addr, "@")
	return d
}

// PlanAccounts derives the AccountPlan. Without an authorized domain
// nothing is served (the fail-closed render of ADR 0003): the plan is
// empty, users included. conflicted are the addresses that lost a
// conflict (ADR 0015, section 4.2); they answer 450 and are never
// served, whoever claims them.
//
// The conflicts are decided elsewhere, by addresses.Decide, which sees the
// claimants the renderer does not (service accounts) and the domains of
// every address: its Conflicted list is the conflicted argument here, and
// the Aliases of an account are UserResult.Aliases. PlanAccounts only
// applies that verdict; TestPlanAccountsAgreesWithTheVerdict keeps the two
// in step on scenarios and on random servers.
//
// It validates every value that reaches a table file again (the values
// come from validated API fields, but the renderers are the last line
// before a file that Postfix and Dovecot parse by whitespace and
// colons) and refuses an address that two accounts claim: the decision
// for conflicts is the controller's, so a conflict that gets here is a
// bug and must not be rendered.
func PlanAccounts(accounts []Account, authorized, conflicted []string) (*AccountPlan, error) {
	plan := &AccountPlan{}
	authorized = lowerAll(authorized)
	lostSet := map[string]bool{}
	for _, c := range conflicted {
		c = strings.ToLower(c)
		if err := validateAddress(c, true); err != nil {
			return nil, fmt.Errorf("conflicted address: %w", err)
		}
		lostSet[c] = true
	}

	owner := map[string]string{} // address -> account name
	claim := func(addr, name string) error {
		if prev, ok := owner[addr]; ok {
			return fmt.Errorf("address %q is claimed by accounts %q and %q", addr, prev, name)
		}
		owner[addr] = name
		return nil
	}
	names := map[string]bool{}
	accounts = slices.Clone(accounts)
	for i := range accounts {
		a := &accounts[i]
		a.Name = strings.ToLower(a.Name)
		a.Address = strings.ToLower(a.Address)
		a.Aliases = lowerAll(a.Aliases)
		if err := validateAccount(*a); err != nil {
			return nil, err
		}
		if names[a.Name] {
			return nil, fmt.Errorf("account name %q is used twice", a.Name)
		}
		names[a.Name] = true
		if err := claim(a.Address, a.Name); err != nil {
			return nil, err
		}
		// A primary address always wins a conflict (ADR 0015, section
		// 4.2, point 2): the name is unique in the namespace, so only an
		// alias or a sender can lose. A verdict that says otherwise is a
		// bug of the caller and would give a half-served account.
		if lostSet[a.Address] {
			return nil, fmt.Errorf("conflicted address %q is the primary address of account %q: a primary address never loses", a.Address, a.Name)
		}
		for _, al := range a.Aliases {
			if err := claim(al, a.Name); err != nil {
				return nil, err
			}
		}
	}
	if len(authorized) == 0 {
		return plan, nil
	}

	slices.SortFunc(accounts, func(x, y Account) int { return strings.Compare(x.Name, y.Name) })
	deferred := map[string]bool{}
	for _, a := range accounts {
		suspended := a.Suspended(authorized)
		plan.Users = append(plan.Users, a)
		if !a.Disabled && !suspended {
			plan.Logins = append(plan.Logins, a)
		}
		if !suspended {
			plan.Mailboxes = append(plan.Mailboxes, a.Address)
			if !a.Disabled {
				plan.Senders = append(plan.Senders, Pair{a.Address, a.Name})
			}
		}
		for _, al := range a.Aliases {
			served := slices.Contains(authorized, domainOf(al))
			switch {
			case !served, lostSet[al]:
				// Not served: the domain is not virtual, or the address
				// lost a conflict (it answers 450 below).
			case suspended:
				deferred[al] = true
			default:
				plan.Aliases = append(plan.Aliases, Pair{al, a.Address})
				if !a.Disabled {
					plan.Senders = append(plan.Senders, Pair{al, a.Name})
				}
			}
		}
	}
	for c := range lostSet {
		if slices.Contains(authorized, domainOf(c)) {
			deferred[c] = true
		}
	}
	// A conflicted address is never served, even if an account claims it
	// as an alias (a primary address cannot lose, see above).
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
	for _, al := range a.Aliases {
		if err := validateAddress(al, true); err != nil {
			return fmt.Errorf("account %q: alias: %w", a.Name, err)
		}
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
