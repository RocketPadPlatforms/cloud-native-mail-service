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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	lookupUser = "lookup"
	testDB     = "mail"
)

var uidMail = Attributes{Login: DefaultLogin, Mail: DefaultMail}

func TestProbeFilter(t *testing.T) {
	for name, tc := range map[string]struct {
		attrs  Attributes
		active string
		want   string
	}{
		"no restriction": {
			attrs: uidMail,
			want:  "(&(uid=*)(mail=*)(!(uid=.*))(!(uid=*/*))(!(uid=sa.*)))",
		},
		"restriction": {
			attrs:  Attributes{Login: "sAMAccountName", Mail: DefaultMail, Aliases: "mailAlias"},
			active: "(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))",
			want:   "(&(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))(sAMAccountName=*)(mail=*)(!(sAMAccountName=.*))(!(sAMAccountName=*/*))(!(sAMAccountName=sa.*)))",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ProbeFilter(tc.attrs, tc.active)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("filter = %q, want %q", got, tc.want)
			}
			if _, err := ldap.CompileFilter(got); err != nil {
				t.Errorf("the composed filter does not compile: %v", err)
			}
		})
	}
}

func TestLookupFilter(t *testing.T) {
	const active = "(objectClass=PostfixBookMailAccount)"
	attrs := Attributes{Login: DefaultLogin, Mail: DefaultMail, Aliases: "mailAlias"}
	const guards = "(uid=*)(mail=*)(!(uid=.*))(!(uid=*/*))(!(uid=sa.*)))"
	for name, tc := range map[string]struct {
		by   Lookup
		key  string
		want string
	}{
		"mail, Postfix":   {ByMail, KeyPostfix, "(&" + active + "(mail=%s)" + guards},
		"alias, Postfix":  {ByAlias, KeyPostfix, "(&" + active + "(mailAlias=%s)" + guards},
		"login, Dovecot":  {ByLogin, KeyDovecot, "(&" + active + "(|(uid=%{user})(mail=%{user}))" + guards},
		"sender, Postfix": {BySender, KeyPostfix, "(&" + active + "(|(mail=%s)(mailAlias=%s))" + guards},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LookupFilter(attrs, active, tc.by, tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("filter = %q, want %q", got, tc.want)
			}
		})
	}

	// The probe and the lookups share the restriction and the guards:
	// without the key clause the lookup is the probe filter.
	probe, err := ProbeFilter(attrs, active)
	if err != nil {
		t.Fatal(err)
	}
	if want := "(&" + active + guards; probe != want {
		t.Errorf("probe filter = %q, want %q", probe, want)
	}

	for name, tc := range map[string]struct {
		attrs Attributes
		by    Lookup
		key   string
	}{
		"free text key":       {attrs, ByMail, "alice@example.com"},
		"key with a filter":   {attrs, ByMail, "%s)(uid=*"},
		"empty key":           {attrs, ByMail, ""},
		"alias without attr":  {uidMail, ByAlias, KeyPostfix},
		"unknown lookup":      {attrs, Lookup(9), KeyPostfix},
		"attribute with a )":  {Attributes{Login: "uid)(x", Mail: DefaultMail}, ByLogin, KeyDovecot},
		"mail attribute with": {Attributes{Login: DefaultLogin, Mail: "mail=*)(|(x"}, ByMail, KeyPostfix},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if got, err := LookupFilter(tc.attrs, active, tc.by, tc.key); err == nil {
				t.Errorf("filter %q was built, want an error", got)
			}
		})
	}
	if _, err := LookupFilter(attrs, "(a=b))(|(c=d)", ByMail, KeyPostfix); err == nil {
		t.Error("an active filter that only works in the composition was accepted")
	}
}

func TestInvalidFilters(t *testing.T) {
	for name, active := range map[string]string{
		"no parentheses":             "mailEnabled=TRUE",
		"unbalanced":                 "(mailEnabled=TRUE",
		"closes early and adds more": "(a=b))(|(c=d)",
		"empty condition":            "()",
		"trailing text":              "(a=b)x",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidActiveFilter(active); err == nil {
				t.Errorf("ValidActiveFilter(%q) accepted", active)
			}
			if _, err := ProbeFilter(uidMail, active); err == nil {
				t.Errorf("ProbeFilter accepted the active filter %q", active)
			}
		})
	}
	if err := ValidActiveFilter(""); err != nil {
		t.Errorf("an empty filter is no restriction: %v", err)
	}
}

func TestProbeFilterRejectsAttributeNames(t *testing.T) {
	for _, a := range []Attributes{
		{Login: "uid)(objectClass=*", Mail: DefaultMail},
		{Login: "uid", Mail: "mail=x"},
		{Login: "", Mail: DefaultMail},
		{Login: "uid", Mail: DefaultMail, Aliases: "a b"},
		{Login: "1uid", Mail: DefaultMail},
	} {
		if _, err := ProbeFilter(a, ""); err == nil {
			t.Errorf("ProbeFilter accepted %+v", a)
		}
	}
}

