# ADR 0011: DKIM keys, signing configuration and rotation

- **Status:** Proposed
- **Date:** 2026-11-03

## Context

Phase 5 adds the `MailDomain` controller. It has to generate or accept
DKIM keys, hand them to Rspamd, and rotate them, under these constraints:

- **Never unsigned** (principle 5): signing must not depend on Valkey,
  and a missing or unreadable key must stop mail instead of letting it
  leave unsigned (ADR 0013).
- **State rule** (project plan, section 3): custom resources are restored
  from GitOps without status, and k8up does not back up Secrets. Rotation
  progress must be re-derivable, and a restore must never regenerate a
  key that still exists.
- **Bring-your-own keys** are the documented production default (keys in
  GitOps via SOPS or sealed secrets, or in an external secret store);
  operator-generated keys are for quick starts.
- **Reputation and compatibility:** RSA-2048 signs by default. An
  Ed25519 signature (RFC 8463) is an optional second signature, so
  receivers that do not know Ed25519 are unaffected and DMARC can pass on
  the RSA signature alone.

### Verified facts (Rspamd 4.2.3, the pinned image)

Source: the `4.2.3` tag of
[rspamd/rspamd](https://github.com/rspamd/rspamd/tree/4.2.3)
(`src/plugins/lua/dkim_signing.lua`, `lualib/lua_dkim_tools.lua`,
`src/plugins/dkim_check.c`, `src/libserver/dkim.c`,
`lualib/rspamadm/dkim_keygen.lua`), plus runs of the pinned image with
test keys.

- Several signatures per message need `dkim_signing.domain.<name>.selectors`
  (a list of `{ path, selector }`). The plugin signs once per entry. The
  other key sources select exactly one key per message: `signing_table`
  plus `key_table` (OpenDKIM style) returns one entry, `selector_map` and
  `path_map` fill one selector and one path, and Redis/Valkey and Vault
  are external key stores. A run with six working selectors recorded six
  `DKIM_SIGNED` options; a run with two returned two `DKIM-Signature`
  headers (one RSA, one Ed25519).
- The domain used for the lookup is the header-From domain
  (`use_domain = "header"`). With `use_esld = true` (the shipped default)
  it is reduced to the effective second-level domain, so a granted
  subdomain such as `mail.example.org` would be looked up as
  `example.org`.
- With `try_fallback = true` (shipped default) a domain without an entry
  falls back to `$DBDIR/dkim/$domain.$selector.key`; with `false` it is
  not signed.
- `sign_condition` replaces the whole eligibility logic, and nothing in the
  plugin enforces that a message ends up signed. `DKIM_SIGNED` is inserted
  once per successful signature, with the option `domain:s=selector`. A
  key that cannot be loaded logs `cannot load dkim key` and skips that
  selector only; the other selectors still sign (verified with one missing
  key file among six).
- Key files: Rspamd reads a PEM file (`-----BEGIN`), else a base64 text
  file, else raw bytes. Raw or base64 content of 32 bytes is an Ed25519
  seed and of 64 bytes a full Ed25519 secret key (libsodium layout, which
  is also Go's `ed25519.PrivateKey`); anything else is parsed as an
  OpenSSL private key (RSA PKCS#1 and PKCS#8; Ed25519 PKCS#8 when OpenSSL
  has Ed25519, which the image does). Loaded in the 4.2.3 image: RSA
  PKCS#8 PEM, RSA PKCS#1 PEM, Ed25519 PKCS#8 PEM, base64 of the 64-byte
  key as written by `rspamadm dkim_keygen -t ed25519`, base64 of a 32-byte
  seed. `rspamadm dkim_keygen` defaults to 1024-bit RSA; `-b 2048` is
  needed.
- Keys are loaded when a message is signed and cached by path. A cached
  file key is dropped when the file's mtime changes, so a replaced key
  file is picked up without a reload or restart (`dkim_check.c`,
  `dkim_module_load_key_format`).
- `rspamadm configtest` (the ADR 0009 gate) does not look at key files: it
  exits 0 with a `dkim_signing` configuration whose key paths do not exist.
  It does exit 1 for an `rspamd.local.lua` with a syntax or load-time
  error.
- A `local.d/dkim_signing.conf` overrides the shipped defaults, including
  `use_esld`, `sign_local` and `sign_networks` (verified by behaviour).

DNS record formats: RFC 6376 section 3.6.1 (`v=DKIM1; k=rsa; p=<base64
SPKI DER>`), RFC 8463 section 4 (`k=ed25519`; `p=` is the base64 of the
raw 32-byte public key, not an SPKI structure, as `rspamadm dkim_keygen`
prints it), RFC 8301 (RSA keys of at least 1024 bits MUST, 2048 SHOULD).

## Decision

### 1. Algorithms and selectors

- RSA, 2048 bits, always. Ed25519 is added with
  `spec.dkim.ed25519: true` (default `false`). Both sign every message of
  the domain (dual signing). ECDSA is not offered.
- Selector names. RSA: `<selectorPrefix>-<yyyymmdd>`. Ed25519:
  `<selectorPrefix>-ed25519-<yyyymmdd>`. The date is the UTC date the key
  was created. At most 30 + 9 + 8 characters, within the 63 of a DNS
  label.
- DKIM cannot be switched off for a domain: a `MailDomain` always has
  signing keys (principle 5).

### 2. Rspamd configuration

Rendered into `local.d/dkim_signing.conf` (names are examples):

```
use_esld = false;            # look up the header-From domain exactly
try_fallback = false;        # no key entry, no signature (and no guess)
allow_hdrfrom_mismatch = true;   # envelope (VERP) domain may differ
allow_username_mismatch = true;  # logins need not be user@domain
sign_authenticated = true;
sign_local = false;          # see ADR 0013: pod IPs are "local"
sign_networks = ["127.0.0.1", "::1"];   # Postfix's own non-SMTP mail
domain {
  "example.org" {
    selectors [
      { path = "/etc/rspamd/dkim/example.org/cnms-20261103.key"; selector = "cnms-20261103"; },
      { path = "/etc/rspamd/dkim/example.org/cnms-ed25519-20261103.key"; selector = "cnms-ed25519-20261103"; }
    ]
  }
}
```

`allow_hdrfrom_multiple`, `allow_envfrom_empty` and the rest keep their
shipped values. The `domain` table lists only the **active** selectors
(section 5) of the domains the `MailServer` serves (authorized, ADR 0003).
The same data renders the guard of ADR 0013 into `rspamd.local.lua`, so
the two cannot disagree. The Rspamd validation Job also mounts that Lua
file, because `configtest` rejects a broken one.

`allow_username_mismatch = true` is a deliberate widening: a submission
login that is not an address (`bob`, a `MailServiceAccount` name) must
still be signed or the guard would defer its mail forever. Binding the
login to the sender is the Postfix side's job (sender-login maps, ADR
0008 backlog), not the signer's.

### 3. Key material: Secrets

- **One immutable Secret per generated key**, named
  `<maildomain>-dkim-<selector>`, data key `private.key`, an
  RSA or Ed25519 **PKCS#8 PEM** (a format the pinned Rspamd loads and
  every common tool writes). Annotations `cnms.rpp.gmbh/created`
  (RFC 3339) and `cnms.rpp.gmbh/algorithm`; labels
  `app.kubernetes.io/managed-by`, `cnms.rpp.gmbh/managed-by` (the label
  of every CNMS-managed object) and `cnms.rpp.gmbh/maildomain`. The
  `MailDomain` object name is limited to 63 characters (CEL) so the label
  value is valid; the Secret name is then at most 63 + 6 + 47 = 116
  characters, within the 253 of a Secret name.
