# ADR 0013: Never-unsigned guard

- **Status:** Proposed
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
produced. A domain without a usable key has an empty list. The file is
mounted with a `subPath` at `/etc/rspamd/rspamd.local.lua`; the Rspamd
validation Job mounts it too (ADR 0009).

```lua
-- CNMS never-unsigned guard. Rendered file: required maps each served
-- domain to the DKIM_SIGNED options its active keys must have produced.
local required = {
  ["example.org"] = { "example.org:s=cnms-20261103", "example.org:s=cnms-ed25519-20261103" },
  ["mail.example.org"] = { "mail.example.org:s=cnms-20261103", "mail.example.org:s=cnms-ed25519-20261103" },
  ["nokeys.example"] = { "nokeys.example:s=x" },
  ["empty.example"] = {},
}

local skip_actions = { reject = true, drop = true, discard = true, ['soft reject'] = true }

local function defer(task)
  task:set_pre_result('soft reject', 'DKIM signing unavailable, try again later', 'cnms_dkim_guard')
end

local function check(task)
  local user = task:get_user()
  local ip = task:get_from_ip()
  local loopback = ip and ip:is_valid() and (tostring(ip) == '127.0.0.1' or tostring(ip) == '::1')
  if not user and not loopback then return end            -- not a signed class

  local efrom = task:get_from('smtp')
  local empty_env = not (efrom and efrom[1] and efrom[1].addr and #efrom[1].addr > 0)
  if empty_env and not user then return end               -- Postfix-generated bounce

  local hfrom = task:get_from('mime')
  if not hfrom or #hfrom ~= 1 or not hfrom[1].domain then return end
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
    local ok = pcall(check, task)
    if not ok and task:get_user() then defer(task) end    -- a guard bug must not let mail out
  end,
})
```

The listing is the file that was run; the renderer only generates the
`required` table. Behaviour, run against the pinned image with two
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

### 3. Exemptions and their price

- **Empty envelope sender without a login** is exempt, so the guard never
  defers Postfix-generated bounces (README). A login with an empty
  sender is not exempt.
- **Messages that are going to be rejected or deferred anyway** are
  skipped, so the guard does not turn a spam `reject` into a retry loop.
- **Known gaps**, deliberately not closed here: a message with several
  `From` headers, and an authenticated message whose From domain the
  server does not serve, are not guarded. Both belong to Postfix-side
  controls (`header_checks`, sender-login maps, ADR 0008 backlog);
  deferring them in the signer would only retry a message that can never
  be signed. The sender-login binding of Phase 3 closes the second gap
  (backlog entry exists). A held (`quarantine`) message is guarded like
  any other.

### 4. Failure semantics

| Failure | Outcome |
|---|---|
| key missing, unreadable, or does not parse | 451 4.7.1 to the SMTP client at the end of DATA; nothing leaves; the client keeps the mail |
| Rspamd down or slow | unchanged: `milter_default_action = tempfail` with the configured timeouts |
| guard code error | authenticated mail is deferred, the error is logged; other mail is not touched |
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
  (`dmarc.lua`). The HELO name is the primary domain of the `MailServer`,
  so SPF of that domain aligns with the bounce's From (ADR 0012).

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
  RSA and an Ed25519 `DKIM-Signature`; corrupting the BYO key Secret makes
  `swaks` receive `451 4.7.1` within the kubelet sync time, nothing
  arrives, and restoring the key lets the retry through; scaling Rspamd
  to zero defers; an unauthenticated pod sending to a local recipient is
  delivered without a signature.

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
