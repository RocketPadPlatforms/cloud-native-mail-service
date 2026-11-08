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

package directory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/jackc/pgx/v5"
)

// SenderLooker asks a directory whether an address already belongs to an
// account of it (ADR 0015, section 4.9, point 3): the sender of a service
// account must not be an address of a directory user. The outcome is
// Reachable when the lookup ran, whatever it found; any other outcome says
// why it did not run, and owned is false then. Like a probe it returns no
// error: the texts of the drivers can carry a credential.
type SenderLooker interface {
	OwnsLDAP(ctx context.Context, t LDAPTarget, address string) (owned bool, o Outcome)
	OwnsSQL(ctx context.Context, t SQLTarget, address string) (owned bool, o Outcome)
}

var _ SenderLooker = NetProber{}

// senderLDAPFilter is the filter of the sender lookup of Postfix (BySender,
// the same restriction and guards as every other lookup) with the address
// escaped into the key.
func senderLDAPFilter(t LDAPTarget, address string) (string, error) {
	filter, err := LookupFilter(t.Attributes, t.ActiveFilter, BySender, KeyPostfix)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(filter, KeyPostfix, ldap.EscapeFilter(strings.ToLower(address))), nil
}

// OwnsLDAP connects, binds and searches the composed sender filter below the
// base DN.
func (NetProber) OwnsLDAP(ctx context.Context, t LDAPTarget, address string) (bool, Outcome) {
	filter, err := senderLDAPFilter(t, address)
	if err != nil {
		return false, InvalidFilter
	}
	conn, closeConn, o := connectLDAP(ctx, t)
	if conn == nil {
		return false, o
	}
	defer closeConn()
	res, err := conn.Search(ldap.NewSearchRequest(t.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		1, int(probeTimeout/time.Second), false, filter, []string{"1.1"}, nil))
	if res != nil && len(res.Entries) > 0 {
		return true, Reachable
	}
	if o := searchOutcome(res, err); o != NoMatchingEntries {
		return false, o
	}
	return false, Reachable
}

// SQLSenderQuery is the one builder of the sender query of a PostgreSQL
// directory (ADR 0015, section 4.4): the accounts whose mail column, or an
// alias of an account that passes the login guard, is the key. The Postfix
// table selects the login of the owner (owner true) with the placeholder of
// Postfix as key ('%s'); the lookup of a service account selects 1, with a
// query parameter ($1) as key. The key is put in as it is: the caller passes
// one of those two forms, never data.
func SQLSenderQuery(t SQLTarget, owner bool, key string) string {
	first, second := "1", "1"
	if owner {
		first, second = "login", "u.login"
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE lower(mail) = %s AND %s", first, t.Users.Quoted(), key, SQLLoginGuard)
	if t.Aliases != nil {
		q += fmt.Sprintf(" UNION SELECT %s FROM %s a JOIN %s u ON lower(u.mail) = lower(a.target) WHERE lower(a.alias) = %s AND %s",
			second, t.Aliases.Quoted(), t.Users.Quoted(), key, SQLLoginGuardOn("u.login"))
	}
	return q
}

// senderSQL is the query of the sender lookup of a service account.
func senderSQL(t SQLTarget) string {
	return SQLSenderQuery(t, false, "$1") + " LIMIT 1"
}

// OwnsSQL connects and runs the sender query.
func (NetProber) OwnsSQL(ctx context.Context, t SQLTarget, address string) (bool, Outcome) {
	cfg, ok := connConfig(t)
	if !ok {
		return false, TLSFailed
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return false, sqlOutcome(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = conn.Close(closeCtx)
	}()
	var one int
	err = conn.QueryRow(ctx, senderSQL(t), strings.ToLower(address)).Scan(&one)
	switch {
	case err == nil:
		return true, Reachable
	case errors.Is(err, pgx.ErrNoRows):
		return false, Reachable
	}
	return false, sqlOutcome(err)
}
