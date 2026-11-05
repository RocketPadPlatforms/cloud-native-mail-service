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

package dnsrecords

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// ErrNotFound is returned (wrapped) by a Resolver when the name does not
// exist or has no record of the asked kind: an authoritative "no". Every
// other error is temporary (timeout, SERVFAIL, cancelled context) and
// means "unknown" (ADR 0012).
var ErrNotFound = errors.New("no record found")

// MX is one mail exchanger of a name.
type MX struct {
	// Host is the lower-case name without a trailing dot.
	Host string
	Pref uint16
}

// Resolver is the DNS lookup the controller needs.
type Resolver interface {
	// LookupTXT returns the TXT records at name. Each returned string
	// is one record with its character-strings joined, so several
	// records at one name are several strings.
	LookupTXT(ctx context.Context, name string) ([]string, error)
	// LookupHost returns the addresses (A and AAAA records) of name, in
	// text form.
	LookupHost(ctx context.Context, name string) ([]string, error)
	// LookupMX returns the MX records at name.
	LookupMX(ctx context.Context, name string) ([]MX, error)
}

// DefaultTimeout bounds one lookup of a NetResolver.
const DefaultTimeout = 5 * time.Second

// NetResolver is a Resolver backed by net.Resolver.
type NetResolver struct {
	resolver *net.Resolver
	timeout  time.Duration
}

// NewNetResolver returns a resolver that asks server ("host:port"; empty
// means the system resolver). A lookup is bounded by timeout (zero means
// DefaultTimeout) and by the context passed to it, whichever ends first.
func NewNetResolver(server string, timeout time.Duration) (*NetResolver, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	n := &NetResolver{resolver: net.DefaultResolver, timeout: timeout}
	if server == "" {
		return n, nil
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		return nil, fmt.Errorf("invalid DNS resolver address %q (want host:port): %w", server, err)
	}
	n.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		},
	}
	return n, nil
}

// LookupTXT implements Resolver. The name is made absolute, so a search
// domain of the pod never answers for it.
func (n *NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	txts, err := n.resolver.LookupTXT(ctx, strings.TrimSuffix(name, ".")+".")
	return txts, notFound(err, "TXT", name)
}

// LookupHost implements Resolver.
//
// shortcut: net.Resolver also reads /etc/hosts (nsswitch "files" first)
// and has no DNS-only lookup, so a hosts entry of the operator pod can
// verify an address record that the zone does not hold; replace it with
// a DNS-only client when that matters (docs/backlog.md).
func (n *NetResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	addrs, err := n.resolver.LookupHost(ctx, strings.TrimSuffix(name, ".")+".")
	return addrs, notFound(err, "A or AAAA", name)
}

// LookupMX implements Resolver. Records with an invalid host name are
// dropped by net.Resolver, which reports that as an error next to the
// valid ones: the valid ones count.
func (n *NetResolver) LookupMX(ctx context.Context, name string) ([]MX, error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	records, err := n.resolver.LookupMX(ctx, strings.TrimSuffix(name, ".")+".")
	out := make([]MX, 0, len(records))
	for _, r := range records {
		out = append(out, MX{Host: strings.ToLower(strings.TrimSuffix(r.Host, ".")), Pref: r.Pref})
	}
	if len(out) > 0 {
		return out, nil
	}
	return nil, notFound(err, "MX", name)
}

// notFound turns the authoritative "no such name or record" of the
// net package into ErrNotFound and leaves other errors alone.
func notFound(err error, kind, name string) error {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return fmt.Errorf("%w: no %s record at %s", ErrNotFound, kind, name)
	}
	return err
}
