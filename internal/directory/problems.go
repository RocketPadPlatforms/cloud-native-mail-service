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

package directory

import (
	"errors"
	"strings"
)

// The checks below say why the engines cannot be configured from a
// target. The renderers refuse such a target and the MailServer controller
// reports it as DirectoryInvalid before it renders, from the same
// functions, so the two always agree. The messages never carry a value.

// SettingsProblem reports a value of the LDAP target that Postfix and
// Dovecot would read differently from the probe: a control character
// ends a setting and starts another one, and a blank at either end is
// trimmed by the engines but not by the probe.
func (t LDAPTarget) SettingsProblem() error {
	for _, v := range []string{t.BaseDN, t.BindDN, t.BindPassword} {
		if HasControl(v) || strings.TrimSpace(v) != v {
			return errors.New("the LDAP settings hold a control character or surrounding blanks")
		}
	}
	return nil
}

// DovecotProblem reports why Dovecot cannot use the LDAP target. It
// checks SettingsProblem as well.
//
// Dovecot checks a password by binding to the directory as the user, so
// with tls.mode None every IMAP and submission login would send the
// password in clear text; and "%{" starts a variable in the base DN and
// the bind DN (a lone "%" is taken literally). Postfix alone could look
// up over a connection without TLS, but a MailServer always runs
// Dovecot, so a MailServer cannot use such a directory.
func (t LDAPTarget) DovecotProblem() error {
	if err := t.SettingsProblem(); err != nil {
		return err
	}
	if t.TLSMode == LDAPNoTLS {
		return errors.New("tls.mode None would send the password of every login to the directory in clear text; use StartTLS or LDAPS")
	}
	if strings.Contains(t.BaseDN, "%{") || strings.Contains(t.BindDN, "%{") {
		return errors.New("the base DN or the bind DN holds %{, which Dovecot would expand as a variable")
	}
	return nil
}

// SettingsProblem reports a value of the SQL target that the engines
// would read differently from the probe.
func (t SQLTarget) SettingsProblem() error {
	if HasControl(t.Password) || strings.TrimSpace(t.Password) != t.Password {
		return errors.New("the SQL settings hold a control character or surrounding blanks")
	}
	return nil
}

// HasControl reports whether s holds a control character: a newline
// would end the setting and start another one.
func HasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
