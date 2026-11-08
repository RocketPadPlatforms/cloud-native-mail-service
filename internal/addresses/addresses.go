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

// Package addresses decides who owns the addresses of a mail server: the
// pure function behind ADR 0015, section 3 (point 6) and section 4. It
// takes every claimant of the address space of one server (the primary
// address and the aliases of its accounts, the sender of a service
// account), the state of the domains they name, and returns one verdict
// per address: the winner, the addresses nobody serves, the reason for
// each row of status.addresses[], and the list of conflicted addresses
// that the Postfix renderer needs (render.RenderRequest.ConflictedAddresses).
//
// It is a package of its own, with no import of the API types or the
// renderers, so that the MailUser controller and the MailServer controller
// can both call it and reach the same verdict (the MailUser controller
// writes it, the MailServer controller renders it) without one importing
// the other's dependencies. It reads nothing but its input and has no
// clock: the same objects give the same answer, in any order.
package addresses

import (
	"slices"
	"strings"
)

// Rank orders the claimants of an address (ADR 0015, section 4.2, point 1).
// A claimant of a lower rank (a higher number) loses the address to any
// claimant of a higher rank (a lower number). Claimants of the same rank
// all lose it: creation time does not survive a restore (ADR 0003).
type Rank int

// The ranks of ADR 0015, section 4.2.
const (
	// RankPrimary is the primary address of an account: a MailUser or a
	// static test user. The name is unique in the namespace, so primaries
	// cannot collide, and one always wins.
	RankPrimary Rank = 1
	// RankAlias is an alias of a MailUser.
	RankAlias Rank = 2
	// RankSender is the spec.sender of a MailServiceAccount (3c).
	RankSender Rank = 3
)

// String names the rank for the message of a conflict.
func (r Rank) String() string {
	switch r {
	case RankPrimary:
		return "primary address"
	case RankAlias:
		return "alias"
	case RankSender:
		return "service account sender"
	}
	return "unknown rank"
}

// withArticle is String with the indefinite article, for a sentence.
func (r Rank) withArticle() string {
	if r == RankAlias {
		return "an " + r.String()
	}
	return "a " + r.String()
}

// Kind says whether a row of status.addresses[] is the primary address or
// an alias. The values are those of the API.
type Kind string

// The kinds of a row.
const (
	KindPrimary Kind = "primary"
	KindAlias   Kind = "alias"
)

// DomainState is what the caller found out about a domain a claimant
// names: whether the MailDomain exists, whether this server serves it
// (ADR 0014) and whether it is authorized for the namespace (ADR 0003).
// The checks run in this order, and the first that fails is the state.
type DomainState int

// The states of a domain.
const (
	// DomainNotFound means there is no such MailDomain in the namespace. It
	// is the zero value, so a domain nobody filled in fails closed.
	DomainNotFound DomainState = iota
	// DomainOK means the MailDomain exists, is served by this server and
	// is authorized.
	DomainOK
	// DomainNotServed means the MailDomain names another server.
	DomainNotServed
	// DomainNotAuthorized means the domain is not authorized (revoked,
	// not granted, a duplicate, a public suffix).
	DomainNotAuthorized
)

// Domain is a domain a claimant names. Name is the DNS name
// (MailDomain.spec.domain), empty when the MailDomain was not found.
type Domain struct {
	Name  string
	State DomainState
}

// Address is a local part in a domain. Domain is the key into
// Input.Domains: the name of the MailDomain object for a MailUser, the
// domain name itself for a static test user (which has no MailDomain
// reference).
type Address struct {
	Local  string
	Domain string
}

// User is an account with a primary address and aliases: a MailUser, or a
// static test user (no aliases). Name is unique in the server.
type User struct {
	Name     string
	Disabled bool
	Primary  Address
	Aliases  []Address
}

// Sender is the address a service account may send as (ADR 0015, section
// 4.9, 3c). Owner identifies the account across namespaces
// (namespace/name); two claims of one owner are not a conflict.
type Sender struct {
	Owner   string
	Address Address
}

// Input is everything the decision reads: all claimants of one server and
// the state of every domain they name. A domain that is not in Domains is
// DomainNotFound.
type Input struct {
	Users   []User
	Senders []Sender
	Domains map[string]Domain
}

