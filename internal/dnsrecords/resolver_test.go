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
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// answer is what the test DNS server says for a name.
type answer struct {
	rcode dnsmessage.RCode
	// txt holds one entry per TXT record, each a list of character-strings.
	txt [][]string
	// drop makes the server stay silent.
	drop bool
}

// startDNS runs a UDP DNS server on the loopback interface, in process:
// tests never reach a real resolver. It returns its address.
func startDNS(t *testing.T, answers map[string]answer) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if reply, ok := respond(buf[:n], answers); ok {
				_, _ = pc.WriteTo(reply, addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func respond(query []byte, answers map[string]answer) ([]byte, bool) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, false
	}
	q, err := p.Question()
	if err != nil {
		return nil, false
	}
	a, known := answers[strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")]
	if a.drop {
		return nil, false
	}
	rcode := a.rcode
	if !known {
		rcode = dnsmessage.RCodeNameError
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RCode: rcode},
		Questions: []dnsmessage.Question{q},
	}
	for _, strs := range a.txt {
		msg.Answers = append(msg.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.TXTResource{TXT: strs},
		})
	}
	out, err := msg.Pack()
	return out, err == nil
}

func TestNetResolver(t *testing.T) {
	long := strings.Repeat("x", 255)
	addr := startDNS(t, map[string]answer{
		"one.example.org":      {txt: [][]string{{spfNone}}},
		"split.example.org":    {txt: [][]string{{"v=DKIM1; k=rsa; ", "p=abc", long}}},
		"two.example.org":      {txt: [][]string{{"first"}, {"second"}}},
		"nodata.example.org":   {},
		"servfail.example.org": {rcode: dnsmessage.RCodeServerFailure},
		"silent.example.org":   {drop: true},
	})
	r, err := NewNetResolver(addr, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		want []string
	}{
		{"one.example.org", []string{spfNone}},
		{"One.Example.org.", []string{spfNone}}, // case and trailing dot
		{"split.example.org", []string{"v=DKIM1; k=rsa; p=abc" + long}},
		{"two.example.org", []string{"first", "second"}},
	} {
		t.Run("answer "+tc.name, func(t *testing.T) {
			got, err := r.LookupTXT(context.Background(), tc.name)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	for _, name := range []string{"missing.example.org", "nodata.example.org"} {
		t.Run("not found "+name, func(t *testing.T) {
			_, err := r.LookupTXT(context.Background(), name)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("got %v, want ErrNotFound", err)
			}
		})
	}

	t.Run("servfail is temporary", func(t *testing.T) {
		_, err := r.LookupTXT(context.Background(), "servfail.example.org")
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want a temporary error", err)
		}
	})

	t.Run("timeout is temporary and bounded", func(t *testing.T) {
		start := time.Now()
		_, err := r.LookupTXT(context.Background(), "silent.example.org")
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want a temporary error", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("lookup took %s, the timeout is 300ms", d)
		}
	})

	t.Run("context deadline wins over the timeout", func(t *testing.T) {
		slow, _ := NewNetResolver(addr, time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := slow.LookupTXT(ctx, "silent.example.org")
		if err == nil || errors.Is(err, ErrNotFound) || time.Since(start) > 3*time.Second {
			t.Fatalf("got %v after %s", err, time.Since(start))
		}
	})

	t.Run("cancelled context is temporary", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.LookupTXT(ctx, "one.example.org"); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want a temporary error", err)
		}
	})
}

func TestNewNetResolverValidatesTheAddress(t *testing.T) {
	for _, bad := range []string{"10.0.0.1", "dns.example.org", "[::1]"} {
		if _, err := NewNetResolver(bad, 0); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	for _, ok := range []string{"", "10.0.0.1:53", "[2001:db8::1]:53", "dns.example.org:5353"} {
		if _, err := NewNetResolver(ok, 0); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestFakeResolver(t *testing.T) {
	f := NewFakeResolver()
	ctx := context.Background()
	if _, err := f.LookupTXT(ctx, "a.example.org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty fake: got %v", err)
	}
	f.Set("A.example.org.", "one", "two")
	got, err := f.LookupTXT(ctx, "a.EXAMPLE.org")
	if err != nil || !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	got[0] = "changed"
	if again, _ := f.LookupTXT(ctx, "a.example.org"); again[0] != "one" {
		t.Fatal("the fake hands out its internal slice")
	}
	boom := errors.New("servfail")
	f.SetError("a.example.org", boom)
	if _, err := f.LookupTXT(ctx, "a.example.org"); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	f.Set("a.example.org", "back")
	if got, err := f.LookupTXT(ctx, "a.example.org"); err != nil || got[0] != "back" {
		t.Fatalf("Set must clear the error: %q, %v", got, err)
	}
	f.Delete("a.example.org")
	if _, err := f.LookupTXT(ctx, "a.example.org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
