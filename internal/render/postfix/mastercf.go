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

package postfix

import (
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// renderMasterCF renders master.cf. The postscreen pattern follows the
// Postfix 3.11 POSTSCREEN_README (postscreen on 25, the real smtpd as a
// "pass" service, tlsproxy and dnsblog on demand); the remaining service
// table mirrors the Postfix 3.11 shipped master.cf verbatim (the master
// needs every internal service and delivery agent to process mail). The
// submission listener uses the numeric service name "587" so it does not
// depend on an /etc/services entry; AUTH is enabled only for it.
func renderMasterCF(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Postfix master.cf -- do not edit.\n")
	b.WriteString("# Source: MailServer " + req.Server.Namespace + "/" + req.Server.Name + "\n")
	b.WriteString("#\n")
	b.WriteString("#  service   type  private  unpriv  chroot  wakeup  maxproc  command\n")
	b.WriteString("smtp      inet  n       -       n       -       1       postscreen\n")
	b.WriteString("smtpd     pass  -       -       n       -       -       smtpd\n")
	if submissionEnabled(req) {
		b.WriteString("587       inet  n       -       n       -       -       smtpd\n")
		b.WriteString("  -o smtpd_sasl_auth_enable=yes\n")
	}
	b.WriteString("pickup    unix  n       -       n       60      1       pickup\n")
	b.WriteString("cleanup   unix  n       -       n       -       0       cleanup\n")
	b.WriteString("qmgr      unix  n       -       n       300     1       qmgr\n")
	b.WriteString("tlsmgr    unix  -       -       n       1000?   1       tlsmgr\n")
	b.WriteString("rewrite   unix  -       -       n       -       -       trivial-rewrite\n")
	b.WriteString("bounce    unix  -       -       n       -       0       bounce\n")
	b.WriteString("defer     unix  -       -       n       -       0       bounce\n")
	b.WriteString("trace     unix  -       -       n       -       0       bounce\n")
	b.WriteString("verify    unix  -       -       n       -       1       verify\n")
	b.WriteString("flush     unix  n       -       n       1000?   0       flush\n")
	b.WriteString("proxymap  unix  -       -       n       -       -       proxymap\n")
	b.WriteString("proxywrite unix -       -       n       -       1       proxymap\n")
	b.WriteString("smtp      unix  -       -       n       -       -       smtp\n")
	b.WriteString("relay     unix  -       -       n       -       -       smtp\n")
	b.WriteString("showq     unix  n       -       n       -       -       showq\n")
	b.WriteString("error     unix  -       -       n       -       -       error\n")
	b.WriteString("retry     unix  -       -       n       -       -       error\n")
	b.WriteString("discard   unix  -       -       n       -       -       discard\n")
	b.WriteString("local     unix  -       n       n       -       -       local\n")
	b.WriteString("virtual   unix  -       n       n       -       -       virtual\n")
	b.WriteString("lmtp      unix  -       -       n       -       -       lmtp\n")
	b.WriteString("anvil     unix  -       -       n       -       1       anvil\n")
	b.WriteString("scache    unix  -       -       n       -       1       scache\n")
	b.WriteString("postlog   unix-dgram n  -       n       -       1       postlogd\n")
	b.WriteString("tlsproxy  unix  -       -       n       -       0       tlsproxy\n")
	b.WriteString("dnsblog   unix  -       -       n       -       0       dnsblog\n")
	return b.String()
}