// The reasons of a row (ADR 0015, sections 4.1, 4.2, 4.6 and 8). The
// first five are the ones the ADR lists for status.addresses[]; the three
// domain reasons are the reasons of a MailUser whose primary domain fails
// (Ready=False) and are used for the primary row and for the aliases of a
// suspended account; SenderConflict is the reason of a service account.
const (
	ReasonServed = "Served"

	ReasonAliasDomainNotFound      = "AliasDomainNotFound"
	ReasonAliasDomainNotServed     = "AliasDomainNotServed"
	ReasonAliasDomainNotAuthorized = "AliasDomainNotAuthorized"
	ReasonAddressConflict          = "AddressConflict"

	ReasonDomainNotFound      = "DomainNotFound"
	ReasonDomainNotServed     = "DomainNotServed"
	ReasonDomainNotAuthorized = "DomainNotAuthorized"

	ReasonSenderConflict = "SenderConflict"
)

// Row is the verdict for one address: a row of status.addresses[] and what
// the renderers need to know about it.
type Row struct {
	// Address is local@domain, lower case.
	Address string
	Kind    Kind
	// Served is true when mail for the address is accepted.
	Served bool
	// Reason is ReasonServed or why the address is not served.
	Reason string
	// Detail explains a conflict: the address and the rank of the
	// claimants, never their names (a claimant may be in another
	// namespace).
	Detail string
	// Deferred means mail for the address answers a temporary failure
	// (450 4.2.1) instead of a refusal: a conflict nobody won, and the
	// alias of a suspended account in a domain that is still served.
	Deferred bool
	// MaySend is true when the owner may use the address as a sender: it
	// is served and the account is not disabled.
	MaySend bool
}

// UserResult is the verdict for one account.
type UserResult struct {
	Name string
	// Suspended is true when the primary domain fails: no login, no
	// sending, no new delivery, and its aliases are deferred (ADR 0015,
	// section 4.6). Reason is then the reason of the domain.
	Suspended bool
	Reason    string
	// Rows are the primary address first, then the aliases by address; one
	// row per distinct address. They are status.addresses[].
	Rows []Row
}

// PartiallyServed reports whether any address of the account is not
// served: the AddressesServed warning of the MailUser (ADR 0015, section 8).
func (u UserResult) PartiallyServed() bool {
	return slices.ContainsFunc(u.Rows, func(r Row) bool { return !r.Served })
}

// Aliases are the aliases the account still claims: those in a domain that
// is ok and that did not lose a conflict, the aliases of a suspended
// account included. They are the Aliases of render.Account; the renderer
// serves them, or defers those of a suspended account. An alias in a domain
// that fails has no address the renderer could use (the domain may not even
// exist), and one that lost a conflict is not the account's.
func (u UserResult) Aliases() []string {
	var out []string
	for _, r := range u.Rows {
		if r.Kind == KindAlias && r.Reason != ReasonAddressConflict && !strings.HasPrefix(r.Reason, "AliasDomain") {
			out = append(out, r.Address)
		}
	}
	return out
}

// SenderResult is the verdict for the sender of a service account.
type SenderResult struct {
	Owner string
	Row   Row
}

// Result is the verdict for a whole server.
type Result struct {
	// Users and Senders are sorted by name and by owner and address.
	Users   []UserResult
	Senders []SenderResult
	// Conflicted are the addresses in a served domain that nobody owns
	// because the best claimants tie: all lose, and the address answers
	// 450 until someone fixes it. An address that a higher rank won is not
	// here: its winner serves it. It is the ConflictedAddresses of the
	// render request.
	Conflicted []string
	// Deferred are all addresses that answer 450: the conflicted ones and
	// the aliases of suspended accounts. It is the recipient_defer table.
	Deferred []string
}

// claim is one claim on an address, resolved.
type claim struct {
	owner string
	rank  Rank
}

// resolved is a domain that has been looked up.
type resolved struct {
	name  string
	state DomainState
}

func (in *Input) resolve(ref string) resolved {
	d, ok := in.Domains[ref]
	if !ok {
		return resolved{state: DomainNotFound}
	}
	return resolved{name: strings.ToLower(d.Name), state: d.State}
}

