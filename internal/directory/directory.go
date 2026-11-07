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

// Package directory checks an external user directory (a MailDirectory,
// ADR 0015, section 5): the filter the operator composes for LDAP and
// the probe that connects to LDAP or PostgreSQL. The probe sits behind
// the Prober interface, so the controller tests run with a fake and
// never touch the network.
package directory

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// Outcome is the result of a probe. The values are the reasons of the
// Reachable condition. A probe returns an outcome and never an error:
// the error texts of the drivers can carry a connection string or a bind
// DN, so they are not passed on (ADR 0015, section 4.8, point 6).
type Outcome string

// The outcomes of a probe.
const (
	// Reachable means the connection, the bind and the lookup worked
	// and the composed filter found an account.
	Reachable Outcome = "Reachable"
	// Unreachable means the server did not answer in time or refused
	// the connection.
	Unreachable Outcome = "Unreachable"
	// BindFailed means the server rejected the credentials.
	BindFailed Outcome = "BindFailed"
	// TLSFailed means the TLS handshake or the verification of the
	// server certificate failed.
	TLSFailed Outcome = "TLSFailed"
	// InvalidFilter means the filter is not a valid LDAP filter.
	InvalidFilter Outcome = "InvalidFilter"
	// NoMatchingEntries means the lookup worked and found no account.
	NoMatchingEntries Outcome = "NoMatchingEntries"
	// RelationMissing means a table, a view, a schema or a fixed column
	// of the SQL directory does not exist.
	RelationMissing Outcome = "RelationMissing"
)

// TLS modes of the API (LDAPTLS.Mode and SQLTLS.Mode).
const (
	LDAPStartTLS = "StartTLS"
	LDAPS        = "LDAPS"
	LDAPNoTLS    = "None"

	SQLVerifyFull = "verify-full"
	SQLRequire    = "require"
	SQLDisable    = "disable"
)

// Defaults of the LDAP attributes (the API defaults).
const (
	DefaultLogin = "uid"
	DefaultMail  = "mail"
)

// Attributes names the LDAP attributes of an account. Aliases is empty
// when the directory has none.
type Attributes struct {
	Login   string
	Mail    string
	Aliases string
}

// LDAPTarget is everything the LDAP probe needs, with the secret
// values already read.
type LDAPTarget struct {
	Host         string
	Port         int
	TLSMode      string
	CA           []byte // PEM bundle; empty means the system roots
	BaseDN       string
	BindDN       string // empty means an anonymous bind
	BindPassword string
	Attributes   Attributes
	ActiveFilter string // empty means no restriction
}

// Relation is a table or a view of the SQL directory.
type Relation struct {
	Schema string
	Name   string
}

// SQLTarget is everything the PostgreSQL probe needs, with the secret
// values already read. Aliases is nil when the directory has none.
type SQLTarget struct {
	Host     string
	Port     int
	TLSMode  string
	CA       []byte // PEM bundle; empty means the system roots
	Database string
	Username string
	Password string
	Users    Relation
	Aliases  *Relation
}

// Prober connects to a directory and reports how that went.
type Prober interface {
	ProbeLDAP(ctx context.Context, t LDAPTarget) Outcome
	ProbeSQL(ctx context.Context, t SQLTarget) Outcome
}

// attributeRe is the pattern of the API for an attribute name; it is
// checked again here because the name goes into a filter.
var attributeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)

// ValidCA reports whether a CA bundle holds at least one certificate.
func ValidCA(pem []byte) bool {
	_, ok := tlsConfig("", pem)
	return ok
}

// ValidActiveFilter checks the static restriction of the API: it must
// be one complete LDAP filter. An empty filter is valid (no restriction).
// Compiling it alone also catches a filter that only works in the
// composition, such as "(a=b))(|(c=d)".
func ValidActiveFilter(active string) error {
	if active == "" {
		return nil
	}
	if _, err := ldap.CompileFilter(active); err != nil {
		return fmt.Errorf("the active filter is not a valid LDAP filter: %w", err)
	}
	return nil
}

// ProbeFilter is the one builder of the composed LDAP lookup filter
// `(&<active>(<login>=*)(<mail>=*)(!(<login>=.*))(!(<login>=*/*))(!(<login>=sa.*)))`:
// the probe uses it, and the Postfix and Dovecot renderers must build on
// it (a key clause in place of the two presence tests) and not compose
// the restriction and the guards a second time. It matches every account that passes the active restriction, has a login
// and a primary address and is not excluded by the guards of ADR 0015,
// section 4.8, point 2. The
// login becomes a path component of the mailbox, so a login with a
// leading dot, with a slash or in the reserved form of service accounts
// ("sa.") is never an account. The probe must find at least one entry
// with exactly this filter: a directory whose login attribute cannot do
// substring matching matches nothing, and that shows up as
// NoMatchingEntries instead of as a silent 550.
func ProbeFilter(a Attributes, active string) (string, error) {
	if !attributeRe.MatchString(a.Login) || !attributeRe.MatchString(a.Mail) ||
		(a.Aliases != "" && !attributeRe.MatchString(a.Aliases)) {
		return "", fmt.Errorf("an attribute name is not valid")
	}
	if err := ValidActiveFilter(active); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(&")
	b.WriteString(active)
	fmt.Fprintf(&b, "(%[1]s=*)(%[2]s=*)(!(%[1]s=.*))(!(%[1]s=*/*))(!(%[1]s=sa.*))", a.Login, a.Mail)
	b.WriteString(")")
	filter := b.String()
	if _, err := ldap.CompileFilter(filter); err != nil {
		return "", fmt.Errorf("the composed filter is not a valid LDAP filter: %w", err)
	}
	return filter, nil
}
