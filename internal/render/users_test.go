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
		want := []StaticUser{{
			Username:     "testuser",
			PasswordHash: "{SHA512-CRYPT}$6$salt$hash",
			Mailbox:      "testuser@example.com",
		}}
		if len(users) != len(want) || users[0] != want[0] {
			t.Fatalf("got %v, want %v", users, want)
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
