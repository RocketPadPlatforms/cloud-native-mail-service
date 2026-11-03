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

package render

import (
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// usernameRE is the form of a (lower-cased) username: it becomes a path
// component of the Dovecot home directory and the first field of a
// passwd-file line, so only a plain file-name alphabet is accepted;
// a leading dot is excluded (hidden or relative names).
var usernameRE = regexp.MustCompile(`^[a-z0-9_-][a-z0-9._-]*$`)

// usersDoc is the schema of the users.yaml payload in the
// insecureStaticUsers Secret (see
// docs/how-to/02-install-and-operate.md).
type usersDoc struct {
	Users []userDoc `json:"users"`
}

type userDoc struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	Mailbox      string `json:"mailbox"`
}

// ParseStaticUsers parses the users.yaml payload of the
// spec.submission.insecureStaticUsers Secret into StaticUsers.
//
// The schema is:
//
//	users:
//	  - username: testuser
//	    passwordHash: '{SHA512-CRYPT}...'
//	    mailbox: testuser@example.com
//
// A document without a users key is valid and yields zero users.
// Each field is validated fail-closed so a typo cannot produce an
// unusable or overly permissive user entry:
//   - username is a non-empty file-name-safe local part ([a-z0-9._-]
//     after lower-casing, no leading dot, no ".."), at most 64 chars;
//   - passwordHash must be a crypt(3) hash (a "{SCHEME}..."
//     dovecot scheme or a "$..." crypt magic), never plaintext: the
//     plaintext schemes (PLAIN, CLEAR, CLEARTEXT) and the unsalted
//     PLAIN-* schemes are refused;
//   - no field contains ":" (the passwd-file separator) or a control
//     character;
//   - username and mailbox are lower-cased (Dovecot lower-cases logins);
//   - mailbox is a full address with exactly one "@";
//   - usernames and mailboxes are unique, compared case-insensitively.
func ParseStaticUsers(data []byte) ([]StaticUser, error) {
	var doc usersDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("users.yaml: %w", err)
	}
	users := make([]StaticUser, 0, len(doc.Users))
	for i, u := range doc.Users {
		// Postfix lower-cases the recipient lookup and Dovecot 2.4
		// lower-cases logins by default, so both are stored lower-case:
		// a mixed-case username would never match a login.
		u.Username = strings.ToLower(u.Username)
		u.Mailbox = strings.ToLower(u.Mailbox)
		if err := validateUserDoc(u); err != nil {
			return nil, fmt.Errorf("users.yaml: users[%d]: %w", i, err)
		}
		for _, prev := range users {
			if prev.Username == u.Username {
				return nil, fmt.Errorf("users.yaml: users[%d]: duplicate username %q (case-insensitive)", i, u.Username)
			}
			if prev.Mailbox == u.Mailbox {
				return nil, fmt.Errorf("users.yaml: users[%d]: duplicate mailbox %q (case-insensitive)", i, u.Mailbox)
			}
		}
		users = append(users, StaticUser(u))
	}
	return users, nil
}

// hasForbiddenChar reports whether s contains the passwd-file field
// separator or a control character (a newline would start a new line,
// i.e. a new user).
func hasForbiddenChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r == ':' || r < 0x20 || r == 0x7f })
}

// plaintextScheme reports whether a dovecot password scheme name keeps
// the password readable or unsalted: PLAIN, CLEAR and CLEARTEXT, and
// the PLAIN-* family (PLAIN-TRUNC, PLAIN-MD4, PLAIN-MD5); verified
// against `doveadm pw -l` of Dovecot 2.4.5, where names are
// case-insensitive.
func plaintextScheme(name string) bool {
	name = strings.ToUpper(name)
	return name == "PLAIN" || name == "CLEAR" || name == "CLEARTEXT" || strings.HasPrefix(name, "PLAIN-")
}

func validateUserDoc(u userDoc) error {
	if u.Username == "" {
		return fmt.Errorf("username must not be empty")
	}
	if len(u.Username) > 64 {
		return fmt.Errorf("username is longer than 64 characters")
	}
	if strings.Contains(u.Username, "@") {
		return fmt.Errorf("username %q must not contain @", u.Username)
	}
	if !usernameRE.MatchString(u.Username) || strings.Contains(u.Username, "..") {
		return fmt.Errorf("username %q must consist of [a-z0-9._-], must not start with a dot and must not contain two dots", u.Username)
	}
	if hasForbiddenChar(u.PasswordHash) || hasForbiddenChar(u.Mailbox) {
		return fmt.Errorf("passwordHash and mailbox must not contain : or control characters")
	}
	if !looksLikeCryptHash(u.PasswordHash) {
		return fmt.Errorf("passwordHash must be a crypt(3) hash ({SCHEME}... or $...), never plaintext")
	}
	if strings.HasPrefix(u.PasswordHash, "{") {
		if scheme, _, _ := strings.Cut(u.PasswordHash[1:], "}"); plaintextScheme(scheme) {
			return fmt.Errorf("passwordHash scheme {%s} is not allowed: it is plaintext or unsalted", scheme)
		}
	}
	local, domain, ok := strings.Cut(u.Mailbox, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || strings.ContainsAny(u.Mailbox, " \t") {
		return fmt.Errorf("mailbox %q must be a full address with exactly one @", u.Mailbox)
	}
	return nil
}

// looksLikeCryptHash reports whether h is plausibly a crypt(3) hash:
// a dovecot scheme prefix like {SHA512-CRYPT}, {SSHA}, {SHA}, or a
// crypt magic prefix like $6$, $1$. Bare plaintext (and legacy
// unsalted DES output) is rejected.
func looksLikeCryptHash(h string) bool {
	if h == "" {
		return false
	}
	if strings.HasPrefix(h, "{") {
		rest := h[1:]
		end := strings.IndexByte(rest, '}')
		return end >= 1 && validScheme(rest[:end])
	}
	return strings.HasPrefix(h, "$")
}

func validScheme(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