func TestLDAPPort(t *testing.T) {
	for _, tc := range []struct {
		t    LDAPTarget
		want int
	}{
		{LDAPTarget{TLSMode: LDAPStartTLS}, 389},
		{LDAPTarget{TLSMode: LDAPNoTLS}, 389},
		{LDAPTarget{TLSMode: LDAPS}, 636},
		{LDAPTarget{TLSMode: LDAPS, Port: 1636}, 1636},
	} {
		if got := LDAPPort(tc.t); got != tc.want {
			t.Errorf("LDAPPort(%+v) = %d, want %d", tc.t, got, tc.want)
		}
	}
}

func TestTLSConfigRejectsACAWithoutCertificates(t *testing.T) {
	if _, ok := tlsConfig("ldap.example.org", []byte("not a certificate")); ok {
		t.Error("a CA bundle without a certificate was accepted")
	}
	cfg, ok := tlsConfig("ldap.example.org", nil)
	if !ok || cfg.ServerName != "ldap.example.org" || cfg.RootCAs != nil || cfg.InsecureSkipVerify {
		t.Errorf("the default config must verify against the system roots: %+v", cfg)
	}
}

func TestConnConfig(t *testing.T) {
	target := SQLTarget{Host: "db.example.org", Port: 5432, Database: testDB, Username: lookupUser, Password: "pa ss'word"}
	for mode, want := range map[string]struct {
		tls, verify bool
	}{
		SQLVerifyFull: {tls: true, verify: true},
		SQLRequire:    {tls: true},
		SQLDisable:    {},
	} {
		target.TLSMode = mode
		cfg, ok := connConfig(target)
		if !ok {
			t.Fatalf("%s: no config", mode)
		}
		if cfg.Host != "db.example.org" || cfg.Port != 5432 || cfg.Database != testDB ||
			cfg.User != lookupUser || cfg.Password != "pa ss'word" {
			t.Errorf("%s: the fields were not taken over: %+v", mode, cfg.Config)
		}
		if (cfg.TLSConfig != nil) != want.tls {
			t.Errorf("%s: TLS config = %v, want %v", mode, cfg.TLSConfig != nil, want.tls)
		}
		if cfg.TLSConfig != nil && cfg.TLSConfig.InsecureSkipVerify == want.verify {
			t.Errorf("%s: InsecureSkipVerify = %v", mode, cfg.TLSConfig.InsecureSkipVerify)
		}
		if len(cfg.Fallbacks) != 0 {
			t.Errorf("%s: fallbacks would allow plain text: %v", mode, cfg.Fallbacks)
		}
	}
	target.TLSMode, target.CA = SQLVerifyFull, []byte("not a certificate")
	if _, ok := connConfig(target); ok {
		t.Error("a CA bundle without a certificate was accepted")
	}
}

// certPair returns a self-signed CA (PEM) and a server certificate for
// name signed by it.
func certPair(t *testing.T, name string) (caPEM []byte, leaf *x509.Certificate) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	sign := func(tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
		tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	caKey, leafKey := newKey(), newKey()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name + " CA"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	ca := sign(caTmpl, caTmpl, &caKey.PublicKey, caKey)
	leaf = sign(&x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{name},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, ca, &leafKey.PublicKey, caKey)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), leaf
}

// "require" with a CA checks the chain but not the host name, like libpq
// with a root certificate (verify-ca); without a CA it checks nothing.
func TestConnConfigRequireVerifiesTheChainWithACA(t *testing.T) {
	caPEM, leaf := certPair(t, "db.example.org")
	otherPEM, _ := certPair(t, "db.example.org")
	target := SQLTarget{Host: "db-renamed.example.org", Port: 5432, Database: testDB, Username: lookupUser, TLSMode: SQLRequire}

	cfg, ok := connConfig(target)
	if !ok || cfg.TLSConfig.VerifyConnection != nil || !cfg.TLSConfig.InsecureSkipVerify {
		t.Fatalf("require without a CA must verify nothing: %+v", cfg)
	}

	target.CA = caPEM
	cfg, ok = connConfig(target)
	if !ok || cfg.TLSConfig.VerifyConnection == nil {
		t.Fatal("require with a CA must verify the chain")
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err := cfg.TLSConfig.VerifyConnection(state); err != nil {
		t.Errorf("a certificate of the CA for another host name was refused: %v", err)
	}
	if err := cfg.TLSConfig.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Error("a server without a certificate was accepted")
	}
	target.CA = otherPEM
	cfg, _ = connConfig(target)
	err := cfg.TLSConfig.VerifyConnection(state)
	if _, isUnknown := errors.AsType[x509.UnknownAuthorityError](err); !isUnknown {
		t.Errorf("a certificate of another CA: err = %v", err)
	}
	if got := connectOutcome(err); got != TLSFailed {
		t.Errorf("a refused chain = %q, want TLSFailed", got)
	}
	target.CA = []byte("not a certificate")
	if _, ok := connConfig(target); ok {
		t.Error("a CA bundle without a certificate was accepted")
	}
}

