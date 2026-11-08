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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// katHash is the hash of katPassword under the salt 00 01 02 ... 0f that
// hashWithSalt makes. Dovecot 2.4.5 (the pinned image) accepts it:
// `doveadm pw -t '<hash>' -p 'correct horse battery'` answers "(verified)"
// and a wrong password is refused (ADR 0015, verification).
const (
	katPassword = "correct horse battery"
	katHash     = "{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$AAECAwQFBgcICQoLDA0ODw$quyusfeeuAGbpiw09i+j4VKJ+I/5h3mugd4HghmS/x8"

	// dovecotHash is what `doveadm pw -s ARGON2ID` of the same image makes
	// of katPassword, with its own parameters (m=65536, t=3): the Argon2id
	// library recomputes its tag in a test; the production verifier reads
	// only the pinned parameters.
	dovecotHash = "{ARGON2ID}$argon2id$v=19$m=65536,t=3,p=1$h4wU/VOZwqLDH/pr7GkGHA$Yi9O324Z+jLLWrylpQpHkG3SsPTFWDHoRyLAXDCjzH0"
)

func fixedSalt() []byte {
	salt := make([]byte, argonSaltLen)
	for i := range salt {
		salt[i] = byte(i)
	}
	return salt
}

func TestHashKnownAnswer(t *testing.T) {
	if got := hashWithSalt(katPassword, fixedSalt()); got != katHash {
		t.Fatalf("hash = %s, want %s", got, katHash)
	}
	// The size of ADR 0015, section 3, point 4: 107 bytes.
	if len(katHash) != 107 {
		t.Errorf("the hash has %d bytes, want 107", len(katHash))
	}
	if !regexp.MustCompile(`^\{ARGON2ID\}\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`).MatchString(katHash) {
		t.Errorf("the hash is not the PHC form without padding: %s", katHash)
	}
}