// text is the lower-case address. A domain that was not found has no name:
// the reference stands in, so the address of such a row reads
// local@<reference> (it is no address the renderers can use, and
// UserResult.Aliases leaves such rows out).
func text(a Address, d resolved) string {
	name := d.name
	if name == "" {
		name = strings.ToLower(a.Domain)
	}
	return strings.ToLower(a.Local) + "@" + name
}

// outcome is what the ranking decided for one address.
type outcome struct {
	winner string // owner, or "" when nobody won
	tied   bool   // several owners share the best rank: nobody won
	rank   Rank   // the best rank among the claimants
	owners int    // distinct owners
}

// Decide computes the verdict for the address space of one server. Only a
// claim in a domain that is OK takes part in the ranking: a claim in a
// domain that is revoked cannot hold an address, and so cannot take it
// from anyone. The ranking is by rank, then by owner: the best rank wins
// if one owner has it, otherwise every claimant loses. Claims of one owner
// on one address collapse. The ordering of the input does not matter, and
// the input is not changed.
//
// An address is keyed by its folded DNS name (local@domain.name), not by
// the name of the MailDomain object as ADR 0015, section 4.2, point 5
// writes. Only a claim in a domain that is served and authorized is ranked,
// ADR 0003 allows at most one authorized object per DNS name in a
// namespace, so for valid input the two keys select the same claimants; and
// a static test user has a DNS name and no MailDomain object. Two objects
// that name one DNS name give one address, whatever their states.
func Decide(in Input) Result {
	claims := map[string][]claim{}
	add := func(owner string, rank Rank, a Address, d resolved) {
		if d.state != DomainOK {
			return
		}
		k := text(a, d)
		claims[k] = append(claims[k], claim{owner, rank})
	}
	for _, u := range in.Users {
		owner := "user/" + u.Name
		add(owner, RankPrimary, u.Primary, in.resolve(u.Primary.Domain))
		for _, a := range u.Aliases {
			add(owner, RankAlias, a, in.resolve(a.Domain))
		}
	}
	for _, s := range in.Senders {
		add("sender/"+s.Owner, RankSender, s.Address, in.resolve(s.Address.Domain))
	}

	outcomes := make(map[string]outcome, len(claims))
	for k, cs := range claims {
		outcomes[k] = rankClaims(cs)
	}

	var res Result
	deferred := map[string]bool{}
	for _, u := range sortedUsers(in.Users) {
		res.Users = append(res.Users, userResult(&in, u, outcomes, deferred))
	}
	for _, s := range sortedSenders(in.Senders) {
		d := in.resolve(s.Address.Domain)
		row := Row{Address: text(s.Address, d)}
		row.Served, row.Reason, row.Detail, row.Deferred = judge(d, "sender/"+s.Owner, row.Address, outcomes, ReasonSenderConflict, domainReason)
		row.MaySend = row.Served
		res.Senders = append(res.Senders, SenderResult{Owner: s.Owner, Row: row})
		if row.Deferred {
			deferred[row.Address] = true
		}
	}
	for k, o := range outcomes {
		if o.tied {
			res.Conflicted = append(res.Conflicted, k)
			deferred[k] = true
		}
	}
	slices.Sort(res.Conflicted)
	for k := range deferred {
		res.Deferred = append(res.Deferred, k)
	}
	slices.Sort(res.Deferred)
	return res
}

// rankClaims applies ADR 0015, section 4.2, point 1 to the claims on one
// address.
func rankClaims(cs []claim) outcome {
	o := outcome{rank: cs[0].rank}
	for _, c := range cs[1:] {
		o.rank = min(o.rank, c.rank)
	}
	seen := map[string]bool{}
	best := map[string]bool{}
	for _, c := range cs {
		seen[c.owner] = true
		if c.rank == o.rank {
			best[c.owner] = true
		}
	}
	o.owners = len(seen)
	if len(best) == 1 {
		for owner := range best {
			o.winner = owner
		}
	} else {
		o.tied = true
	}
	return o
}

// domainReason maps a failed domain to the reason of a row, for the
// primary address and the sender.
func domainReason(s DomainState) string {
	switch s {
	case DomainNotFound:
		return ReasonDomainNotFound
	case DomainNotServed:
		return ReasonDomainNotServed
	}
	return ReasonDomainNotAuthorized
}

// aliasDomainReason is domainReason for an alias.
func aliasDomainReason(s DomainState) string {
	switch s {
	case DomainNotFound:
		return ReasonAliasDomainNotFound
	case DomainNotServed:
		return ReasonAliasDomainNotServed
	}
	return ReasonAliasDomainNotAuthorized
}