func TestSQLText(t *testing.T) {
	got := sqlText(usersProbe, Relation{Schema: "public", Name: "cnms_users"})
	want := `SELECT 1 FROM "public"."cnms_users" WHERE login ~ '^[a-z0-9][a-z0-9._-]{0,63}$' AND login !~ '^sa\.' AND mail IS NOT NULL AND password IS NOT NULL LIMIT 1`
	if got != want {
		t.Errorf("users probe = %q, want %q", got, want)
	}
	// Whatever a name holds, it stays inside the quoted identifier.
	got = sqlText(aliasesProbe, Relation{Schema: "s", Name: `a"; DROP TABLE x; --`})
	want = `SELECT 1 FROM "s"."a""; DROP TABLE x; --" WHERE alias IS NOT NULL AND target IS NOT NULL LIMIT 1`
	if got != want {
		t.Errorf("aliases probe = %q, want %q", got, want)
	}
}

func TestSQLOutcome(t *testing.T) {
	pg := func(code string) error {
		return fmt.Errorf("query failed: %w", &pgconn.PgError{Code: code, Message: "secret=hunter2"})
	}
	for name, tc := range map[string]struct {
		err  error
		want Outcome
	}{
		"undefined table":      {pg("42P01"), RelationMissing},
		"undefined schema":     {pg("3F000"), RelationMissing},
		"undefined column":     {pg("42703"), RelationMissing},
		"wrong password":       {pg("28P01"), BindFailed},
		"no privilege":         {pg("42501"), BindFailed},
		"other server error":   {pg("57P03"), Unreachable},
		"refused":              {errors.New("dial tcp 203.0.113.5:5432: connect: connection refused"), Unreachable},
		"refused, tls host":    {fmt.Errorf("failed to connect: tls-db.corp.example:5432 (tls-db.corp.example): dial error: %w", refused("tls-db.corp.example")), Unreachable},
		"server refused TLS":   {fmt.Errorf("failed to connect: db.example.org (db.example.org): tls error: %w", errors.New("server refused TLS connection")), TLSFailed},
		"tls error":            {fmt.Errorf("failed to connect: db.example.org (db.example.org): %w", fmt.Errorf("tls error: %w", io.EOF)), TLSFailed},
		"unknown authority":    {fmt.Errorf("connect: %w", x509.UnknownAuthorityError{}), TLSFailed},
		"certificate mismatch": {fmt.Errorf("connect: %w", &tls.CertificateVerificationError{Err: x509.HostnameError{}}), TLSFailed},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sqlOutcome(tc.err); got != tc.want {
				t.Errorf("sqlOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLDAPOutcomes(t *testing.T) {
	entry := &ldap.SearchResult{Entries: []*ldap.Entry{{DN: "uid=a"}}}
	for name, got := range map[string]Outcome{
		"entry":               searchOutcome(entry, nil),
		"size limit, entry":   searchOutcome(entry, ldap.NewError(ldap.LDAPResultSizeLimitExceeded, errors.New("size limit"))),
		"no entry":            searchOutcome(&ldap.SearchResult{}, nil),
		"base DN missing":     searchOutcome(nil, ldap.NewError(ldap.LDAPResultNoSuchObject, errors.New("no such object"))),
		"insufficient access": searchOutcome(nil, ldap.NewError(ldap.LDAPResultInsufficientAccessRights, errors.New("denied"))),
		"network":             searchOutcome(nil, ldap.NewError(ldap.ErrorNetwork, errors.New("closed"))),
	} {
		want := map[string]Outcome{
			"entry": Reachable, "size limit, entry": Reachable, "no entry": NoMatchingEntries,
			"base DN missing": NoMatchingEntries, "insufficient access": BindFailed, "network": Unreachable,
		}[name]
		if got != want {
			t.Errorf("%s: outcome = %q, want %q", name, got, want)
		}
	}
	if got := bindOutcome(ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("bad"))); got != BindFailed {
		t.Errorf("a rejected bind = %q", got)
	}
	if got := bindOutcome(ldap.NewError(ldap.ErrorNetwork, errors.New("closed"))); got != Unreachable {
		t.Errorf("a network error while binding = %q", got)
	}
	if got := connectOutcome(ldap.NewError(ldap.ErrorNetwork, fmt.Errorf("x: %w", x509.UnknownAuthorityError{}))); got != TLSFailed {
		t.Errorf("an unknown authority = %q", got)
	}
	if got := connectOutcome(ldap.NewError(ldap.ErrorNetwork, errors.New("connection refused"))); got != Unreachable {
		t.Errorf("a refused connection = %q", got)
	}
}

// refused is a dial to a closed port of a host.
func refused(host string) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connect %s: %w", host, syscall.ECONNREFUSED)}
}

// A host name that contains "tls" must not turn a network failure into a
// TLS failure.
func TestConnectOutcomeIgnoresHostNames(t *testing.T) {
	const host = "tls-ldap.corp.example"
	for name, tc := range map[string]struct {
		err  error
		want Outcome
	}{
		"refused":         {ldap.NewError(ldap.ErrorNetwork, refused(host)), Unreachable},
		"unresolvable":    {ldap.NewError(ldap.ErrorNetwork, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}), Unreachable},
		"text with host":  {ldap.NewError(ldap.ErrorNetwork, errors.New("lookup "+host+": no such host")), Unreachable},
		"timeout":         {ldap.NewError(ldap.ErrorNetwork, fmt.Errorf("dial %s: %w", host, context.DeadlineExceeded)), Unreachable},
		"StartTLS":        {ldap.NewError(ldap.ErrorNetwork, fmt.Errorf("TLS handshake failed (%v)", "x509: certificate signed by unknown authority")), TLSFailed},
		"untyped tls":     {ldap.NewError(ldap.ErrorNetwork, errors.New("tls: protocol version not supported")), TLSFailed},
		"alert":           {ldap.NewError(ldap.ErrorNetwork, &net.OpError{Op: "remote error", Err: tls.AlertError(42)}), TLSFailed},
		"record header":   {ldap.NewError(ldap.ErrorNetwork, tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), TLSFailed},
		"unknown message": {errors.New("something else"), Unreachable},
	} {
		t.Run(name, func(t *testing.T) {
			if got := connectOutcome(tc.err); got != tc.want {
				t.Errorf("connectOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

// listen serves every connection with handle, on the loopback.
func listen(t *testing.T, handle func(net.Conn)) (port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); handle(c) }()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// closedPort returns a loopback port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// The probes against real sockets: the error values of pgx and go-ldap
// are classified as they really come, not as the tests above build them.
func TestProbesAgainstLoopbackServers(t *testing.T) {
	sql := SQLTarget{Database: testDB, Username: lookupUser, Password: "x", TLSMode: SQLVerifyFull,
		Users: Relation{Schema: "public", Name: "cnms_users"}}
	ldapT := LDAPTarget{BaseDN: "dc=example,dc=org", TLSMode: LDAPS, Attributes: uidMail}

	// PostgreSQL answers the SSLRequest with 'N': it refuses TLS.
	sql.Host = "127.0.0.1"
	sql.Port = listen(t, func(c net.Conn) {
		_, _ = io.ReadFull(c, make([]byte, 8))
		_, _ = c.Write([]byte("N"))
	})
	if got := (NetProber{}).ProbeSQL(t.Context(), sql); got != TLSFailed {
		t.Errorf("a server that refuses TLS = %q", got)
	}
	sql.Port = closedPort(t)
	if got := (NetProber{}).ProbeSQL(t.Context(), sql); got != Unreachable {
		t.Errorf("a closed SQL port = %q", got)
	}

	// A plain-text server on an LDAPS port is a TLS failure.
	ldapT.Host = "127.0.0.1"
	ldapT.Port = listen(t, func(c net.Conn) {
		_, _ = c.Write([]byte("not tls at all\r\n\r\n\r\n"))
		_ = c.(*net.TCPConn).CloseWrite()
		_, _ = io.Copy(io.Discard, c) // until the client gives up, so no reset hides the bytes
	})
	if got := (NetProber{}).ProbeLDAP(t.Context(), ldapT); got != TLSFailed {
		t.Errorf("a plain-text server on LDAPS = %q", got)
	}
	ldapT.Port = closedPort(t)
	if got := (NetProber{}).ProbeLDAP(t.Context(), ldapT); got != Unreachable {
		t.Errorf("a closed LDAPS port = %q", got)
	}
}

// Without an alias attribute the owner of an address is found by the mail
// attribute alone.
func TestLookupFilterSenderWithoutAliases(t *testing.T) {
	got, err := LookupFilter(Attributes{Login: DefaultLogin, Mail: DefaultMail}, "", BySender, KeyPostfix)
	if err != nil {
		t.Fatal(err)
	}
	if want := "(&(mail=%s)(uid=*)(mail=*)(!(uid=.*))(!(uid=*/*))(!(uid=sa.*)))"; got != want {
		t.Errorf("filter = %q, want %q", got, want)
	}
}