func TestVerifyHash(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		password, stored string
		want             bool
	}{
		"the hash of this code": {katPassword, katHash, true},
		// Dovecot accepts other parameters; the operator does not (the Secret may be
		// edited by a tenant): such a hash is replaced by one with the pinned ones.
		"a hash made by Dovecot (m=65536, t=3)": {katPassword, dovecotHash, false},
		"a wrong password":                      {katPassword + "x", katHash, false},
		"no prefix":                             {katPassword, strings.TrimPrefix(katHash, hashScheme), false},
		"empty":                                 {katPassword, "", false},
		"another algorithm":                     {katPassword, strings.Replace(katHash, "argon2id", "argon2i", 1), false},
		"another version":                       {katPassword, strings.Replace(katHash, "v=19", "v=16", 1), false},
		"too much memory":                       {katPassword, strings.Replace(katHash, "m=19456", "m=4194304", 1), false},
		"a little less memory":                  {katPassword, strings.Replace(katHash, "m=19456", "m=19455", 1), false},
		"one more pass":                         {katPassword, strings.Replace(katHash, "t=2", "t=3", 1), false},
		"too many passes":                       {katPassword, strings.Replace(katHash, "t=2", "t=40", 1), false},
		"no passes":                             {katPassword, strings.Replace(katHash, "t=2", "t=0", 1), false},
		"more lanes":                            {katPassword, strings.Replace(katHash, "p=1", "p=2", 1), false},
		"too many lanes":                        {katPassword, strings.Replace(katHash, "p=1", "p=64", 1), false},
		"a 12-byte salt":                        {katPassword, strings.Replace(katHash, "AAECAwQFBgcICQoLDA0ODw", "AAECAwQFBgcICQoL", 1), false},
		"a 64-byte tag":                         {katPassword, katHash[:strings.LastIndex(katHash, "$")+1] + strings.Repeat("quyusfeeuAGbpiw09i+j4VKJ+I/5h3mugd4HghmS/x8", 2)[:86], false},
		"a parameter missing":                   {katPassword, strings.Replace(katHash, "m=19456,t=2,p=1", "m=19456,t=2", 1), false},
		"a parameter that is no number":         {katPassword, strings.Replace(katHash, "t=2", "t=x", 1), false},
		"padding in the salt":                   {katPassword, strings.Replace(katHash, "AAECAwQFBgcICQoLDA0ODw", "AAECAwQFBgcICQoLDA0ODw==", 1), false},
		"a salt that is too short":              {katPassword, strings.Replace(katHash, "AAECAwQFBgcICQoLDA0ODw", "AAEC", 1), false},
		"a tag that is too short":               {katPassword, katHash[:strings.LastIndex(katHash, "$")+1] + "quyu", false},
		"a field too many":                      {katPassword, katHash + "$x", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := verifyHash(ctx, tc.password, tc.stored)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("verifyHash() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Only the canonical form of the hash is accepted (parseHash). The Secrets of
// the accounts are in namespaces of tenants, and the hash ends up in a line of a
// passwd-file: Go's base64 decoder ignores a CR and a LF, so a hash with a
// newline inside its tag decodes, verifies, and would break the file.
func TestParseHashIsCanonical(t *testing.T) {
	ctx := context.Background()
	salt := "AAECAwQFBgcICQoLDA0ODw"
	tag := katHash[strings.LastIndex(katHash, "$")+1:]
	inTag := func(i int, s string) string {
		return katHash[:len(katHash)-len(tag)] + tag[:i] + s + tag[i:]
	}
	for name, stored := range map[string]string{
		"a newline in the tag":             inTag(10, "\n"),
		"a carriage return in the tag":     inTag(10, "\r"),
		"a CRLF in the tag":                inTag(10, "\r\n"),
		"a newline in the salt":            strings.Replace(katHash, salt, salt[:7]+"\n"+salt[7:], 1),
		"a carriage return in the salt":    strings.Replace(katHash, salt, salt[:7]+"\r"+salt[7:], 1),
		"a trailing newline":               katHash + "\n",
		"a trailing CRLF":                  katHash + "\r\n",
		"a leading newline":                "\n" + katHash,
		"a leading blank":                  " " + katHash,
		"a blank in the tag":               inTag(10, " "),
		"a tab in the tag":                 inTag(10, "\t"),
		"a NUL in the tag":                 inTag(10, "\x00"),
		"a non-ASCII byte in the tag":      inTag(10, "é"),
		"padding on the tag":               katHash + "=",
		"bits after the last byte (tag)":   katHash[:len(katHash)-1] + "9",
		"bits after the last byte (salt)":  strings.Replace(katHash, salt, salt[:21]+"x", 1),
		"the URL alphabet in the tag":      strings.Replace(katHash, "+", "-", 1),
		"a character outside the alphabet": inTag(10, "*"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseHash(stored); ok {
				t.Errorf("parseHash accepted %q", stored)
			}
			if validStoredHash(stored) {
				t.Errorf("validStoredHash accepted %q", stored)
			}
			if got, err := verifyHash(ctx, katPassword, stored); err != nil || got {
				t.Errorf("verifyHash(%q) = %v, %v; want false", stored, got, err)
			}
		})
	}
	if _, _, ok := parseHash(katHash); !ok {
		t.Error("the canonical hash is refused")
	}
}

func TestHashPasswordUsesARandomSaltEachTime(t *testing.T) {
	ctx := context.Background()
	a, err := hashPassword(ctx, katPassword)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword(ctx, katPassword)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
	for _, h := range []string{a, b} {
		if ok, _ := verifyHash(ctx, katPassword, h); !ok {
			t.Errorf("%s does not verify", h)
		}
		if !strings.HasPrefix(h, "{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$") {
			t.Errorf("unexpected parameters: %s", h)
		}
	}
}

func TestGeneratedPasswords(t *testing.T) {
	a, err := generatePassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generatePassword()
	if len(a) != 43 || a == b || strings.ContainsAny(a, "+/=") {
		t.Errorf("generated passwords: %q %q", a, b)
	}
}

// One hash runs at a time; a reconcile that waits for the slot gives up
// when its context ends and does not leak the slot.
func TestHashWaitsForTheSlotAndHonoursTheContext(t *testing.T) {
	if err := acquireHashSlot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := hashPassword(ctx, katPassword); err == nil {
		t.Error("a hash ran while the slot was held")
	}
	releaseHashSlot()
	if _, err := hashPassword(context.Background(), katPassword); err != nil {
		t.Errorf("the slot was not released: %v", err)
	}
}

func TestClaimKeysFoldCaseAndKeepTheServer(t *testing.T) {
	u := &cnmsv1alpha1.MailUser{
		ObjectMeta: metav1.ObjectMeta{Name: apiUserName, Namespace: "ns"},
		Spec: cnmsv1alpha1.MailUserSpec{
			ServerRef: cnmsv1alpha1.NameReference{Name: testServerName},
			DomainRef: cnmsv1alpha1.NameReference{Name: objA},
			Aliases: []cnmsv1alpha1.MailUserAlias{
				{LocalPart: "Sales", DomainRef: cnmsv1alpha1.NameReference{Name: objB}},
				{LocalPart: userSales, DomainRef: cnmsv1alpha1.NameReference{Name: objB}},
			},
		},
	}
	got := claimKeys(u)
	want := []string{"mail/alice@dom-a", "mail/sales@dom-b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("claimKeys = %v, want %v", got, want)
	}
	if got := indexMailUserByDomain(u); len(got) != 2 {
		t.Errorf("domains = %v", got)
	}
}

// The credential Secret is labelled by the server, never by the user: a
// user name has up to 64 characters and a label value 63.
func TestCredentialLabelsAreValidForALongUserName(t *testing.T) {
	r := &MailUserReconciler{Scheme: watchTestScheme(t)}
	user := &cnmsv1alpha1.MailUser{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 64), Namespace: "ns", UID: "u1"},
		Spec:       cnmsv1alpha1.MailUserSpec{ServerRef: cnmsv1alpha1.NameReference{Name: strings.Repeat("s", 44)}},
	}
	s := newCredentialShape(t, r, user)
	for k, v := range s.Labels {
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			t.Errorf("label %s=%q is not valid: %v", k, v, errs)
		}
	}
	if s.Annotations[annotationMailUser] != user.Name {
		t.Error("the name of the user is not in the annotation")
	}
	// A server name that is no label value (it cannot exist) leaves the label out.
	user.Spec.ServerRef.Name = strings.Repeat("s", 70)
	if _, ok := newCredentialShape(t, r, user).Labels[manifests.ServerLabel]; ok {
		t.Error("a label with an invalid value")
	}
}

