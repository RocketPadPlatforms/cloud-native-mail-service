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
// exist or has no TXT record: an authoritative "no". Every other error is
// temporary (timeout, SERVFAIL, cancelled context) and means "unknown"
// (ADR 0012).
var ErrNotFound = errors.New("no TXT record found")

// Resolver is the DNS lookup the controller needs. Each returned string
// is one TXT record with its character-strings joined, so several
// records at one name are several strings.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
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
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, fmt.Errorf("%w at %s", ErrNotFound, name)
	}
	return txts, err
}
