# ADR 0013: Never-unsigned guard

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

Principle 5 of the project plan: mail must never be lost because a
component is down, and mail of a signed domain must never leave unsigned.
`milter_default_action = tempfail` (set since Phase 2) only covers a
milter that is unreachable or times out. It does not cover an Rspamd that
is up and has nothing to sign with.

### Verified facts

Rspamd 4.2.3 (the pinned image and the `4.2.3` tag of
[rspamd/rspamd](https://github.com/rspamd/rspamd/tree/4.2.3)), run with
test keys:

- **Rspamd passes mail through unsigned.** With a `dkim_signing`
  configuration whose key file is missing, the plugin logs `cannot load
  dkim key`, inserts no `DKIM_SIGNED`, and the action is `no action`.
  With several selectors, one missing key skips only that selector; the
  message is then partly signed and `DKIM_SIGNED` is present.
- `sign_condition` only selects what to sign; nothing in the plugin
  turns "should have been signed" into a rejection.
- **"Local" includes pod IPs.** With the shipped `sign_local = true`, an
  unauthenticated message from a private address (`10.1.2.3` in the test)
  is signed. A client that reaches Postfix through a pod IP is therefore
  "local" and gets our signature. For mail to our own domain that is a
  spoofing aid, not signing.
- `task:set_pre_result('soft reject', message, module)` sets the action;
  the milter proxy answers `451 4.7.1` with that message
  (`src/libserver/milter.c`, `RSPAMD_MILTER_RCODE_TEMPFAIL`). A pre-result
  disables all symbols except those flagged `idempotent` or
  `ignore_passthrough`. Postfilter symbols run after all filter symbols,
  so `DKIM_SIGNED` is known by then.
- `rules/rspamd.lua` loads `$LOCAL_CONFDIR/rspamd.local.lua` (and
  `lua.local.d/*.lua`). `rspamadm configtest` exits 1 for a syntax error
  and for a load-time error in it, and 0 when a key file is missing.
- The Rspamd milter takes the SASL login from the `{auth_authen}` macro
  (`User` header); Postfix 3.11.7 sends it by default
  (`milter_mail_macros = i {auth_type} {auth_authen} {auth_author}
  {mail_addr} {mail_host} {mail_mailer}`).
- `lualib/lua_dkim_tools.lua` (`prepare_dkim_signing`) signs nothing when
  the From header has a second address and `allow_hdrfrom_multiple` is
  unset, and nothing when no From domain can be extracted.
- A pre-result `reject` makes the milter proxy answer `554 5.7.1` with the
  message (`src/libserver/milter.c`, `RSPAMD_MILTER_RCODE_REJECT`), unless
  `discard_on_reject` or `quarantine_on_reject` is set; CNMS sets neither.

Postfix 3.11.7 (the pinned source tarball, `README_FILES/MILTER_README`,
`proto/postconf.proto`, `src/cleanup/cleanup_milter.c`):

- `smtpd_milters` filter mail from SMTP clients; `non_smtpd_milters`
  filter mail that arrives through `sendmail(1)`, `qmqpd` and
  `postsuper -r` re-injection. For these the cleanup server simulates an
  SMTP session from `127.0.0.1` (re-injected mail keeps the original
  client).
- "Non-SMTP filters must not REJECT or TEMPFAIL simulated RCPT TO
  commands"; the Rspamd milter decides after the message, not at RCPT.
- Postfix does not apply content filters to mail it generates itself
  (bounces, notifications). `internal_mail_filter_classes = bounce,
  notify` turns that on; the README warns that a filter that **rejects**
  an internal bounce loses mail, because the resulting double bounce
  would be blocked too. A 4xx reply from a milter at end of message sets
  `CLEANUP_STAT_DEFER` in the cleanup server, which "prevents bounces from
  being sent": the message is deferred, not lost.
- There is no `{postfix_internal_origin}` macro in 3.11.7 (it arrives in a
  later release), so a milter cannot tell Postfix-generated mail from
  `sendmail(1)` mail.
- The pickup service passes `CLEANUP_FLAG_BOUNCE` for locally submitted
  mail (`src/pickup/pickup.c`), so the cleanup server bounces a milter
  rejection to the sender. This was read in the current upstream source;
  the same behaviour is expected on Postfix 3.11.7, not run.

## Decision

### 1. Who is signed

| Mail | Source of truth | Signed by CNMS |
|---|---|---|
| Authenticated submission | `{auth_authen}` present | yes, required |
| Postfix non-SMTP mail (`sendmail(1)`, re-injection) | client `127.0.0.1` / `::1`, no login | yes, required |
| Everything else: unauthenticated mail from the Internet, from other pods, from the node network | | **never** |

This is `sign_authenticated = true`, `sign_local = false`,
`sign_networks = [127.0.0.1, ::1]` (ADR 0011). A pod that talks to Postfix
without logging in is treated like any other unauthenticated client: its
mail is not signed. The guard enforces exactly the first two rows.

### 2. The guard

A postfilter symbol `CNMS_DKIM_GUARD` in `rspamd.local.lua`, rendered by
the Rspamd renderer from the same data as `dkim_signing.conf`, with the
table `required` that maps every domain the `MailServer` serves (authorized,
ADR 0003) to the `domain:s=selector` options its active keys must have
produced. A domain with no active key has an empty list; a domain whose
active key cannot be read keeps its selector (ADR 0011), so the signature
stays required and cannot appear. The file is mounted with a `subPath` at
`/etc/rspamd/rspamd.local.lua`; the Rspamd validation Job mounts it too
(ADR 0009).

```lua
-- CNMS never-unsigned guard. Rendered file: required maps each served
-- domain to the DKIM_SIGNED options its active keys must have produced.
local required = {
  ["example.org"] = { "example.org:s=cnms-20261103", "example.org:s=cnms-ed25519-20261103" },
  ["mail.example.org"] = { "mail.example.org:s=cnms-20261103", "mail.example.org:s=cnms-ed25519-20261103" },
  ["nokeys.example"] = { "nokeys.example:s=x" },
  ["empty.example"] = {},
}

local rspamd_logger = require 'rspamd_logger'

local skip_actions = { reject = true, drop = true, discard = true, ['soft reject'] = true }

local function defer(task)
  task:set_pre_result('soft reject', 'DKIM signing unavailable, try again later', 'cnms_dkim_guard')
end

-- source reports whether CNMS signs for this client: a login, or loopback
-- (Postfix's own non-SMTP mail) with a non-empty envelope sender.
local function source(task)
  if task:get_user() then return true end
  local ip = task:get_from_ip()
  if not (ip and ip:is_valid() and (tostring(ip) == '127.0.0.1' or tostring(ip) == '::1')) then return false end
  local efrom = task:get_from('smtp')
  return (efrom and efrom[1] and efrom[1].addr and #efrom[1].addr > 0) and true or false
end

local function check(task)
  if not source(task) then return end                     -- not a signed class, or a Postfix-generated bounce

  local hfrom = task:get_from('mime')
  if hfrom and hfrom[2] then                              -- Rspamd signs no message with several From addresses
    for _, a in ipairs(hfrom) do
      if a.domain and required[a.domain:lower()] then
        return task:set_pre_result('reject', 'multiple From addresses cannot be DKIM signed', 'cnms_dkim_guard')
      end
    end
    return
  end
  if not (hfrom and hfrom[1] and hfrom[1].domain) then return end  -- no From: Rspamd does not sign either
  local want = required[hfrom[1].domain:lower()]
  if not want then return end                             -- not a served domain

  if skip_actions[task:get_metric_action()] then return end  -- message is not leaving

  local have = {}
  local sym = task:get_symbol('DKIM_SIGNED')
  for _, o in ipairs((sym and sym[1] and sym[1].options) or {}) do have[o] = true end
  if #want == 0 then return defer(task) end               -- served domain without a key
  for _, w in ipairs(want) do
    if not have[w] then return defer(task) end
  end
end

rspamd_config:register_symbol({
  name = 'CNMS_DKIM_GUARD',
  type = 'postfilter',
  flags = 'ignore_passthrough',
  callback = function(task)
    local ok, err = pcall(check, task)
    if ok then return end
    pcall(rspamd_logger.errx, task, 'CNMS DKIM guard failed: %s', err)
    -- a guard bug must not let mail out
    local sok, signed = pcall(source, task)
    if not sok then sok, signed = pcall(task.get_user, task) end  -- source itself failed: defer logins only
    if sok and signed then defer(task) end
  end,
})
```

The listing is the file that was run (`internal/render/rspamd/guard.lua`);
the renderer only generates the `required` table, and the signing
configuration comes from the same keys (a render test compares the two).
Behaviour, run against the pinned image with two
selectors per domain:

| Case | Result |
|---|---|
| authenticated, all selectors signed | `no action`, two signatures |
| authenticated, one of two keys missing | `soft reject` |
| authenticated, every key missing | `soft reject` |
| authenticated, served domain with an empty key list | `soft reject` |
| authenticated, error inside the guard | `soft reject` |
| `127.0.0.1`, no login, key missing | `soft reject` |
| `127.0.0.1`, empty sender, key missing | `no action`, unsigned (exempt) |
| unauthenticated from an Internet or pod address | untouched, unsigned |
| authenticated, From domain not served | untouched, unsigned |
| authenticated, From subdomain not in the table (`use_esld=false`) | untouched, unsigned |
| several From addresses, one of them served (authenticated, or loopback with a sender) | `reject` (554 5.7.1) |
| several From addresses, none served | untouched, unsigned |
| no From header | untouched, unsigned |
| error inside the guard, loopback with a sender | `soft reject` |
| error inside the guard, loopback with an empty sender | untouched |
| the guard cannot determine the source | authenticated mail `soft reject`, everything else untouched |

### 3. Exemptions and their price

- **Empty envelope sender without a login** is exempt, so the guard never
  defers Postfix-generated bounces (README). A login with an empty
  sender is not exempt.
- **Messages that are going to be rejected or deferred anyway** are
  skipped, so the guard does not turn a spam `reject` into a retry loop,
  except several From addresses with a served domain, which the guard
  always rejects.
- **Several From addresses.** Rspamd signs no message with a second From
  address, so such a message of a served domain could leave unsigned. The
  guard rejects it rather than defers it, because no retry can ever be
  signed. Postfix bounces locally submitted mail that is rejected this way
  back to its sender; the SMTP client gets a 554 5.7.1 on submission.
- **Known gap**, deliberately not closed here: an authenticated message
  whose From domain the server does not serve is not guarded, and an
  authenticated user can put any served domain in the header From and get
  it signed (`allow_hdrfrom_mismatch` is true). Deferring it in the signer
  would only retry a message that can never be signed. The sender-login
  binding of Phase 3 (ADR 0008 backlog) does not close this gap:
  `reject_sender_login_mismatch` checks only the envelope sender. A check
  of the header From against the login is a separate backlog item, on the
  Rspamd or milter side. A held (`quarantine`) message is guarded like any
  other.

### 4. Failure semantics

| Failure | Outcome |
|---|---|
| key missing, unreadable, or does not parse | 451 4.7.1 to the SMTP client at the end of DATA; nothing leaves; the client keeps the mail |
| Rspamd down or slow | unchanged: `milter_default_action = tempfail` with the configured timeouts |
| several From addresses with a served one | 554 5.7.1 to the SMTP client; non-SMTP mail bounces to the sender; a permanent property of the message, so a deferral would only retry until the queue lifetime ends |
| guard code error | logged; authenticated and loopback mail with a sender is deferred (a deferral is retried; Postfix's own bounces never reach the milter, section 5); if the source itself cannot be determined only authenticated mail is deferred; Internet mail is never touched |
| Valkey down | no effect: keys come from mounted Secrets |
| `rspamd.local.lua` does not load | caught by the validation Job (exit 1), the old revision keeps running |

For `non_smtpd_milters` mail the 451 is a 4xx at end of message, which
Postfix turns into a deferral of that message. Nothing in Postfix 3.11.7 is
changed to make that work; `milter_default_action = tempfail` already
applies to the simulated RCPT TO.

### 5. `internal_mail_filter_classes`

Not set, and it stays unset until Postfix 3.12 or later is pinned.
Postfix-generated bounces and notifications stay outside the
milters and are therefore unsigned. Reasons:

- Postfix 3.11.7 cannot tell internal mail from `sendmail(1)` mail to the
  milter, so the guard could only exempt by empty sender, and Rspamd could
  still `reject` or `discard` a bounce by score, which loses it (README).
- The benefit is small: a bounce has an empty envelope sender, and the HELO
  identity stands in for it in Rspamd's DMARC check
  (`dmarc.lua`). The HELO name was the primary domain of the `MailServer`,
  so SPF of that domain aligned with the bounce's From (ADR 0012).
  (Amended by ADR 0014: the HELO identity, and the origin of bounces
  (`myorigin = $myhostname`), is the hostname of the server, whose SPF
  record is reported at the hostname, not at a domain.)

Revisit when a Postfix release with `{postfix_internal_origin}` is pinned
(a new ADR). `non_smtpd_milters` stays configured: it signs `sendmail(1)`
mail and re-injected mail. Whether a re-injected message
(`postsuper -r`, queue rescue) can receive a second signature or is
guarded correctly is not verified here; the queue-rescue work of Phase 9
tests it.

### 6. Tests

- **Lua against the pinned image**, outside `make test` (needs Docker): a
  script starts the digest-pinned Rspamd image with generated RSA and
  Ed25519 test keys and the files the renderer produces, and checks the
  table above through `rspamc` with `--ip`, `--user` and `--from`. Run in
  CI next to the e2e job.
- **Golden files** for `rspamd.local.lua` and `dkim_signing.conf`, and a
  render test that the guard table and the `domain` table list the same
  domains and selectors.
- **e2e (kind, Mailpit)**: an authenticated `swaks` send arrives with an
  RSA and an Ed25519 `DKIM-Signature` that verify against the records the
  `MailDomain` publishes in `status.records`; corrupting the BYO key Secret makes
  `swaks` receive `451 4.7.1` within the kubelet sync time, nothing
  arrives, and restoring the key lets the retry through; stopping Rspamd
  (and the operator, which would scale it back) defers. An unauthenticated
  pod sending to a local recipient is delivered (the inbound e2e spec) and
  stays unsigned (the guard matrix); the mailbox is not readable from the
  sink, so the e2e does not look at its headers.

## Consequences

- **Easier:** the invariant "mail of a served domain from an authenticated or
  loopback source leaves signed or not at all" is one small rule with a
  test table, independent of how keys are provided or rotated.
- **Harder:** a wrong key, a deleted Secret or a bug in the rendered Lua
  stops outbound mail of the domain until it is fixed (inbound is not
  affected); this is the intended failure direction, and Phase 9 adds the
  metric and alert for it. The guard duplicates the "who gets signed"
  conditions of `dkim_signing`, and the Lua test table is what keeps them
  in step when Rspamd is updated.
- **Limits:** the gaps in section 3; unauthenticated mail from pods is no
  longer signed (it was never meant to be); bounces are unsigned.
- **How we know it worked:** the tests of section 6 fail if the guard
  lets an unsigned authenticated message through.
