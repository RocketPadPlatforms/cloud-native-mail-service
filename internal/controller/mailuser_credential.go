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

package controller

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The password hash of a MailUser (ADR 0015, section 3, point 4): Argon2id
// with the OWASP profile m=19456 (19 MiB), t=2, p=1 for every password, a
// 16-byte random salt and a 32-byte tag, in the PHC form that Dovecot reads
// with the {ARGON2ID} prefix. Dovecot's own default, m=65536, is not used:
// a verification needs m of memory, anyone can start one, and the Dovecot
// container is limited (section 3, point 4).
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 19456
	argonThreads uint8  = 1
	argonSaltLen        = 16
	argonKeyLen  uint32 = 32

	// hashScheme is the prefix that selects the scheme in Dovecot.
	hashScheme = "{ARGON2ID}"

	// minPasswordLength is the shortest password a MailUser may bring
	// (ADR 0015, section 3, point 4), in characters.
	minPasswordLength = 12
	// generatedPasswordBytes is the entropy of a generated password; it is
	// stored base64url encoded.
	generatedPasswordBytes = 32
)

// hashSlots lets one hash run at a time in the whole operator. One Argon2id
// run holds 19 MiB and leaves 19 MiB of garbage, the operator container is
// limited to 192 MiB with GOMEMLIMIT 160MiB (config/manager/manager.yaml),
// and the reconcilers of the MailUsers run in parallel: unbounded, a burst
// of new users would exceed the limit. The hashes of the MailUsers are rare
// (one per password); the MailServiceAccount controller verifies a hash only
// when its Secret has changed (verifiedSecrets), so queueing costs little.
var hashSlots = make(chan struct{}, 1)

// acquireHashSlot waits for the hash slot, or for the end of the context.
func acquireHashSlot(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseHashSlot() { <-hashSlots }

// phc encodes a hash in the PHC form with the Dovecot prefix; the salt and
// the tag are base64 without padding.
func phc(salt, tag []byte, memory, time uint32, threads uint8) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("%s$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		hashScheme, argon2.Version, memory, time, threads, b64.EncodeToString(salt), b64.EncodeToString(tag))
}

// hashWithSalt is the hash of password under salt with the parameters of
// ADR 0015. It does not take the hash slot: callers do.
func hashWithSalt(password string, salt []byte) string {
	tag := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return phc(salt, tag, argonMemory, argonTime, argonThreads)
}

// hashPassword hashes password with a fresh random salt. The error is
// fixed: it carries nothing of the password.
func hashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", errHash
	}
	if err := acquireHashSlot(ctx); err != nil {
		return "", err
	}
	defer releaseHashSlot()
	return hashWithSalt(password, salt), nil
}

// errHash is the fixed error of a failed hash.
var errHash = fmt.Errorf("the password could not be hashed")

// parseHash splits a stored hash into its salt and tag. It accepts exactly
// what hashWithSalt writes: argon2id version 19, m=19456,t=2,p=1, a 16-byte
// salt and a 32-byte tag. The operator is the only writer of the hash, and
// the Secret can be edited by anyone who may update Secrets in the
// namespace: a hash with other parameters (a large m, many passes) would
// make a verification cost what its author chose, inside a small container.
// Such a hash does not verify and is replaced by one with the pinned
// parameters. A hash made by `doveadm pw` (m=65536, t=3) is therefore not
// accepted here; it is accepted by Dovecot, and the Go library verifies it
// in a test (TestVerifyHash).
func parseHash(stored string) (salt, tag []byte, ok bool) {
	// The stored string ends up as a line of a passwd-file and must be the
	// printable ASCII of the PHC form, nothing else. Go's base64 decoder
	// ignores a CR or a LF anywhere in its input, so the check cannot be left
	// to the decoding below: a newline in the tag would still decode, still
	// verify, and then break the file (and the render that writes it).
	for i := 0; i < len(stored); i++ {
		if stored[i] <= ' ' || stored[i] > '~' {
			return nil, nil, false
		}
	}
	rest, found := strings.CutPrefix(stored, hashScheme)
	if !found {
		return nil, nil, false
	}
	parts := strings.Split(rest, "$")
	params := fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonTime, argonThreads)
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) || parts[3] != params {
		return nil, nil, false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) != argonSaltLen {
		return nil, nil, false
	}
	tag, err = b64.DecodeString(parts[5])
	if err != nil || len(tag) != int(argonKeyLen) {
		return nil, nil, false
	}
	// Only the canonical form: encoding what was decoded gives the stored
	// string back, byte for byte. That refuses what a decoder tolerates (bits
	// after the last full byte, the other base64 alphabets, padding).
	if phc(salt, tag, argonMemory, argonTime, argonThreads) != stored {
		return nil, nil, false
	}
	return salt, tag, true
}

// verifyHash reports whether password matches the stored hash, in constant
// time for the tag. A hash that does not parse, or has other parameters than
// the pinned ones, does not match (and is rewritten by the caller).
func verifyHash(ctx context.Context, password, stored string) (bool, error) {
	salt, tag, ok := parseHash(stored)
	if !ok {
		return false, nil
	}
	if err := acquireHashSlot(ctx); err != nil {
		return false, err
	}
	defer releaseHashSlot()
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, tag) == 1, nil
}

// generatePassword returns a new random password: 32 bytes from crypto/rand,
// base64url without padding (43 characters).
func generatePassword() (string, error) {
	b := make([]byte, generatedPasswordBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errHash
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
