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
	"slices"
	"strings"
)

// Entry is one account as the engines carry it: the data of a line of the
// Dovecot files and of the Postfix tables, without any engine syntax.
type Entry struct {
	Name string
	// Disabled and Suspended both take the login away (ADR 0015, section
	// 3, point 5); the account stays in the userdb.
	Disabled  bool
	Suspended bool
	// Credential is the password hash. A change of it is a password change.
	Credential string
	Primary    string
	// Aliases are the served aliases, sorted.
	Aliases []string
}

// CanLogin reports whether the account has a line in the passdb file.
func (e Entry) CanLogin() bool { return !e.Disabled && !e.Suspended }

// ChangeKind names a change between two revisions of the accounts.
type ChangeKind string

// The changes. The first three are the ones that a blocked revision still
// applies (ADR 0015, section 4.7): they take a login away or keep it
// working. UserRemoved and LoginDisabled are about the passdb file alone: a
// blocked revision keeps the account in the frozen Postfix maps and in the
// userdb, so it still receives mail, and only its login is gone. The others
// add, or change what Postfix and Rspamd know, and wait.
const (
	UserRemoved     ChangeKind = "UserRemoved"
	LoginDisabled   ChangeKind = "LoginDisabled"
	PasswordChanged ChangeKind = "PasswordChanged"

	UserAdded      ChangeKind = "UserAdded"
	LoginEnabled   ChangeKind = "LoginEnabled"
	PrimaryChanged ChangeKind = "PrimaryChanged"
	AliasAdded     ChangeKind = "AliasAdded"
	AliasRemoved   ChangeKind = "AliasRemoved"
)

// Change is one difference between the running revision and the desired
// one. Address is set for the alias and primary changes.
type Change struct {
	Kind    ChangeKind
	Name    string
	Address string
}

// applies reports whether a blocked revision still applies the change.
func (k ChangeKind) applies() bool {
	switch k {
	case UserRemoved, LoginDisabled, PasswordChanged:
		return true
	}
	return false
}

// diff lists the changes from prev to next, sorted by name, kind and
// address. A changed primary address is one change, PrimaryChanged: the
// ADR calls it a delete and a new user, and it adds a line.
func diff(prev, next []Entry) []Change {
	pm, nm := byName(prev), byName(next)
	var out []Change
	for name, p := range pm {
		n, ok := nm[name]
		if !ok {
			out = append(out, Change{UserRemoved, name, ""})
			continue
		}
		switch {
		case p.CanLogin() && !n.CanLogin():
			out = append(out, Change{LoginDisabled, name, ""})
		case !p.CanLogin() && n.CanLogin():
			out = append(out, Change{LoginEnabled, name, ""})
		case p.CanLogin() && n.CanLogin() && p.Credential != n.Credential:
			out = append(out, Change{PasswordChanged, name, ""})
		}
		if p.Primary != n.Primary {
			out = append(out, Change{PrimaryChanged, name, n.Primary})
		}
		for _, a := range n.Aliases {
			if !slices.Contains(p.Aliases, a) {
				out = append(out, Change{AliasAdded, name, a})
			}
		}
		for _, a := range p.Aliases {
			if !slices.Contains(n.Aliases, a) {
				out = append(out, Change{AliasRemoved, name, a})
			}
		}
	}
	for name := range nm {
		if _, ok := pm[name]; !ok {
			out = append(out, Change{UserAdded, name, ""})
		}
	}
	slices.SortFunc(out, func(a, b Change) int {
		return strings.Compare(a.Name+"\x00"+string(a.Kind)+"\x00"+a.Address, b.Name+"\x00"+string(b.Kind)+"\x00"+b.Address)
	})
	return out
}

func byName(es []Entry) map[string]Entry {
	m := make(map[string]Entry, len(es))
	for _, e := range es {
		m[e.Name] = e
	}
	return m
}

// Settled is what a revision carries, given whether it is blocked.
type Settled struct {
	// Maps are the accounts the Postfix tables and the Rspamd data are
	// rendered from. Blocked, they are the running revision, unchanged.
	Maps []Entry
	// Mailboxes are the accounts of the userdb file. Blocked, the running
	// revision: no line is added, and none is dropped, because the frozen
	// Postfix maps still accept the mail.
	Mailboxes []Entry
	// Logins are the accounts of the passdb file. Blocked, only the lines
	// that are already in it, with the current password, and none for an
	// account that is removed, disabled or suspended.
	Logins []Entry
	// Applied and Held are the changes of the revision that took effect
	// and that wait for the blocking input to be fixed. Blocked, an
	// applied UserRemoved or LoginDisabled takes the login away (the
	// passdb line) and nothing else: the account stays in Maps and
	// Mailboxes.
	Applied, Held []Change
}

// Settle applies ADR 0015, section 4.7, "what a blocked revision blocks".
// Not blocked, the desired accounts are the revision. Blocked (a limit
// refused the new revision), the revision still removes access and keeps
// logins current, and adds nothing: a new account, a new alias, the removal
// of an alias (the frozen valias and sender_login still list it), a changed
// primary address and the re-enabling of a login all wait.
//
// The domain revocation of ADR 0003 is not a blocked revision: its
// fail-closed render has no domains and no users, no limit applies to it,
// and the caller passes blocked=false with an empty desired set.
func Settle(prev, next []Entry, blocked bool) Settled {
	changes := diff(prev, next)
	if !blocked {
		return Settled{
			Maps:      sortedEntries(next),
			Mailboxes: sortedEntries(next),
			Logins:    loginsOf(next),
			Applied:   changes,
		}
	}
	s := Settled{Maps: sortedEntries(prev), Mailboxes: sortedEntries(prev)}
	nm := byName(next)
	for _, p := range sortedEntries(prev) {
		n, ok := nm[p.Name]
		if !p.CanLogin() || !ok || !n.CanLogin() {
			continue
		}
		p.Credential = n.Credential
		s.Logins = append(s.Logins, p)
	}
	for _, c := range changes {
		if c.Kind.applies() {
			s.Applied = append(s.Applied, c)
		} else {
			s.Held = append(s.Held, c)
		}
	}
	return s
}

func sortedEntries(es []Entry) []Entry {
	out := slices.Clone(es)
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func loginsOf(es []Entry) []Entry {
	var out []Entry
	for _, e := range sortedEntries(es) {
		if e.CanLogin() {
			out = append(out, e)
		}
	}
	return out
}
