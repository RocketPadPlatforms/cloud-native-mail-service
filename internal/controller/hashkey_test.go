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
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

const usersTestDomain = "users.example.net"

// noHashKey is the key function of a render that must not need one: it
// fails when called.
func noHashKey() ([]byte, error) { return nil, errors.New("the render asked for the hash key") }

func hashKeyTestServer() *cnmsv1alpha1.MailServer {
	enabled := true
	return &cnmsv1alpha1.MailServer{
		ObjectMeta: metav1.ObjectMeta{Name: "mail", Namespace: "ns"},
		Spec: cnmsv1alpha1.MailServerSpec{
			Hostname:  "mail.example.net",
			Mailboxes: &cnmsv1alpha1.Mailboxes{Enabled: &enabled},
			Outbound:  cnmsv1alpha1.Outbound{Direct: &cnmsv1alpha1.DirectOutbound{}},
			TLS:       cnmsv1alpha1.TLS{IssuerRef: cnmsv1alpha1.IssuerReference{Kind: issuerKind, Name: "iss"}},
		},
	}
}

// The fail-closed render (no domains, no users) holds no secret content,
// so it needs no key and creates none: a Secret of a tenant under the name
// of the key cannot keep a revoked domain from being withdrawn (ADR 0003).
func TestRenderWithoutSecretContentNeedsNoHashKey(t *testing.T) {
	if _, err := renderDataPlane(hashKeyTestServer(), nil, "mail.example.net", userSource{}, nil, noHashKey); err != nil {
		t.Fatalf("fail-closed render: %v", err)
	}
}

// A render with the secret of a directory does hold secret content and gets
// the key; the accounts of the MailUsers do not: their hashes are live data
// outside the config hash (ADR 0015, section 4.7).
func TestRenderWithSecretContentUsesTheHashKey(t *testing.T) {
	src := userSource{directory: &render.DirectorySource{LDAP: &directory.LDAPTarget{
		Host: ldapTestHost, BaseDN: exampleBaseDN, TLSMode: directory.LDAPStartTLS,
		Attributes: directory.Attributes{Login: directory.DefaultLogin, Mail: directory.DefaultMail},
		BindDN:     "cn=lookup,dc=example,dc=org", BindPassword: "secret",
	}}}
	called := 0
	plane, err := renderDataPlane(hashKeyTestServer(), []string{usersTestDomain}, "mail.example.net", src, nil,
		func() ([]byte, error) { called++; return bytes.Repeat([]byte{7}, hashKeyLength), nil })
	if err != nil || plane == nil {
		t.Fatalf("render with a directory secret: %v", err)
	}
	if called != 1 {
		t.Errorf("the key was fetched %d times, want once", called)
	}
	if _, err := renderDataPlane(hashKeyTestServer(), []string{usersTestDomain}, "mail.example.net", src, nil, noHashKey); err == nil {
		t.Error("a render with a directory secret did not ask for the key")
	}
}

func TestRenderWithAccountsNeedsNoHashKey(t *testing.T) {
	users := userSource{accounts: []render.Account{{Name: apiUserName, PasswordHash: "{SHA512-CRYPT}x", Address: "alice@users.example.net"}}}
	plane, err := renderDataPlane(hashKeyTestServer(), []string{usersTestDomain}, "mail.example.net", users, nil, noHashKey)
	if err != nil || plane.LiveUsers == nil {
		t.Fatalf("render with accounts: %v", err)
	}
	if !strings.Contains(string(plane.LiveUsers.Data["passdb"]), "alice:{SHA512-CRYPT}x") {
		t.Error("the hash is not in the live users Secret")
	}
}