- **No owner reference.** A dangling owner reference (a Secret restored
  next to a recreated `MailDomain`, which has a new UID) makes the
  garbage collector delete the key. The Secrets are kept when a
  `MailDomain` is deleted or revoked (ADR 0003 already keeps them on
  revocation); deleting them is a manual step, documented, with a label
  selector. This is the key-reclaim policy "Retain"; there is no field
  for another policy.
- **The public key is derived, never stored**: DNS values are computed
  from the private key on every reconcile.
- **Create, never update.** The operator needs `create`, `get`, `list`
  and `delete` on Secrets (ADR 0009 already grants them, uncached). A
  create that returns `AlreadyExists` is adopted after the content is
  validated, which is what makes a restore safe: a key found under the
  deterministic name is never regenerated. The existence of the name is
  checked before a key is generated, so nothing is generated for a name
  that is taken. A Secret restored without the managed-by and maildomain
  labels is adopted in memory only: the label selector of the list does
  not find it, so it is adopted again on every reconcile (stable, never
  regenerated); restore the labels with it. The selector of a key is read
  from the Secret name (`<maildomain>-dkim-<selector>`); the algorithm
  comes from the parsed key, the `algorithm` annotation only stands in
  for a key that does not parse. A Secret of that name whose
  `cnms.rpp.gmbh/maildomain` label names another MailDomain is never
  adopted: it is reported as `KeySecretConflict` with a warning event,
  and the domain fails closed until the name is free. (An unlabeled
  Secret is still adopted.)

