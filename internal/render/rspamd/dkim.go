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

package rspamd

import (
	_ "embed"
	"slices"
	"strconv"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// guardBody is the never-unsigned guard of ADR 0013 without its
// `required` table, which the renderer generates from the same keys as
// dkim_signing.conf so the two cannot disagree. ADR 0013 lists the
// cases it was run against in the pinned Rspamd image.
//
//go:embed guard.lua
var guardBody string

// sortedDomains returns the authorized domains, sorted.
func sortedDomains(req *render.RenderRequest) []string {
	return slices.Sorted(slices.Values(req.AuthorizedDomains))
}

// renderDKIMSigning renders local.d/dkim_signing.conf (ADR 0011,
// section 2). Only the active selectors of the authorized domains are
// listed; a selector whose key could not be read is listed too, because
// dropping it would let the guard of rspamd.local.lua stop covering it
// (the domain then fails closed instead of leaving unsigned).
func renderDKIMSigning(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString(sourceHeader(req, "local.d/dkim_signing.conf"))
	b.WriteString("# Sign with the header-From domain exactly: no reduction to\n")
	b.WriteString("# the effective second-level domain, and no guessed key for a\n")
	b.WriteString("# domain without an entry below.\n")
	b.WriteString("use_esld = false;\n")
	b.WriteString("try_fallback = false;\n")
	b.WriteString("# The envelope (VERP) domain may differ from the From domain,\n")
	b.WriteString("# and a login need not be an address; binding the login to the\n")
	b.WriteString("# sender is Postfix's job (ADR 0011).\n")
	b.WriteString("allow_hdrfrom_mismatch = true;\n")
	b.WriteString("allow_username_mismatch = true;\n")
	b.WriteString("# Sign authenticated mail and Postfix's own non-SMTP mail. Pod\n")
	b.WriteString("# addresses count as \"local\" for Rspamd, so sign_local stays\n")
	b.WriteString("# off (ADR 0013).\n")
	b.WriteString("sign_authenticated = true;\n")
	b.WriteString("sign_local = false;\n")
	b.WriteString("sign_networks = [\"127.0.0.1\", \"::1\"];\n")
	var domains strings.Builder
	for _, d := range sortedDomains(req) {
		keys := req.ActiveKeys(d)
		if len(keys) == 0 {
			continue
		}
		domains.WriteString("  " + strconv.Quote(d) + " {\n    selectors [\n")
		for _, k := range keys {
			domains.WriteString("      { path = " + strconv.Quote(render.DKIMDir+"/"+render.DKIMKeyPath(d, k.Selector)) +
				"; selector = " + strconv.Quote(k.Selector) + "; },\n")
		}
		domains.WriteString("    ]\n  }\n")
	}
	if domains.Len() > 0 {
		b.WriteString("domain {\n" + domains.String() + "}\n")
	}
	return b.String()
}

// renderLocalLua renders rspamd.local.lua: the guard of ADR 0013 with
// its table of the signatures every served domain must carry.
func renderLocalLua(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString("-- CNMS-rendered Rspamd rspamd.local.lua -- do not edit.\n")
	b.WriteString("-- Source: MailServer " + req.Server.Namespace + "/" + req.Server.Name + " (ADR 0013)\n\n")
	b.WriteString("-- CNMS never-unsigned guard. required maps each served domain to the\n")
	b.WriteString("-- DKIM_SIGNED options its active keys must have produced; an empty\n")
	b.WriteString("-- list is a served domain without a key.\n")
	b.WriteString("local required = {\n")
	for _, d := range sortedDomains(req) {
		list := "{}"
		if keys := req.ActiveKeys(d); len(keys) > 0 {
			opts := make([]string, len(keys))
			for i, k := range keys {
				opts[i] = strconv.Quote(d + ":s=" + k.Selector)
			}
			list = "{ " + strings.Join(opts, ", ") + " }"
		}
		b.WriteString("  [" + strconv.Quote(d) + "] = " + list + ",\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("-- Check 2 of ADR 0015, section 4.4: the header From of authenticated mail\n")
	b.WriteString("-- must be owned by the login. \"on\" for the accounts the operator manages\n")
	b.WriteString("-- (the table is read from owners_path), \"off\" for a directory, whose\n")
	b.WriteString("-- addresses are outside the cluster. The mode is rendered, not guessed from\n")
	b.WriteString("-- an empty table: a server without accounts refuses every address.\n")
	mode := "on"
	if req.Directory != nil {
		mode = "off"
	}
	b.WriteString("local owners_mode = " + strconv.Quote(mode) + "\n")
	b.WriteString("local owners_path = " + strconv.Quote(render.RspamdDataDir+"/"+render.RspamdOwnersFile) + "\n\n")
	b.WriteString(guardBody)
	return b.String()
}

// renderFromOwners renders the table of the owned header From addresses: one
// "address login" per line, sorted, lower case. The rows are those of the
// envelope (smtpd_sender_login_maps): the served addresses of accounts that
// may send, so the owner of a header From is the owner of the same envelope
// sender. A server without accounts has a table without a line, and the
// check refuses every address (fail closed).
func renderFromOwners(req *render.RenderRequest) (string, error) {
	plan, err := render.PlanAccounts(req.Accounts, req.AuthorizedDomains, req.ConflictedAddresses)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# CNMS-rendered Rspamd " + render.RspamdOwnersFile + " -- do not edit.\n")
	b.WriteString("# Source: MailServer " + req.Server.Namespace + "/" + req.Server.Name + " (ADR 0015, section 4.4)\n")
	b.WriteString("# Format: address login (lower case, sorted)\n")
	for _, s := range plan.Senders {
		b.WriteString(s.Key + " " + s.Value + "\n")
	}
	return b.String(), nil
}
