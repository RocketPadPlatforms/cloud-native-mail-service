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
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	wantCryptErr = "crypt(3) hash"
	wantAtErr    = "exactly one @"
)

func TestParseStaticUsers(t *testing.T) {
	valid := `users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}$6$salt$hash'
    mailbox: testuser@example.com
`
	t.Run("valid document", func(t *testing.T) {
		users, err := ParseStaticUsers([]byte(valid))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []Account{{
			Name:         "testuser",
			PasswordHash: "{SHA512-CRYPT}$6$salt$hash",
			Address:      "testuser@example.com",
		}}
		if !reflect.DeepEqual(users, want) {
			t.Fatalf("got %v, want %v", users, want)
		}
	})

	t.Run("mailbox is lower-cased", func(t *testing.T) {
		users, err := ParseStaticUsers([]byte(strings.Replace(valid,
			"testuser@example.com", "TestUser@Example.COM", 1)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if users[0].Address != "testuser@example.com" {
			t.Fatalf("got mailbox %q, want it lower-cased", users[0].Address)
		}
	})

	t.Run("username is lower-cased", func(t *testing.T) {
		users, err := ParseStaticUsers([]byte(strings.Replace(valid,
			"username: testuser", "username: TestUser", 1)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if users[0].Name != "testuser" {
			t.Fatalf("got username %q, want it lower-cased", users[0].Name)
		}
	})

	t.Run("crypt magic hash", func(t *testing.T) {
		_, err := ParseStaticUsers([]byte(strings.Replace(valid,
			"'{SHA512-CRYPT}$6$salt$hash'", "$6$salt$hash", 1)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("empty users list", func(t *testing.T) {
		users, err := ParseStaticUsers([]byte("users: []\n"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(users) != 0 {
			t.Fatalf("got %v users, want 0", users)
		}
	})

	t.Run("missing users key", func(t *testing.T) {
		users, err := ParseStaticUsers([]byte("{}\n"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(users) != 0 {
			t.Fatalf("got %v users, want 0", users)
		}
	})

	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			name:    "invalid yaml",
			doc:     "users: [unclosed",
			wantErr: "users.yaml:",
		},
		{
			name: "empty username",
			doc: `users:
  - username: ''
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@example.com
`,
			wantErr: "users[0]: username must not be empty",
		},
		{
			name: "username with @",
			doc: `users:
  - username: testuser@example.com
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@example.com
`,
			wantErr: `must not contain @`,
		},
		{
			name: "plaintext password",
			doc: `users:
  - username: testuser
    passwordHash: secretpw
    mailbox: testuser@example.com
`,
			wantErr: wantCryptErr,
		},
		{
			name: "lowercase scheme rejected",
			doc: `users:
  - username: testuser
    passwordHash: '{sha512-crypt}x'
    mailbox: testuser@example.com
`,
			wantErr: wantCryptErr,
		},
		{
			name: "empty password",
			doc: `users:
  - username: testuser
    passwordHash: ''
    mailbox: testuser@example.com
`,
			wantErr: wantCryptErr,
		},
		{
			name: "mailbox without @",
			doc: `users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser
`,
			wantErr: wantAtErr,
		},
		{
			name: "mailbox with two @",
			doc: `users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@@example.com
`,
			wantErr: wantAtErr,
		},
		{
			name: "mailbox with space",
			doc: `users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@ example.com
`,
			wantErr: wantAtErr,
		},
		{
			name: "duplicate usernames",
			doc: `users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@example.com
  - username: testuser
    passwordHash: '{SHA512-CRYPT}y'
    mailbox: testuser2@example.com
`,
			wantErr: `users[1]: duplicate username "testuser"`,
		},
		{
			name: "usernames differing only in case",
			doc: `users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@example.com
  - username: TestUser
    passwordHash: '{SHA512-CRYPT}y'
    mailbox: testuser2@example.com
`,
			wantErr: `users[1]: duplicate username "testuser"`,
		},
		{
			name: "duplicate mailboxes",
			doc: `users:
  - username: one
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: shared@example.com
  - username: two
    passwordHash: '{SHA512-CRYPT}y'
    mailbox: shared@example.com
`,
			wantErr: `users[1]: duplicate mailbox "shared@example.com"`,
		},
		{
			name: "mailboxes differing only in case",
			doc: `users:
  - username: one
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: shared@example.com
  - username: two
    passwordHash: '{SHA512-CRYPT}y'
    mailbox: Shared@Example.COM
`,
			wantErr: `users[1]: duplicate mailbox "shared@example.com"`,
		},
		{
			name: "long username",
			doc: `users:
  - username: ` + strings.Repeat("a", 65) + `
    passwordHash: '{SHA512-CRYPT}x'
    mailbox: testuser@example.com
`,
			wantErr: "longer than 64 characters",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStaticUsers([]byte(tc.doc))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// userDocJSON renders one user as a YAML (JSON flow) document, so a
// test can carry newlines and control characters in a field.
func userDocJSON(t *testing.T, username, hash, mailbox string) []byte {
	t.Helper()
	doc, err := json.Marshal(usersDoc{Users: []userDoc{{Username: username, PasswordHash: hash, Mailbox: mailbox}}})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

const (
	goodHash    = "{SHA512-CRYPT}$6$salt$hash"
	testMailbox = "u@example.com"
)

func TestParseStaticUsersUsernames(t *testing.T) {
	for _, name := range []string{"a", "a.b", "a_b-c", "0abc", "_x", "-x", "a..b"} {
		wantOK := name != "a..b"
		_, err := ParseStaticUsers(userDocJSON(t, name, goodHash, testMailbox))
		if (err == nil) != wantOK {
			t.Errorf("username %q: err = %v, want ok = %v", name, err, wantOK)
		}
	}
	for _, name := range []string{
		"a/b", "../x", "..", ".hidden", "a b", "a\\b", "a:b", "a\nb", "a\x00b", "ü", "a%b", "a;b",
	} {
		_, err := ParseStaticUsers(userDocJSON(t, name, goodHash, testMailbox))
		if err == nil {
			t.Errorf("username %q accepted, want an error", name)
		}
	}
}

func TestParseStaticUsersForbiddenFieldChars(t *testing.T) {
	tests := []struct {
		name, username, hash, mailbox string
	}{
		{"colon in hash", "u", "{SHA512-CRYPT}a:b", testMailbox},
		{"colon in mailbox", "u", goodHash, "u:x@example.com"},
		{"newline in hash", "u", "{SHA512-CRYPT}a\nv:x", testMailbox},
		{"newline in mailbox", "u", goodHash, "u@example.com\nx"},
		{"tab in mailbox", "u", goodHash, "u@exa\tmple.com"},
		{"nul in hash", "u", "{SHA512-CRYPT}a\x00b", testMailbox},
		{"delete in mailbox", "u", goodHash, "u@example.com\x7f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStaticUsers(userDocJSON(t, tt.username, tt.hash, tt.mailbox))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestParseStaticUsersPlaintextSchemes(t *testing.T) {
	for _, h := range []string{
		"{PLAIN}secret", "{CLEAR}secret", "{CLEARTEXT}secret", "{plain}secret", "{ClearText}secret",
		"{PLAIN-TRUNC}secret", "{PLAIN-MD5}5ebe2294ecd0e0f08eab7690d2a6ee69", "{PLAIN-MD4}x",
	} {
		_, err := ParseStaticUsers(userDocJSON(t, "u", h, testMailbox))
		if err == nil {
			t.Errorf("hash %q accepted, want an error", h)
		}
	}
	// Salted schemes stay valid, including names that merely contain PLAIN.
	for _, h := range []string{
		goodHash, "{SSHA512}x", "{ARGON2ID}$argon2id$v=19$x", "{BLF-CRYPT}$2y$05$x", "$6$salt$hash", "{PBKDF2}$1$x",
	} {
		if _, err := ParseStaticUsers(userDocJSON(t, "u", h, testMailbox)); err != nil {
			t.Errorf("hash %q rejected: %v", h, err)
		}
	}
}