// judge decides whether owner holds the address in a domain, and why not.
// A domain that fails comes first, then the conflict.
func judge(d resolved, owner, address string, outcomes map[string]outcome, conflict string, domain func(DomainState) string) (served bool, reason, detail string, deferred bool) {
	if d.state != DomainOK {
		return false, domain(d.state), "", false
	}
	o := outcomes[address]
	if o.winner == owner {
		return true, ReasonServed, "", false
	}
	if o.tied {
		return false, conflict, "the address is claimed by several accounts of the rank " + o.rank.String() + ": none of them has it", true
	}
	return false, conflict, "the address belongs to " + o.rank.withArticle() + " of another account", false
}

func userResult(in *Input, u User, outcomes map[string]outcome, deferred map[string]bool) UserResult {
	owner := "user/" + u.Name
	pd := in.resolve(u.Primary.Domain)
	res := UserResult{Name: u.Name}
	if pd.state != DomainOK {
		res.Suspended, res.Reason = true, domainReason(pd.state)
	}

	primary := Row{Address: text(u.Primary, pd), Kind: KindPrimary}
	primary.Served, primary.Reason, primary.Detail, primary.Deferred = judge(pd, owner, primary.Address, outcomes, ReasonAddressConflict, domainReason)
	primary.MaySend = primary.Served && !u.Disabled
	rows := map[string]candidate{primary.Address: {row: primary, ok: pd.state == DomainOK}}

	for _, a := range u.Aliases {
		d := in.resolve(a.Domain)
		addr := text(a, d)
		row := Row{Address: addr, Kind: KindAlias}
		row.Served, row.Reason, row.Detail, row.Deferred = judge(d, owner, addr, outcomes, ReasonAddressConflict, aliasDomainReason)
		if d.state == DomainOK && res.Suspended && row.Reason != ReasonAddressConflict {
			// The domain of the alias is served but the account is not:
			// mail is deferred until the primary domain is back (ADR
			// 0015, section 4.6), and the reason is the cause.
			row.Served, row.Reason, row.Deferred = false, res.Reason, true
		}
		row.MaySend = row.Served && !u.Disabled
		// The same address twice (two aliases, or two objects that name
		// one DNS name): the primary row stays, and among aliases the
		// better candidate, by a total order, so that the order of the
		// aliases does not matter.
		c := candidate{row: row, ok: d.state == DomainOK}
		if prev, dup := rows[addr]; dup && (addr == primary.Address || !c.betterThan(prev)) {
			continue
		}
		rows[addr] = c
	}

	res.Rows = append(res.Rows, primary)
	aliases := make([]string, 0, len(rows))
	for addr := range rows {
		if addr != primary.Address {
			aliases = append(aliases, addr)
		}
	}
	slices.Sort(aliases)
	for _, addr := range aliases {
		res.Rows = append(res.Rows, rows[addr].row)
		if rows[addr].row.Deferred {
			deferred[addr] = true
		}
	}
	return res
}

// candidate is a row for an address that an account names more than once,
// with whether its domain is ok.
type candidate struct {
	row Row
	ok  bool
}

// betterThan is a total order over the candidates for one address: a served
// row beats one that is not, a row in a domain that is ok beats one in a
// domain that fails, then the reason and the detail by their text.
func (c candidate) betterThan(o candidate) bool {
	switch {
	case c.row.Served != o.row.Served:
		return c.row.Served
	case c.ok != o.ok:
		return c.ok
	case c.row.Reason != o.row.Reason:
		return c.row.Reason < o.row.Reason
	}
	return c.row.Detail < o.row.Detail
}

func sortedUsers(us []User) []User {
	out := slices.Clone(us)
	slices.SortStableFunc(out, func(a, b User) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func sortedSenders(ss []Sender) []Sender {
	out := slices.Clone(ss)
	slices.SortStableFunc(out, func(a, b Sender) int {
		return strings.Compare(a.Owner+"\x00"+senderText(a), b.Owner+"\x00"+senderText(b))
	})
	return out
}

func senderText(s Sender) string {
	return strings.ToLower(s.Address.Local) + "@" + strings.ToLower(s.Address.Domain)
}
