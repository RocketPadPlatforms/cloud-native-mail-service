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

package addresses

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func usersWithAliases(n, aliases int) []User {
	us := make([]User, n)
	for i := range us {
		us[i] = user("u"+strconv.Itoa(i), refA)
		for j := range aliases {
			us[i].Aliases = append(us[i].Aliases, addr("a"+strconv.Itoa(j), refB))
		}
	}
	return us
}

func wantLimit(t *testing.T, err error, reason string) {
	t.Helper()
	le, ok := errors.AsType[*LimitError](err)
	if !ok || le.Reason != reason || le.Message == "" || !IsLimit(err) {
		t.Fatalf("error = %v, want a LimitError with reason %s", err, reason)
	}
}

// ADR 0015, section 4.7: 1,000 accounts, 5,000 addresses, 32 aliases.
func TestCheckLimitsBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		in     Input
		reason string // empty: no error
	}{
		{"1000 accounts", input(usersWithAliases(MaxUsers, 0)), ""},
		{"1001 accounts", input(usersWithAliases(MaxUsers+1, 0)), ReasonUserLimitExceeded},
		{"5000 addresses", input(usersWithAliases(MaxUsers, 4)), ""},
		{"5000 addresses with a sender", input(usersWithAliases(MaxUsers-1, 4), sender("ns/a", "x", refA)), ""},
		{"5001 addresses", input(usersWithAliases(MaxUsers, 4), sender("ns/a", "x", refA)), ReasonAddressLimitExceeded},
		{"5001 addresses of senders alone", input(nil, manySenders(MaxAddresses+1)...), ReasonAddressLimitExceeded},
		{"5000 addresses of senders alone", input(nil, manySenders(MaxAddresses)...), ""},
		{"32 aliases", input(usersWithAliases(1, MaxAliases)), ""},
		{"33 aliases", input(usersWithAliases(1, MaxAliases+1)), ReasonAddressLimitExceeded},
		{"too many accounts and addresses: the accounts first", input(usersWithAliases(MaxUsers+1, 32)), ReasonUserLimitExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckLimits(tt.in)
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("CheckLimits() = %v", err)
				}
				return
			}
			wantLimit(t, err, tt.reason)
		})
	}
}

func manySenders(n int) []Sender {
	ss := make([]Sender, n)
	for i := range ss {
		ss[i] = sender("ns/s"+strconv.Itoa(i), "x", refA)
	}
	return ss
}

func TestLimitErrorWrapped(t *testing.T) {
	err := fmt.Errorf("render: %w", CheckLimits(input(usersWithAliases(MaxUsers+1, 0))))
	if !IsLimit(err) {
		t.Error("a wrapped LimitError is not a limit")
	}
	if IsLimit(errors.New("other")) || IsLimit(nil) {
		t.Error("another error is a limit")
	}
}

// The byte guard: under 1,000,000 passes; 1,000,000 and more are refused.
func TestCheckObjectSizeBoundary(t *testing.T) {
	for size, ok := range map[int]bool{0: true, 999_999: true, 1_000_000: false, 1_000_001: false} {
		err := CheckObjectSize("mail-postfix-1234abcd", size)
		if ok && err != nil {
			t.Errorf("%d bytes: %v", size, err)
		}
		if !ok {
			wantLimit(t, err, ReasonConfigTooLarge)
			if !strings.Contains(err.Error(), "mail-postfix-1234abcd") || !strings.Contains(err.Error(), strconv.Itoa(size)) {
				t.Errorf("the message does not name the object and its size: %v", err)
			}
		}
	}
}

// The size counts the keys as well as the values (the conservative
// reading of the limit of the API server).
func TestObjectSizeCountsKeys(t *testing.T) {
	if got := ObjectSize(map[string]string(nil)); got != 0 {
		t.Errorf("ObjectSize(nil) = %d", got)
	}
	data := map[string]string{"key": strings.Repeat("v", 999_996)} // 3 + 999,996
	if got := ObjectSize(data); got != 999_999 {
		t.Fatalf("ObjectSize = %d, want 999999", got)
	}
	if err := CheckObjectSize("x", ObjectSize(data)); err != nil {
		t.Errorf("999,999 bytes: %v", err)
	}
	data["k2"] = "" // the key alone: 1,000,001
	wantLimit(t, CheckObjectSize("x", ObjectSize(data)), ReasonConfigTooLarge)
}

func TestCheckObjectsNamesTheFirstInOrder(t *testing.T) {
	big := map[string]string{"f": strings.Repeat("x", MaxObjectBytes)}
	err := CheckObjects(map[string]map[string]string{"b-object": big, "a-object": big, "c-object": {"f": "x"}})
	wantLimit(t, err, ReasonConfigTooLarge)
	if !strings.Contains(err.Error(), "a-object") {
		t.Errorf("the first object in order is not named: %v", err)
	}
	if err := CheckObjects(map[string]map[string]string{"a": {"f": "x"}}); err != nil {
		t.Errorf("CheckObjects() = %v", err)
	}
}

// A Secret has byte values: the size is the same, with no conversion.
func TestObjectSizeOfBytes(t *testing.T) {
	data := map[string][]byte{"key": make([]byte, 999_996)}
	if got := ObjectSize(data); got != 999_999 {
		t.Fatalf("ObjectSize = %d, want 999999", got)
	}
	if err := CheckObjects(map[string]map[string][]byte{"secret": data}); err != nil {
		t.Errorf("999,999 bytes: %v", err)
	}
	data["k2"] = nil
	wantLimit(t, CheckObjects(map[string]map[string][]byte{"secret": data}), ReasonConfigTooLarge)
}