### 4. Bring-your-own keys

`spec.dkim.keys` (at most 8) lists keys by reference; it replaces
generation:

```yaml
dkim:
  keys:
    - selector: s2026a            # DNS label
      secretRef: {name: example-dkim, key: s2026a.key}
      signing: true               # default true
    - selector: s2027a
      secretRef: {name: example-dkim, key: s2027a.key}
      signing: false              # published, not signing yet
```

- Same namespace only (like every other reference). CEL: selectors are
  unique, at least one key signs, and `keys` excludes `rotation`,
  `ed25519` and `selectorPrefix` (the user owns selectors and schedule).
- The Secret value must parse as one of the formats of the table above
  and be RSA of at least 2048 bits (RFC 8301 allows 1024; a lower floor
  would only invite weak keys) or Ed25519; ECDSA and other types are
  rejected. At most 16 KiB. The controller derives the algorithm from the
  key; a failing key sets `DKIMKeysValid=False` with the key and the
  reason, and the other keys keep working. A missing Secret is such a
  failure, not an error: the controller reads the Secrets again after a
  minute, and otherwise every five minutes, because Secrets are neither
  cached nor watched.
- `status.dkim` lists every key with `signing: true` as active (up to
  8, so `status.dkim.active` allows 8 entries, not one per algorithm)
  and every key with `signing: false` as pending: it is published and
  does not sign (a new key before it takes over, or an old one until you
  remove it). There is no retiring state: the user removes the entry
  after the overlap.
- **User-driven rotation without a state machine.** Publish the new key
  with `signing: false`, wait until the status shows its record verified,
  flip the flags in one commit (new `true`, old `false`), and remove the
  old entry after the overlap. Everything is in spec, so it survives a
  restore and fits GitOps.
- Changed key bytes under the same selector are picked up by Rspamd
  without a rollout (mtime check, see above), but the published record
  then no longer matches: the `status.records` verification turns
  `false`. Replacing the key under a published selector is not a rotation;
  documented.

### 5. Mounting and rollout

- The keys of **active** selectors of all served domains are mounted
  into the Rspamd pods in **one projected volume** with explicit
  `items` (`path: <domain>/<selector>.key`) at `/etc/rspamd/dkim`. Pending
  and retiring keys are not mounted (they do not sign). Explicit items
  avoid file-name collisions between domains, which share selector names
  (same prefix, same day).
- The pod template names the Secrets and the item list; it never contains
  key bytes, and the ADR 0009 config hash (ConfigMap files, users Secret,
  image) does not cover them. Key contents cannot change for generated
  Secrets (immutable); a BYO change needs no rollout (mtime).
- Every Secret source of the volume is `optional: true`. The controller
  only lists keys it could read, so the pod starts; and a Secret deleted
  later does not leave the pod stuck (a non-optional source fails the
  mount on the next kubelet sync): the files vanish, Rspamd logs
  `cannot load dkim key`, and the guard defers the mail of the domain.
  A key that becomes readable, or a Secret that is replaced under the
  same selector, changes the item list and so the pod template: a
  rolling update without a validation Job, because the configuration
  hash is unchanged.
- A switch of the active key changes the rendered `dkim_signing.conf`
  and the guard table, so it goes through the ADR 0009 gate (validation
  Job, then rollout) like any configuration change. No reload is needed
  for keys; certificates keep using the reload sidecar.
