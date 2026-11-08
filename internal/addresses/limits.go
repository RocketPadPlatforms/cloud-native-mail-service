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
	"slices"
)

// The limits of ADR 0015, section 4.7. The counts are product limits of
// one MailServer; the alias limit is the API's (MailUser.spec.aliases);
// the byte limit is a margin below the 1 MiB of a ConfigMap or Secret.
const (
	// MaxUsers is the number of accounts (MailUsers) of a server.
	MaxUsers = 1000
	// MaxAddresses is the number of addresses of a server: the primary
	// addresses, the aliases and the senders of the service accounts.
	MaxAddresses = 5000
	// MaxAliases is the number of aliases of one account.
	MaxAliases = 32
	// MaxObjectBytes is the size that the data of a rendered object (keys
	// and values) must stay under.
	MaxObjectBytes = 1_000_000
)

// The reasons of a blocked revision (ADR 0015, sections 4.7 and 8): the
// Ready reasons of a MailServer. The previous revision keeps running.
const (
	ReasonUserLimitExceeded    = "UserLimitExceeded"
	ReasonAddressLimitExceeded = "AddressLimitExceeded"
	ReasonConfigTooLarge       = "ConfigTooLarge"
)

// LimitError is the verdict of a limit: it carries the reason for the
// condition and a message that names the counts or the object. It is never
// resolved by dropping entries: "dropping the tail by name would silently
// take arbitrary users away".
type LimitError struct {
	Reason  string
	Message string
}

// Error implements error.
func (e *LimitError) Error() string { return e.Reason + ": " + e.Message }

// IsLimit reports whether err is, or wraps, a LimitError: the revision is
// blocked.
func IsLimit(err error) bool {
	_, ok := errors.AsType[*LimitError](err)
	return ok
}

// CheckLimits checks the counts of a server: at most MaxUsers accounts and
// MaxAddresses addresses, and MaxAliases aliases per account (the API
// enforces the last; a state that gets here with more is refused with the
// address reason). It counts what is declared, not what is served, so a
// conflict cannot hide a claim from the count. The result is nil, or a
// *LimitError.
func CheckLimits(in Input) error {
	if n := len(in.Users); n > MaxUsers {
		return &LimitError{ReasonUserLimitExceeded,
			fmt.Sprintf("the server has %d accounts, at most %d are allowed", n, MaxUsers)}
	}
	addrs := len(in.Senders)
	for _, u := range sortedUsers(in.Users) {
		if len(u.Aliases) > MaxAliases {
			return &LimitError{ReasonAddressLimitExceeded,
				fmt.Sprintf("the account %q has %d aliases, at most %d are allowed", u.Name, len(u.Aliases), MaxAliases)}
		}
		addrs += 1 + len(u.Aliases)
	}
	if addrs > MaxAddresses {
		return &LimitError{ReasonAddressLimitExceeded,
			fmt.Sprintf("the server has %d addresses, at most %d are allowed", addrs, MaxAddresses)}
	}
	return nil
}

// ObjectSize is the size of the data of a ConfigMap (string values) or a
// Secret (byte values): the keys and the values. Counting the keys is the
// conservative reading of the limit of the API server (ADR 0015, section
// 4.7).
func ObjectSize[V ~string | ~[]byte](data map[string]V) int {
	n := 0
	for k, v := range data {
		n += len(k) + len(v)
	}
	return n
}

// CheckObjectSize refuses an object whose data is not under MaxObjectBytes:
// 999,999 bytes pass, 1,000,000 and more do not. The result is nil, or a
// *LimitError with the reason ConfigTooLarge that names the object and its
// size.
func CheckObjectSize(name string, size int) error {
	if size < MaxObjectBytes {
		return nil
	}
	return &LimitError{ReasonConfigTooLarge,
		fmt.Sprintf("%s holds %d bytes, it must stay under %d", name, size, MaxObjectBytes)}
}

// CheckObjects checks several rendered objects, by name, and returns the
// first that is too large in sorted order of the names, so that the
// message does not depend on map order. The MailServer controller calls it
// on the data of the ConfigMaps and Secrets of a revision before it applies
// them.
func CheckObjects[V ~string | ~[]byte](objects map[string]map[string]V) error {
	names := make([]string, 0, len(objects))
	for n := range objects {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := CheckObjectSize(n, ObjectSize(objects[n])); err != nil {
			return err
		}
	}
	return nil
}
