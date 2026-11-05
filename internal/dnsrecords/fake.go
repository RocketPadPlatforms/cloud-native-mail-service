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
	"fmt"
	"slices"
	"strings"
	"sync"
)

// FakeResolver is a Resolver with canned answers for unit and envtest
// tests, which never use real DNS. It is safe for concurrent use.
type FakeResolver struct {
	mu      sync.Mutex
	records map[string][]string
	hosts   map[string][]string
	mxs     map[string][]MX
	errs    map[string]error
}

// NewFakeResolver returns an empty FakeResolver: every name is not found.
func NewFakeResolver() *FakeResolver {
	return &FakeResolver{
		records: map[string][]string{}, hosts: map[string][]string{},
		mxs: map[string][]MX{}, errs: map[string]error{},
	}
}

func fakeKey(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }

// Set replaces the TXT records at name; each argument is one record.
func (f *FakeResolver) Set(name string, txts ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[fakeKey(name)] = slices.Clone(txts)
	delete(f.errs, fakeKey(name))
}

// SetHost replaces the addresses (A and AAAA records) of name.
func (f *FakeResolver) SetHost(name string, addrs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts[fakeKey(name)] = slices.Clone(addrs)
	delete(f.errs, fakeKey(name))
}

// SetMX replaces the MX records at name.
func (f *FakeResolver) SetMX(name string, mxs ...MX) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mxs[fakeKey(name)] = slices.Clone(mxs)
	delete(f.errs, fakeKey(name))
}

// SetError makes every lookup of name (any record kind) fail with err,
// which should not wrap ErrNotFound if a temporary failure is meant.
func (f *FakeResolver) SetError(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[fakeKey(name)] = err
}

// Delete removes name: lookups of any record kind answer "not found"
// again.
func (f *FakeResolver) Delete(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := fakeKey(name)
	delete(f.records, k)
	delete(f.hosts, k)
	delete(f.mxs, k)
	delete(f.errs, k)
}

// LookupTXT implements Resolver.
func (f *FakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[fakeKey(name)]; err != nil {
		return nil, err
	}
	if txts := f.records[fakeKey(name)]; len(txts) > 0 {
		return slices.Clone(txts), nil
	}
	return nil, fmt.Errorf("%w: no TXT record at %s", ErrNotFound, name)
}

// LookupHost implements Resolver.
func (f *FakeResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[fakeKey(name)]; err != nil {
		return nil, err
	}
	if addrs := f.hosts[fakeKey(name)]; len(addrs) > 0 {
		return slices.Clone(addrs), nil
	}
	return nil, fmt.Errorf("%w: no A or AAAA record at %s", ErrNotFound, name)
}

// LookupMX implements Resolver.
func (f *FakeResolver) LookupMX(_ context.Context, name string) ([]MX, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[fakeKey(name)]; err != nil {
		return nil, err
	}
	if mxs := f.mxs[fakeKey(name)]; len(mxs) > 0 {
		return slices.Clone(mxs), nil
	}
	return nil, fmt.Errorf("%w: no MX record at %s", ErrNotFound, name)
}