- **Which keys are active.** The `MailServer` controller takes them from
  the `MailDomain` object, not from its status: the `active` selectors of
  the rotation annotation for generated keys (the Secret is
  `<maildomain>-dkim-<selector>`, data key `private.key`), the entries of
  `spec.dkim.keys` with `signing` true for your own keys. The status lists
  only keys that could be read, so a deleted Secret would drop the selector
  from the guard and let mail leave unsigned. A `MailDomain` without the
  annotation (not reconciled yet, or restored without it) has no active
  key: its domain fails closed until the `MailDomain` controller has
  written the state.
- **Key presence before rollout.** The validation Job cannot see keys, so
  the `MailServer` controller reads each active key Secret (uncached) when
  it renders, and parses it (`dkim.Parse`, the rules of section 4): a
  missing Secret or data key, or content that does not parse, is left out
  of the volume, reported as `DKIMKeysAvailable=False` (naming domain and
  selector) and an Event, and the selector stays in the `domain` table and
  in the guard table. The domain then **fails closed**: its outbound mail
  is deferred (ADR 0013), its inbound mail is unaffected. Dropping the
  selector from the configuration instead would remove the domain from
  the guard and let mail leave unsigned, so it is not done. The rollout is
  not held back: the new configuration is no worse than the old for that
  domain, and holding it would stall every other change of the server.
  Secrets are not watched, so the controller looks for a missing key again
  every minute.
- Mount modes: `defaultMode: 0440` with the pod `fsGroup` of the Rspamd
  group (11333, which is also the uid of the `_rspamd` user in the pinned
  image; the pod already sets it), read-only root file system unchanged.
  Verified in the pinned image run as 11333:11333 with a read-only root
  file system and key files owned by `root:11333` with mode 0440: the
  keys load. The same files owned by `root:root` give `Permission denied`
  and the guard defers, so the `fsGroup` is required. What the kubelet
  does to a projected volume with `fsGroup` was not verified on a
  cluster; the e2e suite covers it.
- The validation Job (ADR 0009) mounts the configuration and
  `rspamd.local.lua`, not the keys: `configtest` does not read key files
  (verified above), and a Job that failed on a missing key would hold
  back every other change of the server, which this ADR rules out.

### 6. Rotation of generated keys

`spec.dkim.rotation` as in the API (interval at least 24h, overlap
shorter than the interval). Each key is **pending**, **active** or
**retiring**; signing uses the active keys only (one per algorithm).

1. **Bootstrap.** A domain without keys gets a key per enabled algorithm,
   immediately **active**: nothing was signed before, so there is no
   older signature to protect, and waiting for DNS would make the guard
   defer all mail until the records exist (in `manual` mode that can be
   days). The status shows the records unverified until they are
   published.
2. **Start.** Per algorithm: when the active key is at least `interval`
   old (from its `created` annotation) and no key of that algorithm is
   pending, create the new key. It is **pending**: its record is added to
   the required records and published; it does not sign. A key is never
   created under a selector that already exists (a restored Secret is
   adopted, not replaced): if today's selector is taken, the creation
   waits for the next UTC day. Changing `selectorPrefix` avoids the wait.
3. **Activate.** When the pending record has been verified (ADR 0012) for
   at least 15 minutes (a constant, covering lag between authoritative
   servers), the pending key becomes active and the previous key
   **retiring** with `until = now + overlap`. A newly enabled Ed25519
   follows the same path: RSA keeps signing, so nothing waits on it. The
   hold is tracked per pending selector. A lookup that answers "not
   verified" restarts the hold; a temporary error leaves it alone. If
   several pending keys of one algorithm are ready, the newest wins and
   the older ones, which would never sign, retire with the same overlap.
4. **Retire.** At `until` the record leaves the required set (the
   `DNSEndpoint` entry is removed, or the status stops listing it) and the
   key Secret is deleted. Turning `ed25519` off retires its active key the
   same way and drops a pending one at once, since it never signed.
   RSA is mandatory: if the annotation names no usable RSA key (stale
   after a manual Secret deletion), the oldest existing RSA key signs at
   once, or a new one is created, without waiting for DNS.
