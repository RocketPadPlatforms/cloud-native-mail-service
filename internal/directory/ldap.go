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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// probeTimeout bounds the connection and every request of a probe.
const probeTimeout = 10 * time.Second

// NetProber is the Prober that talks to the real directories.
type NetProber struct{}

// LDAPPort is the port of the target: the API leaves it unset for the
// default of the TLS mode.
func LDAPPort(t LDAPTarget) int {
	if t.Port != 0 {
		return t.Port
	}
	if t.TLSMode == LDAPS {
		return 636
	}
	return 389
}

// tlsConfig verifies the server certificate against the CA bundle, or
// the system roots when there is none. ok is false when the bundle holds
// no certificate.
func tlsConfig(host string, ca []byte) (cfg *tls.Config, ok bool) {
	cfg = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if len(ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, false
		}
		cfg.RootCAs = pool
	}
	return cfg, true
}

// isTLSError reports a failed handshake or certificate verification.
func isTLSError(err error) bool {
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
		verification     *tls.CertificateVerificationError
		record           tls.RecordHeaderError
		alert            tls.AlertError
	)
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &verification) ||
		errors.As(err, &record) || errors.As(err, &alert)
}

// ProbeLDAP connects (with the TLS mode of the target), binds, and runs
// the composed filter below the base DN. It must find at least one entry.
func (NetProber) ProbeLDAP(ctx context.Context, t LDAPTarget) Outcome {
	filter, err := ProbeFilter(t.Attributes, t.ActiveFilter)
	if err != nil {
		return InvalidFilter
	}
	conn, closeConn, o := connectLDAP(ctx, t)
	if conn == nil {
		return o
	}
	defer closeConn()
	res, err := conn.Search(ldap.NewSearchRequest(t.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		1, int(probeTimeout/time.Second), false, filter, []string{"1.1"}, nil))
	return searchOutcome(res, err)
}

// connectLDAP connects (with the TLS mode of the target) and binds. A nil
// connection comes with the outcome that says why; otherwise the caller
// closes it with the function that is returned.
func connectLDAP(ctx context.Context, t LDAPTarget) (*ldap.Conn, func(), Outcome) {
	cfg, ok := tlsConfig(t.Host, t.CA)
	if !ok {
		return nil, nil, TLSFailed
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(LDAPPort(t)))
	dialer := &net.Dialer{Timeout: probeTimeout}
	var (
		conn *ldap.Conn
		err  error
	)
	if t.TLSMode == LDAPS {
		conn, err = ldap.DialURL("ldaps://"+addr, ldap.DialWithDialer(dialer), ldap.DialWithTLSConfig(cfg))
	} else {
		conn, err = ldap.DialURL("ldap://"+addr, ldap.DialWithDialer(dialer))
	}
	if err != nil {
		return nil, nil, connectOutcome(err)
	}
	conn.SetTimeout(probeTimeout)
	// The library has no context: close the connection when the
	// reconcile is cancelled, which fails the pending request.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	closeConn := func() {
		stop()
		_ = conn.Close()
	}

	if t.TLSMode == LDAPStartTLS {
		if err := conn.StartTLS(cfg); err != nil {
			closeConn()
			if isTLSError(err) || ldap.IsErrorAnyOf(err, ldap.LDAPResultProtocolError, ldap.LDAPResultUnavailable, ldap.LDAPResultOperationsError) {
				return nil, nil, TLSFailed
			}
			return nil, nil, connectOutcome(err)
		}
	}
	if t.BindDN != "" {
		if err := conn.Bind(t.BindDN, t.BindPassword); err != nil {
			closeConn()
			return nil, nil, bindOutcome(err)
		}
	}
	return conn, closeConn, Reachable
}

// connectOutcome classifies a failure to connect or to start TLS (LDAP
// and PostgreSQL). A typed TLS error is a TLS failure; any other network
// error (refused, unresolvable, timed out) is not, whatever the host is
// called. What is left is matched by the fixed start of the messages of
// the libraries: go-ldap formats a StartTLS handshake error into text,
// crypto/tls has untyped "tls: ..." errors, and pgx wraps the TLS step
// as "tls error: ..." (pgconn v5.12.0, pgconn.go).
func connectOutcome(err error) Outcome {
	if isTLSError(err) {
		return TLSFailed
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return Unreachable
	}
	if hasTLSText(err) {
		return TLSFailed
	}
	return Unreachable
}

// hasTLSText walks the error tree (pgx joins the errors of several
// addresses) for a message that starts with a fixed TLS text. Only the
// start of a message is matched: a host name appears later in it.
func hasTLSText(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "tls: ") || strings.HasPrefix(msg, "tls error: ") ||
		strings.HasPrefix(msg, "TLS handshake failed (") || msg == "server refused TLS connection" {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return hasTLSText(u.Unwrap())
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(u.Unwrap(), hasTLSText)
	}
	return false
}

// bindOutcome classifies a failed bind: a network error is not a
// rejected credential.
func bindOutcome(err error) Outcome {
	if ldap.IsErrorWithCode(err, ldap.ErrorNetwork) {
		return Unreachable
	}
	return BindFailed
}

// searchOutcome classifies the answer to the probe search. A server that
// stops at the size limit of 1 has found an entry.
func searchOutcome(res *ldap.SearchResult, err error) Outcome {
	switch {
	case res != nil && len(res.Entries) > 0:
		return Reachable
	case err == nil, ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject):
		return NoMatchingEntries
	case ldap.IsErrorAnyOf(err, ldap.LDAPResultInsufficientAccessRights, ldap.LDAPResultInvalidCredentials):
		return BindFailed
	default:
		return Unreachable
	}
}