func newCredentialShape(t *testing.T, r *MailUserReconciler, user *cnmsv1alpha1.MailUser) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := r.shapeCredential(s, user, credentialSecretName(user.Name), ""); err != nil {
		t.Fatal(err)
	}
	return s
}

// The hash that Dovecot makes is a valid Argon2id hash: the library the
// operator uses recomputes its tag from the parameters it carries. The
// production verifier does not read other parameters (TestVerifyHash), so
// this is the Dovecot-to-Go direction as a test only.
func TestTheLibraryVerifiesTheHashDovecotMade(t *testing.T) {
	rest := strings.TrimPrefix(dovecotHash, hashScheme)
	parts := strings.Split(rest, "$")
	var m, tt uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &tt, &p); err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	tag, err := b64.DecodeString(parts[5])
	if err != nil {
		t.Fatal(err)
	}
	if got := argon2.IDKey([]byte(katPassword), salt, tt, m, p, uint32(len(tag))); !bytes.Equal(got, tag) {
		t.Error("the Argon2id library does not recompute the tag of the hash that Dovecot made")
	}
	if got := argon2.IDKey([]byte(katPassword+"x"), salt, tt, m, p, uint32(len(tag))); bytes.Equal(got, tag) {
		t.Error("a wrong password gives the same tag")
	}
}