5. **Stall.** A pending key whose record never verifies leaves the old key
   signing indefinitely; condition `DKIMRotation=False` with reason
   `AwaitingDNS`, never a silent switch.

The **overlap** has to cover the Postfix queue lifetime
(`maximal_queue_lifetime` is 5 days by default, verified in the pinned
Postfix 3.11.7 sources): mail is signed on acceptance and verified on
delivery, possibly days later, so a record removed earlier makes the
signature of deferred mail fail. The default overlap is therefore **120h**,
and a rotation interval of 5 days or less needs an explicit `overlap`
(CEL). The existing rules stay: the interval is at least 24h and the
overlap is shorter than the interval.

### 7. State: where it lives

| Item | Where | Re-derivable |
|---|---|---|
| Key material | key Secret (generated) or user Secret (BYO) | no, hence "never regenerate" |
| Key creation time and algorithm | Secret annotations; the date is also in the name | yes |
| Which keys are active, retiring (with `until`), and when the pending key was first verified | annotation `cnms.rpp.gmbh/rotation` on the `MailDomain` (JSON, `{"v": 1, "active": [...], "retiring": [{"selector", "until"}], "verifiedAt": {"<selector>": "<time>"}}`; `verifiedAt` is per pending selector, because a newly enabled Ed25519 key can be pending next to a rotating RSA key) | partly |
| Pending keys | implicit: a key Secret of the domain that is neither active nor retiring | yes |
| BYO key set and `signing` flags | spec | n/a |
| Everything in `status` | status | yes |

The annotation is written after the Secret exists (create, then record),
so a crash in between leaves a key that is implicitly pending. A value
that is not valid JSON or has an unknown version is treated as absent
(section 8) and reported as a warning; unknown fields are ignored. The
encoding is deterministic (sorted, UTC, second precision), so an
unchanged state never rewrites the annotation. A GitOps
tool that prunes annotations must be told to ignore this one; the docs
say so (same as for claim bindings, plan section 3).

### 8. Restore and key loss

- **Restore with Secrets and without annotation** (status is gone as
  well): the first reconcile derives the state conservatively. Per
  algorithm the **oldest** key is active (it is the one that has been in
  DNS longest), all others are pending, and the retiring list is empty;
  the derived state is then written to the annotation and the normal
  state machine continues with DNS verification, so a newer verified key
  takes over within one hold period. Keys that exist
  are never regenerated. Overlap and hold clocks restart, which only
  lengthens them.
- **Restore without the Secrets** (the default: k8up does not back them
  up): the domain is bootstrapped with new keys (step 1); the old DNS
  records are harmless leftovers. This is the **recovery path for key
  loss**: a new date gives a new selector. When the date is the same as
  the lost key's (same-day loss), change `selectorPrefix` so that the new
  selector name is different; a changed record under an unchanged selector
  is legal but breaks the verification of mail in flight.
- Recommended production setup: BYO keys from GitOps (section 4), which
  need no restore step at all. Operator-generated keys can additionally be
  backed up through the Rspamd backup-command mechanism of the project
  plan (section 8); that wiring belongs to Phase 9.

## Consequences

- **Easier:** one signing configuration form serves one or two
  signatures; BYO rotation needs no controller logic; a key never changes
  under a running pod (generated) and is never in a pod-template hash.
- **Harder:** the `MailServer` controller now reads the `MailDomain`
  annotation and key Secrets and renders volumes and a Lua file;
  `allow_username_mismatch` moves a part of sender binding to Postfix;
  generated keys are retained after a `MailDomain` is deleted (manual
  cleanup).
- **Limits:** Ed25519 is unknown to many receivers; they ignore that
  signature, so it adds nothing for them. The 15-minute hold and the
  2048-bit floor are constants of this ADR, not API fields; the overlap
  is an API field with a 120h default. A wrong-content Secret is detected on reconcile, not
  admission.
- **How we know it worked:** unit tests for key generation, parsing and
  the state machine (a fake clock and resolver); golden files for
  `dkim_signing.conf`; an e2e that sends through submission and finds an
  RSA and an Ed25519 `DKIM-Signature` at the sink; an e2e rotation with
  both selectors valid during the overlap; a restore test that deletes the
  annotation and finds the same keys in use.
