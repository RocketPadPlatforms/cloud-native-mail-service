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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The SQL of the probe, ADR 0015, section 4.8, point 2. The relation is
// quoted by pgx; nothing else is put into the text. The probe names the
// fixed columns without reading a value (no hash leaves the database).
// The login guard keeps out logins that are not a safe path component
// and the reserved form of service accounts.
const (
	usersProbe = "SELECT 1 FROM %s WHERE login ~ '^[a-z0-9][a-z0-9._-]{0,63}$' AND login !~ '^sa\\.' " +
		"AND mail IS NOT NULL AND password IS NOT NULL LIMIT 1"
	aliasesProbe = "SELECT 1 FROM %s WHERE alias IS NOT NULL AND target IS NOT NULL LIMIT 1"
)

// connConfig builds the connection from the fields of the target, never
// from a connection string that holds the password. The TLS mode is the
// only place that decides about encryption: there are no fallbacks to
// plain text.
func connConfig(t SQLTarget) (*pgx.ConnConfig, bool) {
	cfg, err := pgx.ParseConfig("sslmode=disable")
	if err != nil {
		return nil, false
	}
	cfg.Host, cfg.Port = t.Host, uint16(t.Port) //nolint:gosec // the API limits the port to 1..65535
	cfg.Database, cfg.User, cfg.Password = t.Database, t.Username, t.Password
	cfg.ConnectTimeout = probeTimeout
	cfg.Fallbacks = nil
	switch t.TLSMode {
	case SQLDisable:
		cfg.TLSConfig = nil
	case SQLRequire:
		// "require" encrypts without verifying the host name, like libpq.
		// With a CA bundle libpq checks the chain as well (verify-ca; the
		// root certificate is passed as PGSSLROOTCERT, ADR 0015, 4.8): "if a
		// root CA file exists, the behavior of sslmode=require will be the
		// same as that of verify-ca"
		// (https://www.postgresql.org/docs/current/libpq-ssl.html).
		cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // explicit choice of the user; the chain is checked below when there is a CA
		if len(t.CA) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(t.CA) {
				return nil, false
			}
			cfg.TLSConfig.VerifyConnection = verifyChain(pool)
		}
	default:
		tc, ok := tlsConfig(t.Host, t.CA)
		if !ok {
			return nil, false
		}
		cfg.TLSConfig = tc
	}
	return cfg, true
}

// verifyChain checks the certificate chain of the server against the
// pool without a host name (verify-ca).
func verifyChain(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("tls: the server sent no certificate")
		}
		opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
		for _, c := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(c)
		}
		_, err := cs.PeerCertificates[0].Verify(opts)
		return err
	}
}

// ProbeSQL connects and reads one row of the users relation with the
// login guard of the operator, and checks that the aliases relation (if
// any) can be read. A users relation without a row is NoMatchingEntries;
// an empty aliases relation is fine.
func (NetProber) ProbeSQL(ctx context.Context, t SQLTarget) Outcome {
	cfg, ok := connConfig(t)
	if !ok {
		return TLSFailed
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return sqlOutcome(err)
	}
	defer func() {
		// ctx may be cancelled or expired here; give the close its own
		// short deadline so it can neither be skipped nor hang.
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = conn.Close(closeCtx)
	}()

	var one int
	err = conn.QueryRow(ctx, sqlText(usersProbe, t.Users)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return NoMatchingEntries
	}
	if err != nil {
		return sqlOutcome(err)
	}
	if t.Aliases != nil {
		err = conn.QueryRow(ctx, sqlText(aliasesProbe, *t.Aliases)).Scan(&one)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return sqlOutcome(err)
		}
	}
	return Reachable
}

// sqlText puts the quoted relation into the probe text.
func sqlText(format string, r Relation) string {
	return strings.Replace(format, "%s", pgx.Identifier{r.Schema, r.Name}.Sanitize(), 1)
}

// sqlOutcome classifies a failure to connect or to query. A server error
// is classified by its SQLSTATE; anything else by connectOutcome.
func sqlOutcome(err error) Outcome {
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch {
		case pe.Code == "42P01" || pe.Code == "3F000" || pe.Code == "42703":
			// undefined table, invalid schema name, undefined column
			return RelationMissing
		case strings.HasPrefix(pe.Code, "28") || pe.Code == "42501":
			// invalid authorization, insufficient privilege
			return BindFailed
		}
		return Unreachable
	}
	return connectOutcome(err)
}
