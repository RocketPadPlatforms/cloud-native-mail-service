# ADR 0015: User sources, directory users and addresses

- **Status:** Accepted; nothing is implemented yet (3a, 3b and 3c below)
- **Date:** 2026-11-06

## Context

ADR 0008 decided the broad shape of Phase 3: `MailUser`, `MailServiceAccount`,
the `allowedNamespaces` consent, and one user source per server (a directory
**or** `MailUser`s). It did not design three things that decide whether the
phase is usable and safe:

- **How a directory feeds the engines.** `MailDirectory` has connection
  fields only. Nothing says which queries Postfix and Dovecot run, what a
  lookup error turns into, or how aliases come out of LDAP and PostgreSQL.
- **Who may send as which address.** Submission accepts any `MAIL FROM`
  today (`docs/backlog.md`, Phase 3). The header `From` is a second gap that
  ADR 0013 left open.
- **Several addresses per account.** A person has one mailbox but several
  addresses, in **different domains served by the same server** (for
  example `alice@a.example`, `alice@b.example`, `sales@b.example`, all
  delivering into one mailbox, with the right to send as each of them).
  Addresses are the unit of ownership, uniqueness, revocation and
  authorization, so they must be designed before the first line of rendering
  code, in the directory case as well.

The engines were checked against the pinned versions (Postfix 3.11.7 built
from `images/postfix`, Dovecot 2.4.5, `internal/images/images.go`). The
evidence is in "Verification" at the end; where this text says *verified*,
the row there says how, and what is *inferred* or *not verified* is marked
as such.

## Decision

### 1. Phase 3 is delivered in three pull requests

| PR | Content | Done when |
|---|---|---|
| **3a** `MailDirectory` | LDAP and PostgreSQL maps for Postfix (recipient validation, alias to canonical address; `451` on lookup errors) and Dovecot (auth and user lookup against the directory); the `MailDirectory` controller (configuration validity, reachability); the Postfix image with the `pgsql` map type; the Rspamd header `From` check 1 (section 4.4); the `userFilter` rename (section 4.8); e2e with an OpenLDAP and a PostgreSQL fixture | e2e: valid, alias and invalid recipients; tempfail (not reject) with the directory down; a directory user logs in by login name and by address, not by alias; IMAP reads a delivered message; the guard matrix with check 1 |
| **3b** `MailUser` and sender binding | `MailUser` with aliases across domains; credential Secrets and password hashing; the live Dovecot passdb and userdb files (section 4.7); the accounts rendered into Dovecot and Postfix; address uniqueness; **strict sender binding for every user source** (envelope in Postfix); `soft_bounce` on the `lmtp` service (4.7); the Rspamd check 2 as the last, separable commit | e2e: a `MailUser` with aliases in two domains logs in, receives on all addresses into one mailbox, sends as each, is refused for another user's address, for a foreign domain and for the null sender; a disabled user cannot log in (IMAP and SASL on 587) and still receives mail; a user added, changed and removed with no Dovecot restart; revoking the alias domain stops its addresses in the same reconcile |
| **3c** `MailServiceAccount` | The CRD, `allowedNamespaces` (a list of objects, section 4.9), the credential Secret and the NetworkPolicy in the application namespace, sender conflicts across namespaces | e2e: an application in a second namespace sends with the operator-provided Secret, only as `spec.sender`; revoking the namespace removes the credential |

3a is useful alone: a deployment with a directory can go live. Its gap is
the missing envelope sender binding for directory users until 3b; 3a says so
in the docs. Each PR carries its docs, golden files and envtest coverage, and
the checklist of section 10.

**Commit order of 3a.** Every commit builds and passes `make test` on its
own; the order is what makes that true:

1. The Postfix image with libpq, the `pgsql` map type and the build revision
   in the tag (section 9, all places together). No behaviour change.
2. The `userFilter` to `activeFilter` rename and the `attributes` fields
   (API, generated CRD and reference, samples, envtest, upgrade notes,
   section 4.8, point 1).
3. The `MailDirectory` controller (scaffold, probe, conditions, RBAC); it
   changes no rendering.
4. The render seam of section 5 (`Accounts`, `Directory`) and the Postfix and
   Dovecot directory maps, with golden files; `insecureStaticUsers` is a
   producer of `Accounts`, so the existing golden files stay as they are.
   `UserSourceConflict` is not enforced yet.
5. `UserSourceConflict`, the `IndexFields` registration and the rework of
   the e2e suite and fixtures (section 10), in one commit: the e2e
   `MailServer` sets both sources today, so the rule and the suite must
   change together.
6. Rspamd check 1 with the changed guard matrix (section 4.4).

The docs of each step go in the commit of that step.

### 2. Vocabulary

- An **address** is `local@domain`, lower case, ASCII (the pinned Postfix is
  built without SMTPUTF8/EAI, see Verification).
- A **login** is what the SASL or IMAP client sends. After authentication
  Dovecot reports the **account name**, one string per account (the object
  name of a `MailUser`; the login attribute of a directory user; the
  reserved form of a service account, section 4.9).
- The **canonical address** of an account is its primary address. Every other
  address is an **alias**. Aliases are rewritten to the canonical address by
  Postfix, and only the canonical address goes to Dovecot over LMTP.
- A **served address** is an address in a domain of the server that passes
  the domain authorization of ADR 0003 (the authorized subset after the
  ADR 0014 filter).

### 3. `MailUser` (3b)

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailUser
metadata:
  name: alice                     # the login, and the local part of the primary address
spec:
  serverRef: {name: mail}         # a MailServer of this namespace (ADR 0008)
  domainRef: {name: a-example}    # a MailDomain of this namespace: primary address alice@a.example
  aliases:                        # optional, at most 32
    - {localPart: alice, domainRef: {name: b-example}}   # alice@b.example
    - {localPart: sales, domainRef: {name: b-example}}   # sales@b.example
  displayName: Alice Example      # optional
  disabled: false
  passwordSecretRef: {name: alice-password}   # optional; key "password"
```

1. **Name.** `metadata.name` is the login. It follows the DNS-1123
   **subdomain** pattern (lower case, digits, `-`, `.`; every dot-separated
   label starts and ends with an alphanumeric), at most 64 characters (the
   local-part limit of RFC 5321). This amends ADR 0008, which said "DNS
   label": label names cannot hold the `.` of `alice.smith`. The pattern
   keeps the name safe as a path component (no leading dot, no `..`, no `/`)
   and as a passwd-file field (no `:`). Names that start with `sa.` are
   reserved for service accounts (section 4.9); a CEL rule on
   `metadata.name` refuses them.
2. **Primary address.** `<metadata.name>@<domainRef's spec.domain>`. One
   canonical mailbox per user, keyed by the name: `/srv/mail/<name>` (ADR 0004
   layout, unchanged). Because the name is unique in the namespace and the
   server lives in the same namespace, **two `MailUser`s can never have the
   same primary address**; the API server enforces it.
3. **`serverRef`/`domainRef`** are `NameReference`s, same namespace (ADR
   0002). The domain must be served by the *same* server
   (`MailDomain.spec.serverRef.name == MailUser.spec.serverRef.name`, ADR
   0014) and authorized. The controller checks both; CEL cannot.
4. **Password.** Operator-generated by default (`crypto/rand`, 32 bytes,
   base64url); or the plaintext under key `password` of
   `passwordSecretRef` (at least 12 characters; the controller reports
   `PasswordInvalid` otherwise). The operator hashes the password with
   **Argon2id** and stores the hash in an operator-owned, controller-owned
   **credential Secret** `<name>-credential` (keys `username`, `passwordHash`,
   and `password` only when operator-generated). The hash is written once
   per password and not recomputed on every reconcile: the salt is random,
   so recomputing would change the rendered data on every pass. The Secret
   records the `resourceVersion` of the source Secret (annotation
   `cnms.rpp.gmbh/password-source-version`). **Rehash rule:** when that
   `resourceVersion` differs from the annotation, the controller verifies
   the plaintext against the stored hash and rewrites the hash only if it
   does not match; it then records the new `resourceVersion` either way. A
   metadata-only write of the source Secret (a GitOps tool or an external
   secrets operator touching labels or annotations) therefore never changes
   the hash and never reaches Dovecot. Provided Secrets are read again every
   minute, like bring-your-own DKIM keys (ADR 0011), because Secrets are not
   watched (ADR 0009). A missing Secret or key is reported as
   `SecretNotFound`, the reason `MailDomain` already uses for its own keys.
   - **Concurrency.** The credential Secret is **created** once; a create
     that finds it (`AlreadyExists`) requeues and does not overwrite it.
     Every **update** carries the `resourceVersion` it read (a conflict
     requeues), and a generated `password` is never overwritten: if the
     Secret already holds one and the user has no `passwordSecretRef`, it is
     kept. Two operator replicas (leader election makes this rare) or a
     reconcile racing a restore cannot replace a working credential.
   - **How a new hash reaches the server.** A hash write is the one change
     here that no CR event announces: the source Secret is not watched (ADR
     0009), and the `MailServer` controller does not watch Secrets, so it
     would see the new hash only on its safety requeue
     (`readyRequeueInterval`, 10 minutes,
     `internal/controller/mailserver_controller.go`; the watches are in its
     `SetupWithManager`, and none is on Secrets). The `MailUser` controller
     therefore writes a non-secret **`status.credentialRevision`** (the
     `resourceVersion` of the credential Secret after the write that changed
     the hash; later metadata-only writes do not move it). The status write
     is an event on the `MailUser`, and the existing `MailUser` to
     `MailServer` watch (section 7) enqueues the server, which then lists the
     credential Secrets. The latency of a changed provided password is thus
     at most one polling interval (one minute) plus the debounce (a few
     seconds) plus the kubelet's projection into the Dovecot pod (section
     4.7, typically a minute or two); without the field it would be up to
     about twelve minutes. A generated password never changes, and a `disabled`
     flag, a deletion or an alias change is a CR event: those skip the
     polling interval.
   - **Scheme.** Argon2id is available in the pinned Dovecot
     (`doveadm pw -l`), and the Go side uses `golang.org/x/crypto/argon2`
     (BSD-3-Clause; already in `go.mod` as an indirect dependency, license
     check at 3b).
     **Hash shape (fixed):** a 16-byte random salt from `crypto/rand`, a
     32-byte tag (`argon2.IDKey` with `keyLen = 32`), and unpadded base64 in
     the PHC form with the `{ARGON2ID}` prefix that Dovecot reads:
     `{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$<salt>$<tag>`. That is
     **107 bytes**: `{ARGON2ID}` 10, `$argon2id` 9, `$v=19` 5,
     `$m=19456,t=2,p=1` 16, `$` and the salt (22 characters) 23, `$` and the
     tag (43 characters) 44. Dovecot reads that form when `doveadm pw` made
     it (verified); a hash made by the Go library is **not verified**, 3b
     tests it.
   - **Parameters: `m=19456,t=2,p=1` for every hash**, generated or
     provided password (an OWASP profile, [Password Storage Cheat
      Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html),
      read 2026-11-06, equal in strength to its other minimums; one
     profile, so no password can cost more than another). Dovecot's own
     default, `m=65536,t=3,p=1`, is **not** used: a verification needs the
     `m` of memory, anyone can start one (a primary address is public and a
     login attempt needs no valid password), and the Dovecot container is
     Guaranteed at 512 MiB (`dovecotMainResources` in
     `internal/manifests/resources.go`).
     With `passdb_use_worker = yes` and Dovecot's default of 30 workers
     (see below), 8 parallel logins at 64 MiB would already be the whole
     container limit.
   - **Memory budget.** Each verification runs in one `auth-worker` process
     that handles one request at a time, so the peak is *workers x m*.
     3b sets `passdb_use_worker = yes` for the passwd-file passdb and limits
     the workers: `service auth-worker { process_limit = 4 }`. That bounds
     the hashing memory to 4 x 19 MiB = 76 MiB (15 % of the container
     limit); excess logins queue in the auth process and cost latency, not
     memory. Without the limit the documented default of 30 workers
     would be 30 x 19 MiB = 570 MiB, more than the container has, even at
     the lower `m`. The setting `service_process_limit` (written
     `process_limit` inside `service auth-worker` in the 2.3 notation; the
     2.4 spelling inside the block is what 3b confirms) and the default of 30
     are taken from the upstream **2.4.5** documentation, which exists for
     this version: the [services
     page](https://doc.dovecot.org/2.4.5/core/config/service.html) (the
     setting `service_process_limit`, "changed: 2.4.5"), the [settings
     summary](https://doc.dovecot.org/2.4.5/core/summaries/settings.html)
     (`default_process_limit`, 100, the fallback for any service without its
     own value) and the [2.3 to 2.4 upgrade
     page](https://doc.dovecot.org/2.4.5/installation/upgrade/2.3-to-2.4.html)
     (a table of changed defaults with `service/auth-worker/process_limit`
     going from 1 to 30, and `auth_worker_max_count` replaced by "the
     service-specific process limit"). **Not run on the pinned 2.4.5**: the
     validation Job (`doveconf -n`) rejects an unknown setting, and 3b
     confirms the name there. **Fallback**, if the gate rejects the name or
     the load test below fails: first lower the limit (2 workers), then lower
     `m` (for example to the OWASP profile `m=12288,t=3,p=1`, 12 MiB, listed
     on the cheat sheet above), and keep `p=1`; if no worker limit exists
     at all, set `passdb_use_worker = no` so that hashing is serialized in
     the auth process (one verification at a time, 19 MiB), and accept that a
     login flood queues behind it. That the passwd-file passdb honours
     `passdb_use_worker` is **not verified** (upstream, [passdb
     page](https://doc.dovecot.org/2.4.5/core/config/auth/passdb.html): "only
     used by some of the passdb drivers"); if it does not, the hashing runs
     one at a time in the auth process, memory is 19 MiB and the limit is
     moot, but a
     login flood queues behind it. 3b adds a **load test** as a done
     criterion: 50 parallel failing logins for a minute must keep the
     container's peak memory (cgroup `memory.peak`) under half of its limit
     and keep mail delivery working; the measured numbers go into the docs.
     Hashes of a directory (section 4.8) are the directory owner's: their
     cost is not under the control of CNMS.
   - **Operator-generated passwords exist only in the credential Secret.**
     k8up does not back up Secrets (AGENTS.md, section 8), and the custom
     resources in GitOps carry no generated value: after a restore from
     GitOps the credential Secrets are generated again, so **every user has
     a new password** and the old ones are gone. The advice is the one for
     DKIM keys (ADR 0011): in production use `passwordSecretRef` with a
     Secret from GitOps (SOPS or sealed secrets) or an external secret
     store; generated passwords suit quick starts.
5. **`disabled: true`** removes the login and every sender entry (no login,
   no sending). **Inbound mail keeps being delivered** to the mailbox: a
   suspended account must not lose mail (principle 5). The trap: today one
   passwd-file serves both as passdb and as userdb
   (`internal/render/dovecot/dovecotconf.go`, the `passdb passwd-file` and
   `userdb passwd-file` blocks on the same `usersPath`), so dropping the
   line for a disabled user would also drop its userdb entry, and the LMTP
   delivery would fail with a permanent 5xx, a bounce.
   **The design is two files**, both keys of the one mutable Secret of
   section 4.7: a **passdb file** (`passdb`: who may log in, with the hash
   and `user=<account name>`) and a **userdb file** (`userdb`: where the
   mail goes, with uid, gid and home; no password). `dovecot.conf` points
   the `passdb passwd-file` block at the first and the `userdb passwd-file`
   block at the second. A user that is **disabled, suspended (section
   4.6), revoked or blocked by a limit (section 4.7)** is dropped from the
   passdb file and kept in the userdb file: login stops (IMAP and SASL, both
   go through the passdb), and LMTP delivery, which needs the userdb only,
   keeps working, so queued and incoming mail is delivered and not
   bounced. Both files change in one Secret update, which the kubelet
   projects as one unit, so the passdb and userdb never disagree with each
   other. **Not verified on 2.4.5:** two separate passwd-files, a passdb file
   whose uid, gid and home fields are empty, and the userdb file with an
   empty password field; the e2e test of 3b is the check: a disabled user
   cannot log in by IMAP **and** by SASL on 587 and still receives mail.
   **Alternative, not chosen: the passdb extra field `nologin`** on the
   user's lines in one file ([upstream passdb
   page](https://doc.dovecot.org/2.4.5/core/config/auth/passdb.html): "User
   isn't actually allowed to log in even if the password matches"). It was
   not chosen because it is unverified on 2.4.5 and the same page calls the
   field "mainly intended for user logins (IMAP, POP3, ManageSieve)", so it
   may not stop SASL authentication for the submission listener on 587,
   which is the login that matters most for a suspended account; and the
   page says it also blocks LMTP "as long as `lmtp_proxy = yes`", which CNMS
   does not set, so delivery would depend on a condition nobody tested.
6. **Status:** `status.address` (canonical), `status.addresses[]`
   (`address`, `kind: primary|alias`, `served`, `reason`; at most 33),
   `status.credentialSecretName`, `status.credentialRevision` (the
   `resourceVersion` of the credential Secret after the last write that
   changed the hash; point 4 says why it exists; not secret),
   `observedGeneration`, `conditions`.
   Printer columns: `Address`, `Server`, `Ready`, `Age`. Conditions are in
   section 8. The `MailUser` controller writes `status.addresses[]`: to
   decide a conflict it needs every address claimant of the server, so the
   conflict decision is a pure function of the objects (like the domain
   authorization, ADR 0014) in one shared package that both the `MailUser`
   and the `MailServer` controller call; they reach the same verdict, the
   `MailUser` controller writes it, the `MailServer` controller renders it.
7. **The server has one user source.** A `MailServer` with
   `spec.directoryRef` and `MailUser`s that reference it does not pick one
   silently: its `Ready` is `False` with reason `UserSourceConflict`, the
   previous revision keeps running, and each `MailUser` reports
   `DirectoryConfigured`. The same holds for
   `submission.insecureStaticUsers` together with either (3a: this makes the
   server of the e2e suite, which sets both, a conflict; see section 10).
   `NoUserSource` (existing reason) means: no directory, no static users, no
   `MailUser` that references the server and, from 3c, no
   `MailServiceAccount` accepted by it. Service accounts add to either user
   source (section 4.9): a server whose only accounts are service accounts
   is a send-only server and is not stuck in `NoUserSource`.

### 4. Addresses and aliases

#### 4.1 API shape: `{localPart, domainRef}`

`aliases` is a list of `{localPart, domainRef}` with `MaxItems=32`, not a
list of address strings. Reasons: the primary already uses `domainRef`;
references by object name make a typo in the domain part impossible, survive
a rename of `spec.domain` of a `MailDomain` (it is immutable today, but the
reference does not rely on it), and let the controller resolve the domain
without a cluster-wide lookup by name; the authorization and the server
binding are properties of the `MailDomain` object anyway. The price is more
verbosity, which the 32-item cap keeps bounded. A flat form with the
domain name in place of the `domainRef` was considered and rejected for the
same reasons.

- **`localPart`:** `^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`, at most 64
  characters (CEL: `!self.contains('..')`). Lower case only (Postfix folds
  lookup keys and Dovecot lower-cases logins, verified). `+` is excluded:
  `recipient_delimiter` is empty by default, and `+` is not in Dovecot's
  default `auth_username_chars`. No quoted or non-ASCII local parts.
- **CEL, two rules.** A `listType=map` is not possible: the key would be a
  field of the nested `domainRef` object, and `listMapKey` takes top-level
  scalar fields only. Both rules must work when `aliases` is **omitted**: at
  the root, `self.spec.aliases` fails with "no such key" for an object
  without the field, which would refuse every user that has no alias.
  - *No two aliases are equal*, on the **`aliases` field** (there `self` is
    the list, and the rule is not evaluated when the field is absent):

    ```
    self.all(a, self.exists_one(b, b.localPart == a.localPart && b.domainRef.name == a.domainRef.name))
    ```

    It is quadratic in the number of items: 32 x 32 = 1,024 comparisons of
    strings bounded by the `localPart` limit (64) and the name limit of
    `NameReference` (`MaxLength=253`, `api/v1alpha1/common_types.go`). The
    `MaxItems=32` and these `MaxLength`s are what the API server's cost
    estimate needs.
  - *An alias is not the primary address*, at the **root** of `MailUser`
    (`metadata.name` is available there), guarded with `has()`:

    ```
    !has(self.spec.aliases) || !self.spec.aliases.exists(a, a.localPart == self.metadata.name && a.domainRef.name == self.spec.domainRef.name)
    ```
  - **Not verified:** that the API server accepts both rules and their cost
    estimate. 3b tests it by creating a `MailUser` **with no `aliases`**
    (not only by installing the CRD), one with aliases, one with a
    duplicate alias and one whose alias is the primary address.
- **Controller:** each alias domain must exist in the namespace, be served by
  **the same server**, and be in the authorized subset of the server's
  evaluation (ADR 0003, 0014). An alias that fails any check is dropped
  from the maps and reported in `status.addresses[]` with one of
  `AliasDomainNotFound`, `AliasDomainNotServed`, `AliasDomainNotAuthorized`,
  `AddressConflict`. The user itself keeps working with its other addresses
  (fail closed **per address**, not per user).

#### 4.2 Uniqueness per server and conflicts

The address space of a server is the union of the primary and alias
addresses of its `MailUser`s, the `spec.sender` of its `MailServiceAccount`s
(3c) and, for a directory, the addresses the directory returns. An address
has at most one owner. Rules, in the style of `DuplicateDomain` (ADR 0003):

1. **Rank.** (1) the primary address of a `MailUser`; (2) an alias of a
   `MailUser`; (3) the `spec.sender` of a `MailServiceAccount`. A claimant
   of a lower rank **loses** the address to any claimant of a higher rank.
   Claimants of the same rank **all lose** it (no "older wins": creation
   time does not survive a restore, ADR 0003). The rank is a function of
   the objects only, not of time or status.
2. **Why a primary always wins.** The name is unique in the namespace, so
   primaries cannot collide with each other. An alias or a sender that
   names someone's primary address therefore cannot take it away: a
   tenant who may create `MailUser`s cannot knock out another user's
   mailbox by claiming its address (the denial of service that "all
   claimants lose" would give for primaries). It can only deny an address
   to claimants of the same rank. For ranks 1 and 2 that is the tenant's own
   misconfiguration (the `MailUser`s of a server are in one namespace), and
   it is documented like `DuplicateDomain`. **For rank 3 it is not:** the
   service accounts of two namespaces that the server consents to
   (section 4.9) can name the same `spec.sender`, and then **both lose** it,
   so a consented namespace can deny another one its address (a
   cross-tenant denial of service; a distinct third rank is no help,
   because the claimants are of the same rank). The mitigation is a
   configuration rule: give every consented namespace its own
   `senderDomains` (section 4.9), so no domain is shared. The local-part
   grant that closes it for a shared domain is in the backlog.
3. **Per address, one place.** A lost address is removed from the maps and
   reported in one way: the entry in `status.addresses[]` of the claimant
   has `served: false` and reason `AddressConflict`, and the condition
   `AddressesServed` of the `MailUser` is `False` with reason
   `PartiallyServed` (its message names the address and the owner's rank).
   On the `MailServer` the warning `AddressesServed` says the same and never
   feeds into `Ready`. The other addresses of the same accounts are
   unaffected.
4. **Mail to a conflicted address is deferred, not rejected.** It is listed
   in a `recipient_defer` table (`450 4.2.1`), checked before the
   recipient restrictions. A conflict is an operator-correctable state; a
   permanent `550` would bounce mail for an address that exists again after
   the fix. An address that is simply **not in the spec** (alias removed,
   user deleted) is not in any map: `550 5.1.1`, as the requirement says.
5. **The key of a conflict** is `<local part>@<MailDomain object name>`,
   which is exact because a domain served by one server has one authorized
   `MailDomain` (`DuplicateDomain`, ADR 0003), and is what an index of
   `MailUser`s by claimed address uses (section 7), so a conflict reaches
   the claimants and not every user of the server.
6. **Directory addresses are outside the cluster** (section 4.8): the
   operator cannot see them, so conflicts between a directory and a
   `MailServiceAccount` sender cannot be decided from the objects; 3c
   probes for them (section 4.9).

#### 4.3 Receiving

Postfix renders, for operator-managed accounts, plain `texthash` tables in
the ConfigMap (ADR 0009):

```
virtual_mailbox_domains = <all served domains>
virtual_alias_domains   =                               # explicit, empty
virtual_alias_maps      = texthash:/etc/postfix/valias   # alias -> canonical
virtual_mailbox_maps    = texthash:/etc/postfix/vmailbox # canonical only
virtual_transport       = lmtp:<dovecot service>:24
```

- `virtual_alias_domains` **must be set to empty**: its default is
  `$virtual_alias_maps` (verified with `postconf -d` on 3.11.7), so Postfix
  would look every domain up as a key in the alias map. With an LDAP or SQL
  alias table that is one useless query per domain and a way for a directory
  value to turn a domain into an alias domain.
- `valias` maps each alias to the canonical address, never to another alias,
  so there are no chains or loops. The canonical address must be a served
  address (section 4.6 explains why). Verified: an alias in a second
  `virtual_mailbox_domain` is accepted at `RCPT` without being in
  `virtual_mailbox_maps`; an unknown address of the same domain gets
  `550 5.1.1`; Postfix hands the **canonical** address to LMTP
  (`to=<alice@a.example>, orig_to=<sales@b.example>`), and two aliases of the
  same user in one transaction produce **one** delivery.
- The mailbox path stays keyed by the account name: Dovecot sees only the
  canonical address, whose userdb line carries the home
  `/srv/mail/alice` (verified: a message for `sales@b.example` landed in
  `/srv/mail/alice`).
- Directory sources use the same contract through `ldap:` and `pgsql:`
  tables (section 4.8). **A lookup error is a temporary failure**, never a
  reject. Verified on Postfix 3.11.7: for LDAP a directory that is down, a
  rejected bind (wrong password) and a failed TLS verification (wrong CA),
  for PostgreSQL a failed TLS verification, all end in the reply
  `451 4.3.0 Temporary lookup failure` at `RCPT` (LDAP also through
  `proxy:`). A missing entry is the only thing that gives `550 5.1.1`.

#### 4.4 Sending and sender binding

**Rule (strict, no switch):** an authenticated account may send only as an
address it owns, in a domain the server serves and authorizes. Owned means:
a `MailUser` its primary and aliases; a service account its `spec.sender`; a
directory user the addresses the directory returns for that account.

Envelope sender (Postfix, on the submission listener only, so port 25
mail from other servers is unaffected):

```
smtpd_sender_login_maps = texthash:/etc/postfix/sender_login   # every owned address -> account name
smtpd_sender_restrictions = check_sender_access texthash:/etc/postfix/null_sender,
                            reject_non_fqdn_sender, reject_sender_login_mismatch
```

- `sender_login` lists **every** owned address (primary and every alias, any
  served domain) with the **account name** as owner. Verified: an
  authenticated `alice` is accepted as `alice@a.example`,
  `alice@b.example`, `sales@b.example` and `ALICE@B.EXAMPLE`; refused
  (`553 5.7.1 ... not owned by user alice`) as `bob@b.example`,
  `nobody@a.example` (served domain, no owner) and `alice@c.example`
  (foreign domain). `reject_sender_login_mismatch` rejects both a sender
  with another owner and a sender with **no** owner, which is what closes the
  "anything goes" gap for the envelope.
- **The null sender must be refused separately.** `reject_sender_login_mismatch`
  accepts `MAIL FROM:<>` from an authenticated client (verified). The
  `check_sender_access` table `<> REJECT` (key `<>`, the pinned
  `smtpd_null_access_lookup_key`) refuses it (verified, `554 5.7.1`).
  Consequence to document: mail with a null sender cannot be submitted on
  587. That covers mail that is legitimately sent with one, delivery status
  notifications and the message disposition notifications of RFC 8098 (and,
  in Phase 4, the replies of a Sieve vacation action): whatever generates
  them in Dovecot must reach Postfix on a path other than the authenticated
  587 listener. The path is a Phase 4 decision; this ADR only states that 587
  will not take them.
- **The owner must be the same string for every way of logging in** (4.5);
  otherwise `sender_login` would need an entry per login form.
- **master.cf syntax.** A `-o` value that contains spaces must use the
  braces form, `-o { smtpd_sender_restrictions = a, b }`. Without braces
  Postfix 3.11.7 fails when it starts the service
  (`fatal: unexpected command-line argument`), and neither `postfix check`
  nor `postconf -n` notices it (verified). The renderer uses braces or
  space-free values, and the e2e suite starts the listener.
- Lookup errors of `smtpd_sender_login_maps` end in `451 4.3.0` (verified
  for `pgsql:`; for `ldap:` **inferred** from the recipient maps, not run).
- Address-to-login lookups are case-insensitive (verified).

Header `From` (Rspamd). The envelope check does not bind the header: an
account can submit `MAIL FROM:<alice@a.example>` with `From: ceo@b.example`,
and Rspamd signs it for `b.example` because the guard only looks at served
domains (ADR 0013, "Known gap"); and with a header `From` in a domain the
server does not serve, with no `From` at all or with one that has no domain,
the message leaves **unsigned**: the guard returns early when the `From` has
no domain or none of the domains is served (`internal/render/rspamd/guard.lua`,
`check`: `if not (hfrom and hfrom[1] and hfrom[1].domain) then return end`,
and `if not want then return end`). So envelope binding alone does not close
the unserved-`From` gap. Two Rspamd checks for **authenticated** mail (a
login; loopback mail of Postfix itself is not touched), both permanent
rejects (`554 5.7.1`), because no retry makes such a message legitimate (the
same reasoning as ADR 0013's several-`From` rule):

1. **Check 1, in 3a: every header `From` address has a domain, and that
   domain is a served domain** (data-free, every user source, and the only
   check that needs no data about accounts). A missing `From` header, a
   `From` without a domain (`From: bob`), an unserved domain and a
   subdomain of a served domain (only exact served domains count) are
   refused. An **empty-string domain counts as missing**: in Lua `''` is
   truthy, and the guard tested only for `nil`, so the check compares
   against `''` as well (`internal/render/rspamd/guard.lua`, `check`: a
   domain that is missing or empty is not in the `required` table). This
   closes the unserved-`From` gap for every user source and changed the
   expectations of the guard matrix: before check 1 the matrix of
   `hack/test-rspamd-guard.sh` expected four authenticated cases to pass
   untouched (domain not served, subdomain of a served domain, several From
   addresses none of them served, no From header, via the early returns
   quoted above); they are now rejects, and the cases `From` without a
   domain and an empty `From` are added. Unauthenticated and loopback cases
   stay untouched.
2. **Check 2, in 3b (the last commit, separable): every header `From`
   address is owned by the login** (the whole address is lower-cased before
   the lookup, not only the domain, as Postfix does for `sender_login`),
   from a map `address login` rendered
   into its own immutable Rspamd ConfigMap (section 4.7 gives the size),
   for operator-managed accounts only. For a directory the owned set is not
   in the cluster; the **residual risk** is that a directory user can put any
   *served* address in the header `From` and have it signed. Options for
   later: a directory lookup from Rspamd (`ldap`/SQL module), or a policy
   service (backlog). The residual risk is documented in the docs; check 1
   still applies.

Both checks use the guard's existing view (`task:get_user()`,
`task:get_from('mime')`, the served-domain table). Check 1 is implemented
and verified (row 19 of Verification; the reply is `554 5.7.1`, message
`From must be an address in a served domain`); check 2 is **not verified
here**: the matrix run is the done criterion of 3b. Check 1 also refuses
several From addresses none of which is served (authenticated mail only);
a served one keeps the several-From message of ADR 0013.

**DKIM and aliases.** Each alias domain signs with the key of **its own
domain** (ADR 0011: keys are per domain, selected by the header `From`
domain). A user who sends as `alice@b.example` while `b.example` has no
usable key gets the guard's deferral (`451 4.7.1`, ADR 0013): the
consequence is user-visible and intended (mail is never sent unsigned).
`DKIMKeysAvailable` on the server names the domain. Envelope and header
domains may differ (envelope `alice@a.example`, header `alice@b.example`):
both are owned, DKIM aligns with the header domain. The `Sender:` header is
not bound by either check (it is not part of DMARC alignment, and no run
looked at how clients display it).

#### 4.5 Login

A user may log in with the **account name** (`alice`) or the **primary
address** (`alice@a.example`), case-insensitively. **Aliases are not login
identifiers.** Reasons: an alias like `sales@b.example` is published widely,
so it would hand an attacker a valid login name for free and multiply the
usernames that Dovecot's failure delay and any external fail2ban have to
account for; the narrower set keeps enumeration and brute-force surface the
same as for accounts without aliases.

Mechanism (verified on Dovecot 2.4.5): the passwd-file (or passdb query)
accepts both forms and returns `user=<account name>` as an extra field, so
the authenticated user is always `alice`. Postfix then sees
`sasl_username = alice` for both forms (verified through the
`not owned by user alice` replies after logging in with the address), and
`sender_login` needs one owner string. The existing renderer writes two
lines per user without `user=`; 3b adds the field to the two lines of the
passdb file (and the userdb file gets the same two keys: the lookup after
a login is by the account name, LMTP's by the address). The check ran with
one passwd-file; the two-file form is **not verified** (section 3, point
5). Aliases are not in the passdb, so `doveadm auth test alice@b.example`
fails (verified for passwd-file, LDAP and PostgreSQL). `auth_username_format`
keeps its default `%{user | lower}`.

#### 4.6 Revocation and lifecycle

Authorization is a function of the current objects (ADR 0003); the
`MailServer` renders all address maps in the same pass as the domains, so
revocation needs no extra mechanism.

- **Alias domain revoked** (grant gone, `serverRef` moved, `MailDomain`
  deleted, ADR 0014): the alias leaves the maps in the same reconcile as the
  domain leaves `virtual_mailbox_domains`. Mail to it is refused as for any
  domain the server does not serve (`554 5.7.1 Relay access denied`). The
  fail-closed render of ADR 0003 applies unchanged: the user data is part of
  the input that can block a normal render, and when it does, the
  fail-closed render (no domains, no users) replaces the running
  configuration.
- **Primary domain revoked.** The account is **suspended as a whole**: no
  login (its passdb line is dropped, its userdb line stays, section 3,
  point 5), no sending, no new mailbox delivery, and its aliases in
  still-authorized domains are removed from `valias`. Reason: an alias
  is rewritten to the canonical address; if that address' domain is not in
  `virtual_mailbox_domains`, Postfix would treat the rewritten recipient as
  a **remote** address and send the mail out to that domain's real MX.
  Mail to the surviving aliases is **deferred** (`450 4.2.1`, through
  `recipient_defer`, as for conflicts), not rejected: a revocation can be
  transient (a GitOps prune and re-create, ADR 0003) and a permanent `550`
  would turn it into lost mail. The `MailUser` reports
  `Ready=False, reason=DomainNotAuthorized`.
- **The two cases differ on purpose.** Mail to an address in a revoked
  domain gets a permanent `554 5.7.1` today, because the fail-closed render
  of ADR 0003 and ADR 0014 has `reject_unauth_destination` and no
  `unauth_destination_reject_code`, so it bounces instead of letting the
  sender retry; mail to a surviving alias of a suspended account, whose own
  domain is still authorized, gets `450`. The backlog item "Make the
  fail-closed render defer" (`docs/backlog.md`, Phase 5) would remove the
  difference; this ADR does not depend on it.
- **Alias removed from the spec:** the address is in no map, `550 5.1.1`.
- **`disabled: true`:** see section 3, point 5.
- **User deleted:** the entries leave the maps and both Dovecot files in the
  next reconcile (4.7, cost 2 says what that means for mail that Postfix
  still accepts); the
  credential Secret is garbage-collected (owner reference). **The mailbox
  data stays on the volume** (`/srv/mail/<name>`), and Phase 4 adds an
  explicit purge operation; nothing is ever deleted automatically. The
  mailbox is keyed by the **name**, not by the object's UID: a UID is new
  after a restore from GitOps (AGENTS.md section 8). Consequence to settle
  in Phase 4: a new `MailUser` of the same name inherits the old mailbox.
- **Operator down:** nothing changes; the data plane runs from the rendered
  objects (principle 4).

#### 4.7 Live user data for Dovecot, limits and sizes

**The exception to ADR 0009.** ADR 0009 puts every rendered file into an
immutable, hash-named object and sends every change through the validation
Job and a rollout. For the users of Dovecot that would mean a **Dovecot
restart** (one replica: IMAP, LMTP and auth are down; Postfix queues and SASL
answers `454 4.7.0`; no mail is lost) for every added user, changed
password or removed alias, and a restart of every open IMAP session. That is
too much for what a password change is. 3b therefore makes the Dovecot
passwd-files an explicit exception:

- **The object.** The two files of section 3, point 5 (`passdb` and
  `userdb`) live as two keys of one Secret with a **stable name**
  (`<server>-dovecot-users`, owned by the `MailServer`), **mutable**,
  updated in place by the operator. One object, so that one projection
  changes both files together. It is mounted as a **whole directory**,
  not with `subPath` (the kubelet never updates a `subPath` mount). The
  existing mount already is a directory mount (`usersVolume` and
  `dovecotMounts` in `internal/manifests`). The Secret is **outside the
  pod-template content hash** (ADR 0009 puts it in the hash today), so a
  change of a user is not a new revision: no Job, no rollout, no restart.
- **What stays as ADR 0009 says.** Postfix maps (`vmailbox`, `valias`,
  `sender_login`, `recipient_defer`, `null_sender`) and the Rspamd data stay
  in immutable, hash-named objects behind the validation gate. Postfix
  mounts its files with `subPath` and rolls one pod at a time behind its
  disruption budget; Rspamd rolls with `maxUnavailable: 0` and a surge.
  The Dovecot **structure** (`dovecot.conf`, the directory settings) also
  stays in the immutable ConfigMap. The validation Job never read the users
  files (`doveconf -n` does not open them and the Job mounts no Secret, ADR
  0009), so nothing that was validated is lost.
- **Cost 1: propagation.** The kubelet projects a changed Secret into the
  pod after its next sync: the sync period (one minute by default) plus the
  cache propagation delay, so typically a minute or two, and longer under
  load; not measured here. Dovecot re-reads a changed passwd-file by itself
  (verified for an in-place edit and an atomic replace of one file; the
  kubelet's symlink swap and two files are **not verified**, an e2e test of
  3b does exactly that: add a user and log in, change a password, disable,
  delete).
- **Cost 2: Dovecot and Postfix disagree for a while.** The Dovecot files
  change after the projection (a minute or two), the Postfix maps after the
  validated rollout (minutes), and nothing orders the two: a rolling Postfix
  revision can finish before the kubelet has projected the Secret. Cases:
  - A **new** user, Dovecot first: Postfix does not know the address yet and
    answers `550 5.1.1` at `RCPT` (nothing is lost, the sender is told).
  - A **new** user, Postfix first: Postfix accepts the mail, Dovecot has no
    userdb line yet and LMTP answers `550 5.1.1`; without more, Postfix
    bounces mail it has already accepted.
  - A **deleted** user (or a changed primary address, which is a delete and
    a new user): Postfix still accepts mail for the old address, its userdb
    line is gone, LMTP answers `550 5.1.1`, and the accepted mail bounces.
  - A **disabled, suspended, revoked or blocked** user is **not** one of
    these cases: its userdb line stays (section 3, point 5), so mail that
    Postfix accepts is delivered.

  The guard against the bounces of accepted mail is **`soft_bounce = yes` as
  a per-service override on the `lmtp` entry of `master.cf`** (the renderer
  at `internal/render/postfix/mastercf.go`, the `lmtp` line: add the line
  `-o soft_bounce=yes` under it; `main.cf` does not change). Postfix then
  turns a permanent reply of the LMTP delivery into a deferral: the mail
  stays in the queue and is retried until the Secret has been projected, or
  until the queue lifetime ends. **Not verified on Postfix 3.11.7**: that
  the parameter is honoured as a per-service `-o` override for the `lmtp`
  client, and what `postqueue -p` and the log show; 3b verifies it with
  exactly that setup (a `550` from a stub LMTP server must give a deferral,
  and `smtpd` must still reject unknown recipients permanently). The price:
  any permanent LMTP error stays queued instead of bouncing, for example a
  mailbox that does not exist for another reason; it is bounded by the queue
  lifetime and visible in the queue. If the check fails, the reverse order
  is a **documented worst case** (accepted mail for a new or deleted address
  bounces within the window) and the backlog item of keeping the userdb
  line of a removed user until the Postfix revision has rolled out
  becomes the fix.
- **Cost 3: the revocation latency.** For the security-relevant changes
  (disable, delete, password change) the propagation window **is** the
  revocation latency: the old password, or a removed login, keeps working
  until the passdb file is projected and re-read. A change that is a CR event
  (disable, delete, alias change) takes the debounce of a few seconds plus
  the projection, typically a minute or two. A changed **provided password**
  adds up to one polling interval of the `MailUser` controller (one minute),
  and reaches the server through `status.credentialRevision` (section 3,
  point 4), not through the ten-minute safety requeue. The latency is not
  zero and is documented as such, and it is not a bound under load.
  **Open IMAP sessions survive revocation.** Dovecot authenticates at login
  only, an IDLE session can last for days, and where the old design's restart
  ended every session, this one ends none. Ending them needs
  `doveadm kick`, and the operator has no path to run it (no `pods/exec`;
  the cert-reload sidecar only signals the master). The manual procedure,
  which the 3b docs and the runbook give: after revoking an account run
  `kubectl exec -n <ns> <server>-dovecot-0 -c dovecot -- doveadm kick <user>`
  with the account name for `<user>` (**not verified on 2.4.5**: that the
  `doveadm` socket is reachable in the pod as the container runs, and that
  `kick` finds the session), or, if that does not work, restart the Dovecot
  pod with `kubectl rollout restart statefulset/<server>-dovecot`, which
  ends every session, and say so. Automation (a sidecar that kicks revoked
  accounts) stays in the backlog.
- **RBAC grows, and says so.** The operator has **no** `update` on Secrets
  today (`config/rbac/role.yaml`; the markers in `mailserver_controller.go`
  and `maildomain_controller.go` say `get;list;create;delete`). 3b adds the
  verb **`update`** on `secrets` (not `patch`, not `watch`; reads stay
  uncached, ADR 0009). The credential Secret `<name>-credential` is updated
  in place by the same verb. The role is a `ClusterRole`, so this lets the
  operator overwrite **any** Secret in any namespace it can reach. That
  adds less than it appears to: the operator can already create and delete
  Secrets cluster-wide, so it can already replace any Secret by deleting and
  re-creating it; `update` adds the overwrite in place, without the delete.
  An optional hardening is a `ValidatingAdmissionPolicy` that lets the
  operator's identity update only Secrets that carry the CNMS managed-by
  label (`cnms.rpp.gmbh/managed-by`, `internal/manifests`); it is in the
  backlog, because CNMS ships no admission policy today. Alternatives that
  were considered: a namespaced `Role` and `RoleBinding` per namespace that
  holds `MailServer`s, created by the cluster admin (feasible, and the
  documented hardening for a locked-down cluster, but every new namespace
  needs it, and the operator cannot grant it to itself without `escalate`);
  `resourceNames` (not feasible, the names depend on the server name); and
  delete-and-recreate of the same name, which needs no new verb but leaves a
  window in which the kubelet cannot refresh and a pod start finds no Secret
  (not tested, and worse). The decision is `update` in the `ClusterRole`; the
  Helm chart of Phase 10 can offer the namespaced form.
- **The cleanup of ADR 0009** deletes Secrets that are neither referenced
  nor in the desired set; the users Secret is in the desired set and is
  created before the first workload (empty files are valid).
- **Alternative: restart.** One restart-based rollout per user change is the
  design this replaces. It stays in the backlog as the option for clusters
  that cannot grant `update`.

**Sizes and limits.** A ConfigMap or Secret is limited to 1 MiB (the API
server sums the keys and values of `data`; **not verified** on the pinned
Kubernetes version, so the guard below, which counts the keys as well, is
the conservative reading). That limit is **per object**: the revisions of
ADR 0009 are separate objects, and old and new together cost etcd space
(negligible against its quota) but do not share the 1 MiB. Sizes with
realistic and with worst-case lengths (worst case: a local part of 64 and a
domain of 253 characters, an address of 318 bytes, an account name of 64;
Argon2id hash of 107 bytes; uid and gid 1000, home `/srv/mail/` and the
name, 74 bytes):

| Object | Per entry, realistic (25-character addresses, 8-character names) | Per entry, worst case | At the caps, worst case |
|---|---|---|---|
| Dovecot `passdb` file, key of the Secret: `key:hash` and six colons, `user=<name>`, newline; address line, name line | 153 + 136 = about 290 bytes per user | 502 + 248 = **750 bytes per user** (key 318 or 64, hash 107, `user=` 5, name 64, separators 7, newline 1) | 1,000 users: 750,000 bytes |
| Dovecot `userdb` file, key of the same Secret: `key::1000:1000::<home>:`, newline; address line, name line | 114 + 97 = about 210 bytes per user | 407 + 153 = **560 bytes per user** (key 318 or 64, uid and gid 8, home 74, separators 6, newline 1) | 1,000 users: 560,000 bytes |
| Both files, the one Secret `<server>-dovecot-users` | about 500 bytes per user | **1,310 bytes per user** | 1,000 users: 1,310,000 bytes, **over the guard**; the guard stops at about 760 worst-case users |
| Postfix `valias` (ConfigMap) | 50 bytes | 638 bytes (318 + 1 + 318 + newline) | |
| Postfix `sender_login` (ConfigMap) | 40 bytes per address | 384 bytes (318 + 1 + 64 + newline) | |
| Postfix, one alias, both tables | 90 bytes | **1,022 bytes**: about 1,000 aliases fill 1 MiB | 4,000 aliases: about 4 MB, **over 1 MiB** |
| Rspamd `from_owners` (3b, own ConfigMap) | 40 bytes per address | 384 bytes | 5,000 addresses: about 1.9 MB, **over 1 MiB** |

Each Dovecot file is under the 1,000,000-byte guard at 1,000 worst-case
users (750,000 and 560,000 bytes), but both are keys of **one** Secret, and
the limit is per object, so the Secret is not: the one-Secret choice buys
the atomic projection of section 3, point 5, and costs the headroom. Service
accounts (3c) are lines of the same Secret: the account name is the reserved
form of 19 characters, so a passdb line of about 160 bytes and, to be
conservative, a userdb line of about 110 bytes, about 270 bytes each. They
count toward the guard, not toward the 1,000-user cap.

So at the worst case neither the Dovecot Secret (1,310,000 bytes) nor the
Postfix tables nor `from_owners` fit into one object at the caps below, and
the count caps alone are not a safe limit: **the byte guard, not the count
caps, holds every object at its limit.** At realistic sizes the caps are the
tighter bound (1,000 users are about 0.5 MB of Dovecot data). The limits,
enforced by the `MailServer` controller in the same pass that renders:

- at most **32 aliases** per user (API);
- at most **1,000 `MailUser`s** and **5,000 addresses** (primary + alias +
  service-account senders) per `MailServer` (product limits);
- **a byte guard on every rendered ConfigMap and Secret**, in addition to the
  counts: the size of the data of each object (keys and values) must stay
  under 1,000,000 bytes, a margin below the 1 MiB limit. The reason is
  `ConfigTooLarge` and the message names the object and its size. `from_owners`
  is its own immutable object for this reason: it is the only table that
  grows with every address by 384 bytes, and it must not push the guard and
  DKIM tables of the main Rspamd ConfigMap over the limit. Splitting the
  Postfix tables into one object each (the files are `subPath` mounts, so
  the source object does not matter) is in the backlog. It is a modest gain,
  not a fix: `valias` is 638 of the 1,022 worst-case bytes per alias, so one
  object that holds only `valias` fits 1,000,000 / 638, about 1,567 aliases,
  against about 978 now (a factor of about 1.6), and even split the tables
  do not fit at the caps in the worst case: `sender_login` with 5,000 x 384
  is about 1.92 MB, and `valias` with 4,000 aliases x 638 is about 2.55 MB.
  The byte guard stays the limit, and the split is not needed at realistic
  sizes.
- Beyond a count cap or the byte guard the **new revision of Postfix and
  Rspamd is refused**: `Ready=False`, reason `UserLimitExceeded`,
  `AddressLimitExceeded` or `ConfigTooLarge`, the previous revision keeps
  running, and the message names the counts or the object. Dropping the
  tail by name would silently take arbitrary users away.

**What a blocked revision blocks, and what still applies.** A blocked
revision freezes the Postfix maps and the Rspamd data (and with them any
other change of the same pass, such as a new domain). It must not freeze the
removal of access:

- **Dovecot files: logins are never blocked.** While a limit blocks, the
  controller still updates the live Secret, with a revocation-only rule.
  In the **passdb file** every line already in it is rendered again from its
  current object (a new hash for a changed password) and **dropped** for a
  disabled user and for a user whose object is gone; **no line is added**.
  The **userdb file** is not touched: no line is added, and none is dropped,
  because the frozen Postfix maps still accept mail for those addresses.
  Mail for a user whose login was revoked is therefore still delivered to
  the retained mailbox.
- **Domain revocation** keeps the path of ADR 0003: its fail-closed render
  has no domains and no users, so no limit applies to it, and it replaces the
  running configuration (also the Dovecot files: both are rendered empty).
- **What does not apply while blocked:** new users, new aliases, and the
  removal of an *alias* by itself (the frozen `valias` and `sender_login` of
  the running revision still list it) until the blocking input is fixed.
  The sender binding of the frozen revision still ties the address to the
  same account; a removed or disabled account cannot log in to use it.
- The failure mode and the way out are in the failure-modes and runbook
  pages of the docs.

Postfix reads a `texthash` into memory per process; 5,000 lines are
negligible. The real cost of size is rollout time. 3b settles changes for a
few seconds (debounce in the controller) so that creating ten users does not
produce ten revisions of the Postfix maps.

#### 4.8 Directory users

Aliases and senders come from the directory through the **same contract** as
`MailUser` (the maps differ, the semantics do not):

| Meaning | LDAP | PostgreSQL |
|---|---|---|
| login name | `attributes.login` (default `uid`) | column `login` of the users relation |
| canonical address | `attributes.mail` (default `mail`; must be single-valued) | column `mail` |
| aliases (optional) | `attributes.aliases` (multi-valued; for example `mailAlias`) | the aliases relation: columns `alias`, `target` |
| password | auth bind (default) or the hash in `userPassword` | column `password` (`{SCHEME}hash`) |
| active filter | `activeFilter`: a static restriction | the relation itself (use a view) |

1. **API (3a).** The new `MailDirectory.spec.ldap.attributes` has the
   fields `login`, `mail` and `aliases`: attribute descriptors only, with
   the pattern `^[A-Za-z][A-Za-z0-9-]{0,63}$`.
   **`userFilter` is renamed to `activeFilter`**, because its meaning changes
   (v1alpha1, nothing is released): it is a **static restriction without
   placeholder** (default none), for example
   `(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))`. Keeping the
   name would be a trap: a stored `(mail={user})` still compiles (the
   braces are ordinary characters) and then matches no entry, a permanent
   `550` for everybody. With a new name the CRD's structural schema
   **prunes** a stored `userFilter` (an apply with strict field validation,
   the default of current `kubectl`, fails loudly with an unknown-field
   error). Pruning can drop a restriction silently where a GitOps tool does
   not validate strictly, which widens the account set to every entry with
   the attributes; with no release there are no such objects, and the
   upgrade notes say so. Every consumer to change in 3a:
   - `api/v1alpha1/maildirectory_types.go` (field, comment, `Pattern`, and
     the CEL rule that counts the `{user}` placeholder) and the generated
     `config/crd/bases/cnms.rpp.gmbh_maildirectories.yaml` (`make manifests`)
      and `api/v1alpha1/zz_generated.deepcopy.go` (`make generate`; the new
      `attributes` struct and the renamed pointer field);
   - `config/samples/cnms_v1alpha1_maildirectory.yaml` and
     `config/samples/full-setup/maildirectory.yaml` (both set `userFilter`);
      `docs/concepts/resource-model.mdx` embeds the second file and follows
      on its own;
   - `internal/controller/api_validation_test.go` (the `MailDirectory` cases
     for the placeholder, `%` and newlines);
   - `docs/reference/api.md` (generated, `make api-ref`) and the entry for
     the old placeholder in `docs/reference/upgrade-notes.md`, which is
     replaced by an entry for the rename;
   - the `{user}` placeholder is not an ADR 0003 rule (that ADR has none):
     the sources are the type comment and the upgrade notes above.

   The operator composes the lookups (`(&<activeFilter>(<mail>=<key>))`, and
   `(|(<login>=<key>)(<mail>=<key>))` for the login). The pattern rejects
   `{`, `}`, `%` and control characters; the controller compiles the filter
   (go-ldap `CompileFilter`) and reports `InvalidFilter`.
   `spec.sql.users` and `spec.sql.aliases` name the **relations**
   (`^[a-z_][a-z0-9_]{0,62}$`, optional `schema`, default `public`; defaults
   `cnms_users`, none); the **column names are fixed**. An existing schema
   is adapted with views. The operator generates all SQL itself; there is no
   free-text query in the API. That makes the injection surface zero by
   construction on the API side. The key is put into the query by the
   engine's own quoting (`%s` in Postfix, `%{user}` in Dovecot); on the
   Dovecot side a quote-injection key and an LDAP filter key found nothing
   (Verification); on the Postfix side the quoting is documented for the
   `ldap` and `pgsql` tables and **inferred** here, not run.
2. **The login is a path component.** Dovecot's home is
   `/srv/mail/%{ldap:<login>}` (or the SQL `login` column), the value of a
   directory attribute and not something Dovecot validates. The guard
   `(!(<login>=.*))` only blocks a login that starts with a dot; it does not
   block `/`, so a value such as `alice/../bob` would point into another
   account's mailbox. The operator therefore appends to every LDAP lookup
   `(!(<login>=.*))(!(<login>=*/*))(!(<login>=sa.*))` (no leading dot, no
   `/`, not in the reserved form of section 4.9), and generates the SQL with
   `WHERE login ~ '^[a-z0-9][a-z0-9._-]{0,63}$' AND login !~ '^sa\.'`
   (a close relative of the `MailUser` name pattern, not the same: it
   allows `..` inside a value and a trailing `.` or `-`, which are safe in
   a path component; what matters is no `/` and no leading dot). Two facts
   to keep in mind: a substring filter on an attribute without a SUBSTR matching rule is *Undefined*,
   and `(!(Undefined))` is Undefined, which excludes the entry, so a
   directory whose login attribute cannot do substring matching ends up with
   **no accounts** instead of with an unguarded path; and the `MailDirectory`
   probe (section 5) runs the **composed** filter and must find at least one
   entry, so this shows as a condition (`NoMatchingEntries`, a warning,
   which a directory that is still empty also gets) rather than as a silent
   `550`. A check of the login value shape in the Dovecot userdb itself
   (declaratively) is **not verified** and not planned; the guards above are
   at the source.
3. **Postfix (verified with OpenLDAP and PostgreSQL, except where marked):**
   an alias table (`mailAlias=%s` returning `mail`; for SQL a `SELECT` of
   `target` with `WHERE alias='%s'`), a mailbox table (`mail=%s`), and, in
   3b, a sender table (`(|(mail=%s)(mailAlias=%s))` returning the login;
   **the LDAP sender table was not run**, inferred from the other two). All
   three with the active restriction.
4. **Dovecot:** `passdb ldap` with `bind = yes`: the directory verifies the
   password, hashes never leave it, and the filter is
   `(|(<login>=%{user})(<mail>=%{user}))` with `fields { user = %{ldap:<login>} }`
   (canonical account name); `userdb ldap` by the same filter with the home
   `/srv/mail/%{ldap:<login>}`. For PostgreSQL: `passdb sql` with the same two
   keys, returning `login AS user`, and `userdb sql` for `home`. Verified on
   2.4.5: login by name and by address works, an alias and a disabled
   account do not authenticate, the LMTP user lookup resolves the canonical
   address and not an alias, an injection key finds nothing. The SQL
   `password` values must carry a scheme prefix; plaintext schemes are the
   directory owner's responsibility.
5. **TLS to the directory is required by default** (ADR 0003, unchanged):
   LDAP StartTLS with verification, PostgreSQL `verify-full`; `None`,
   `disable` and `require` stay explicit choices. Verified: Postfix
   (`start_tls`, `tls_require_cert = yes`, `tls_ca_cert_file`), Dovecot
   (`ldap_starttls`, `ssl_client_ca_file`) and PostgreSQL (`sslmode`,
   `sslrootcert`) verify against the supplied CA; a wrong CA ends in the
   temporary failures above.
6. **Credentials.** Never in a ConfigMap and never in a URI. The
   referenced Secrets (`bindSecretRef`, `passwordSecretRef`,
   `caSecretRef`) are read by the `MailServer` controller and copied into the
   per-server Secrets of the revision (immutable, hash-named, ADR 0009), so a
   change is a validated rollout. Details that were verified and decide the
   shape:
   - **Postfix `pgsql:` logs its connection URI, password included**, when
     a connection fails (`connect to pgsql server postgresql://user:secret@...`).
     So `hosts = <host>`, `user`, `password`, `dbname` go into the `.cf`
     file and TLS goes through libpq's `PGSSLMODE` and `PGSSLROOTCERT`
     environment variables; with the non-URI form the log names only the
     host. Postfix daemons see only variables in `import_environment`;
     the pinned default is the list below, and the renderer appends
     `PGSSLMODE PGSSLROOTCERT` to the full list:
     `MAIL_CONFIG MAIL_DEBUG MAIL_LOGTAG TZ XAUTHORITY DISPLAY LANG=C POSTLOG_SERVICE POSTLOG_HOSTNAME XDG_RUNTIME_DIR`.
     The `.cf` files (they carry `bind_pw`/`password`; Postfix has no
     include or file-reference for them) live in a **Postfix Secret**
     mounted at `/etc/postfix-secret`, mode 0440, group 5001 (like the
     Dovecot users Secret). The `pgsql:` `statement_mode` of the current Postfix manual is
     a 3.12 feature that 3.11.7 silently ignores: only the documented
     legacy quoting exists.
   - **Dovecot 2.4 reads a setting from a file** with `<path`
     (`ldap_auth_dn_password = </etc/dovecot-secret/ldap-password`, and
     `password = </...` inside `pgsql { parameters { } }`; both verified at
     runtime, a wrong password in the file fails the lookup). `doveconf -n`
     does not read the file, so the ConfigMap holds the **whole structure**
     and the validation Job (no Secret mounted, ADR 0009) still validates it.
   - **Not verified: whether the error texts of Dovecot, `pgx` and `go-ldap`
     can contain a credential** (a connection string, a bind DN with a
     password). Until a test shows it they are treated as if they could: the
     controller never copies a driver error into a condition or an Event
     verbatim, it maps it to a reason and a fixed message, and logs only the
     reason and the host.
7. **Timeouts and caching.** Postfix's `timeout` covers the search but not the
   connection; with an unreachable server a lookup blocked for 8 to 10 s in
   the tests (libldap's own network timeout; not tunable per table).
   The consequence is a slow `451`, not a loss; 3a tests `NETWORK_TIMEOUT`
   in an `ldap.conf` (not verified) and relies on the per-client limits of
   ADR 0006. Dovecot's `auth_cache_size` stays 0 (default): no stale
   credential after a password change in the directory.
8. **Trust boundary.** The directory is outside the cluster; the operator
   can neither see nor police its content. What the operator guarantees:
   the *domain* of every address is checked by Postfix against
   `virtual_mailbox_domains` (an address in a domain the server does not
   serve is never accepted or delivered, whatever the directory says), and
   sender binding applies with the directory's answer. What it cannot
   guarantee: that two directory users do not share an address (Postfix's
   alias table would expand a shared alias to both; mail goes to both
   mailboxes, and both may send as it), that `mail` is single-valued, that
   aliases are not chained. These are documented as the directory owner's
   responsibility. A **directory user** also keeps the residual of the
   header `From` (section 4.4, check 2).

#### 4.9 Service accounts (3c): naming, probe and consent

1. **A reserved login form.** `sender_login` maps an address to an account
   **name**, so a name must not belong to two accounts. A service account's
   account name and login are `sa.` plus the first 16 hexadecimal characters
   of the SHA-256 of `<namespace>/<name>` of the `MailServiceAccount`
   (written to the credential Secret as `username`). The form cannot
   collide with a `MailUser` (its name pattern allows `sa.`, which a CEL rule
   on `metadata.name` refuses) nor with a directory login (the generated LDAP
   filter and SQL `WHERE` of section 4.8 exclude it); the primary-address
   login forms of the users contain `@` and never start with `sa.`.
   A collision of two service accounts (a 64-bit prefix, negligible) is a
   conflict like any other: both lose, as in 4.2.
2. **Two user sources at once.** Service accounts are lines of the same
   live Dovecot Secret (the passdb and the userdb file), whichever user
   source the server has (a directory, or `MailUser`s). Dovecot's `passdb`
   order is the file first (small, local, no network), then the directory;
   a login that is not in the file falls through to the directory. The
   default behaviour of several `passdb`s (the next one is tried when the
   user is unknown) and the directory `userdb` after the file's are **not
   verified** here; the e2e test of 3c logs in as a service account on a
   directory server.
3. **The probe and the credential (3c).** For a service-account sender in
   a directory server, 3c probes the directory once per account and sender
   (the controller already has the connection from the `MailDirectory`
   probe); a hit marks `SenderConflict`. **A probe that fails withholds the
   credential only for a new account or for a changed `spec.sender`**
   (`DirectoryUnavailable`). An existing account keeps the credential it has
   when the probe errors: `Reachable` never takes a working account away,
   which is the rule `Reachable` already follows for the rollout (section 5).
   This makes the new-account failure a fail-closed admission decision and
   not a runtime dependency (principle 4). A race with a later directory
   change remains: the sender maps then list both owners
   (`unionmap:{texthash:..., ldap:...}`, verified with `postmap`; errors in
   either member fail the lookup; inside a daemon not run).
4. **`allowedNamespaces` is a list of objects** `{name, senderDomains}`
   (3c), not of strings, so it can grow without a breaking change.
   `senderDomains` (optional) lists the `MailDomain`s of the server's
   namespace that this namespace may send from; omitted it means every
   served domain (the behaviour of ADR 0008). The list is a `listType=map`
   on `name`. The risk it addresses: a namespace that the server consented
   to can set any `spec.sender`, and an address that nobody owns, such as
   `ceo@a.example` or `postmaster@a.example`, goes to its **only claimant**;
   a second claimant of the same address makes **both lose** it (section
   4.2, point 1), so a namespace can also deny another consented namespace
   an address. A namespace that is only meant to send as
   `app@notifications.example` must not be able to take a name in another
   domain. `senderDomains` closes both at the domain level, **provided every
   consented namespace gets its own `senderDomains`**, with no domain in two
   of them: that is the documented mitigation of the cross-tenant denial in
   4.2, point 2. What stays open is the local part inside a granted domain
   (a local-part grant is in the backlog).

#### 4.10 Not handled in Phase 3

- **`postmaster@`.** RFC 5321 expects a `postmaster` mailbox for each domain
  that accepts mail. CNMS renders none today (ADR 0014 says the renderer has
  no `postmaster` address). A `MailUser` or alias may claim the local part
  `postmaster` (it fits the pattern); nothing maps it automatically, and mail
  to it is otherwise `550 5.1.1`. An automatic per-domain mapping is in the
  backlog.
- Shared addresses, distribution lists, catch-all, forwarding to external
  addresses, per-alias send-only, plus-addressing and quotas are out of
  scope (the backlog).

### 5. `MailDirectory` rendering contract

- **Controller (3a).** Conditions: `Ready` (configuration is valid: Secrets
  exist and carry their keys, TLS mode and CA are consistent, the LDAP filter
  compiles, the SQL relation names are valid) and `Reachable` (a bind and a
  base search with the **composed** filter, which must return at least one
  entry, or `SELECT 1 FROM <relation> LIMIT 1` plus the `WHERE` of 4.8,
  through go-ldap/v3 and pgx/v5, both MIT). `Reachable` is a **warning**: it
  never feeds into any `MailServer`'s `Ready`, because the operator pod may
  have different network access than the data plane and because an outage
  must not take the rollout away (principles 4, 5, 10). The probe re-runs
  every five minutes and on spec change. Reasons: `Reachable`, `Unreachable`,
  `BindFailed`, `TLSFailed`, `InvalidFilter`, `NoMatchingEntries`,
  `SecretNotFound`, `SecretIncomplete`, `RelationMissing`.
- **Seam.** `RenderRequest` replaces `StaticUsers` with a user-source
  description: `Accounts []Account` (operator-managed: account name,
  password hash, primary address, aliases, `Disabled`, `SendOnly`) and
  `Directory *DirectorySource` (type, resolved settings, secret values).
  `insecureStaticUsers` becomes one more producer of `Accounts`, so existing
  golden files keep their content. The Postfix renderer emits `vmailbox`,
  `valias`, `sender_login`, `recipient_defer`, `null_sender` (ConfigMap) and
  the `.cf` files (Secret); the Dovecot renderer emits the `passdb` and
  `userdb` files (the live Secret of 4.7) or the directory passdb/userdb
  blocks (ConfigMap, secret values by file reference); the Rspamd renderer
  emits `from_owners`. Rendering is deterministic: sorted, stable, no
  timestamps.
- **Validation gate (ADR 0009).** `postfix check` and `postconf -n` cover
  `main.cf` and `master.cf`; they do **not** open maps (verified: a missing
  table file passes, an unknown key in a `.cf` file is silently ignored, and
  a query against an unreachable server fails at query time). The new map
  files therefore get no real check from the Job; they are covered by golden
  files, a Go parser test per table, the `MailDirectory` probe, and e2e. The
  Job runs `postmap -s` on each `texthash` file (parse check, no server) as
  a cheap addition. `doveconf -n` covers the Dovecot structure.
  The Job mounts no Secret, so the `.cf` files are not seen by it. A
  `master.cf` argument error is not caught either (section 4.4); the e2e
  suite starts the listener.
- **Startup.** With PostgreSQL unreachable at start, Dovecot's `auth`
  service is killed by the master after 30 s and restarted until the
  database answers; it recovers without a pod restart (verified). While
  down, SASL answers `454 4.7.0` and LMTP `451 4.3.0`, both temporary. LDAP
  down at start was **not tested**.

### 6. Where the data lives (summary)

| Data | Object | Why |
|---|---|---|
| `vmailbox`, `valias`, `sender_login`, `recipient_defer`, `null_sender` | Postfix ConfigMap, immutable, hash-named | addresses are not secret; one revision path (ADR 0009) |
| directory `.cf` files, directory CA | Postfix Secret, immutable, hash-named, mode 0440 | `bind_pw` and `password` are inside the file |
| `passdb` file (hashes) and `userdb` file (delivery) of accounts and service accounts | **Dovecot users Secret `<server>-dovecot-users`, two keys, stable name, mutable, outside the content hash** | live changes without a restart; one projection for both files (4.7; the exception to ADR 0009) |
| directory settings, secret values by `<file` | Dovecot ConfigMap + Secret, immutable | structure stays validatable |
| `from_owners` | own Rspamd ConfigMap, immutable | addresses only; it is the table that grows (4.7) |
| password hash per user | `<name>-credential` Secret, owned by the `MailUser` | stable across reconciles |

The `MailServer` controller reads the credential Secrets with **one**
label-selected, uncached list (ADR 0009 allows list without watch), not one
`get` per user.

### 7. Controllers, watches, RBAC

Follow the repository patterns: indexes registered **once** in
`cmd/main.go` through `internal/controller/indexes.go` (a manager rejects a
second registration; the envtest helper registers them the same way); watch map
functions use the indexes; `Reconcile` filters the lists it already loaded,
because an uncached `List` rejects field selectors (ADR 0014).

- New indexes: `MailUser` by `spec.serverRef.name`, `MailUser` by
  `spec.domainRef.name` and by each `spec.aliases[].domainRef.name` (one
  index function returning all of them), `MailUser` by each claimed address
  (4.2, point 5), and, for 3a, `MailServer` by `spec.directoryRef.name`;
  `MailDomain` events reach the `MailUser`s of their name (a revocation
  updates their status), and `MailUser`/`MailDirectory` events reach their
  `MailServer`. The `MailUser` watch carries **no** generation predicate
  (the `MailDirectory` watch has none today either): a status write, such as
  a new `status.credentialRevision` (section 3, point 4), must reach the
  server. Status writes happen only when the value changed (ADR 0007).
- `MailServer` reconcile gathers: authorized domains (existing), the
  `MailUser`s of the server, the credential Secrets (one list), the directory.
- 3c adds the cross-namespace map functions of ADR 0008 (server to accounts
  in other namespaces, `allowedNamespaces`).
- **RBAC** (verbs on resources; subresources and finalizers are separate
  resources): `mailusers` get, list, watch; `mailusers/status` patch;
  `maildirectories` get, list, watch and `maildirectories/status` patch (the
  scaffold removed the write access until the controller exists, backlog);
  `secrets` **`update`** is added to the existing `get`, `list`, `create`,
  `delete`, still without `watch` (4.7 says what that grows: the `ClusterRole`
  can update any Secret). The `mailusers/finalizers` rule is not needed: a
  `MailUser` has no finalizer, its credential Secret is garbage-collected
  through the owner reference. Tenant roles: a `mailuser-editor` and
  `mailuser-viewer`, and the generated admin role. Creating a `MailUser`
  lets its author create mailboxes and passwords, but only for domains that
  are authorized for the namespace.

### 8. Conditions

`MailUser`:

| Condition | Reasons |
|---|---|
| `Ready` | `Ready`, or the reason of the first failing condition below; plus `ServerNotFound`, `DirectoryConfigured`, `UserSourceConflict`, `DomainNotFound`, `DomainNotServed`, `DomainNotAuthorized`, `Disabled` (not a failure: `Ready=True`, reason `Disabled`) |
| `CredentialReady` | `Ready`, `SecretNotFound` (the existing reason, for the Secret or its key), `PasswordInvalid`, `HashFailed` |
| `AddressesServed` (warning, never feeds `Ready`) | `AllServed`, `PartiallyServed` (the message names the addresses and their reasons, conflicts included) |

The reasons of one **address** are in `status.addresses[]`, not on the
condition: `Served`, `AliasDomainNotFound`, `AliasDomainNotServed`,
`AliasDomainNotAuthorized`, `AddressConflict`.

`MailServer` additions: reasons `UserSourceConflict`, `UserLimitExceeded`,
`AddressLimitExceeded`, `ConfigTooLarge`, `DirectoryInvalid` (the
`MailDirectory` has `Ready=False`; the previous revision keeps running);
warning `AddressesServed`. `DirectoryNotFound` and `NoUserSource` stay
(`NoUserSource` with the meaning of section 3, point 7).
`MailDirectory`: `Ready`, `Reachable` (section 5). Each reason is added to
`docs/reference/status-conditions.md` with its PR.

`MailServiceAccount` (3c): its conditions are not tabled here. The two
reasons this ADR introduces, `SenderConflict` and `DirectoryUnavailable`,
are described in section 4.9, point 3, and the table of the account's
conditions is written with the CRD in 3c.

### 9. Test fixtures, images and licenses

Not added to `internal/images` by this ADR (3a does that).

| Fixture | Image | Licence | Notes (measured here) |
|---|---|---|---|
| LDAP | `osixia/openldap:2.6.15-rc`, pinned by digest (below) | image tooling MIT ([licence](https://github.com/osixia/container-openldap/blob/main/LICENSE.md)); OpenLDAP **OLDAP-2.8** ([licence](https://www.openldap.org/software/release/license.html); read from the package metadata in the image) | 51 MB, Alpine 3.24, runs as user `ldap`; first authenticated StartTLS search about 2 s after `docker run`; seeds LDIF from a mounted directory; `OPENLDAP_BOOTSTRAP_TLS=true` takes `tls.crt`, `tls.key`, `ca.crt`; ships the Postfix Book schema (`mailAlias`, `mailEnabled`) |
| PostgreSQL | `postgres:18-alpine` (digest below; the e2e pins the exact version tag, to be confirmed in 3a) | **PostgreSQL License** ([licence](https://www.postgresql.org/about/licence/); package metadata) | 433 MB on disk; ready with schema about 2.4 s; `/docker-entrypoint-initdb.d` seeds SQL; TLS needs the key owned by `postgres` mode 0600 |

**The LDAP image is a pre-release tag, deliberately.** On Docker Hub the
2.6.x line of `osixia/openldap` has only `-alpha`, `-beta` and `-rc` tags
(`2.6.15-rc` is the newest; the [tag
list](https://hub.docker.com/r/osixia/openldap/tags) and the [same list as
JSON](https://hub.docker.com/v2/repositories/osixia/openldap/tags), read on
2026-11-06, show `2.6.15-rc`, `-beta`, `-alpha` and `2.6.10-alpha`);
`latest` and `stable` were last pushed in 2021, with `1.5.0`, an old line
that was not examined. The repository (`osixia/container-openldap`,
MIT, default branch `main`) was rewritten in 2026 and is active (pushed
within the week of this ADR). A pre-release is acceptable for a test
fixture, not for the data plane. The e2e pins it **by digest**. **Renovate
follows only the `-rc` tags**: it moves the pin to a later `-rc` tag and
to nothing else, so when a stable tag appears, or the tag line ends,
someone has to move the pin by hand. The fallback, if this image
disappears or breaks, is a small CNMS image from the Debian `slapd` package
(OLDAP-2.8), built like `images/e2e-client`; it is recorded here and not
built.

Go dependencies (3a/3b, licence-checked when added, `make license-check`):
`github.com/go-ldap/ldap/v3` (MIT, [licence](https://github.com/go-ldap/ldap/blob/master/LICENSE);
used for the probe and the filter compiler), `github.com/jackc/pgx/v5` (MIT,
[licence](https://github.com/jackc/pgx/blob/master/LICENSE)),
`golang.org/x/crypto` (BSD-3-Clause, [licence](https://go.googlesource.com/crypto/+/refs/heads/master/LICENSE);
becomes direct). Postfix image: libpq (`libpq-dev` at build, `libpq5` at
runtime, PostgreSQL License) for the `pgsql` map type.

**The Postfix image changes content, so its tag must change.** The CNMS
images carry `POSTFIX_VERSION` as their tag (ADR 0004, item 6;
`internal/images/images.go` uses `cnms-postfix:3.11.7`, by tag only, no
digest). Adding libpq would overwrite what `cnms-postfix:3.11.7` means: a node
that already pulled it keeps the image without `pgsql` (the default pull
policy does not re-pull a present tag) and a `MailServer` fails at the first
`pgsql:` table. 3a therefore adds a **build revision** to the tag:
`3.11.7-1`.

**The e2e client gets the same suffix.** The `images` workflow reads the
version from `images/postfix/Dockerfile` **for both images**
(`.github/workflows/image-build.yml`, the step "Read the Postfix version",
and the `type=raw` tag; `.github/workflows/images.yml` sets `postfix-tag`
for `cnms-postfix` and for `cnms-e2e-client`), so an `ARG` in
`images/e2e-client` would never be read. If the workflow appends the
revision, `cnms-e2e-client` is published as `3.11.7-1` as well, and the
constant and the Makefile variable that name it must follow. That is the
simplest option and it is the decision: one revision, one place to read it;
the e2e client image is rebuilt with no change of content when the revision
moves. **All places, changed together in the first commit of 3a:**

- `internal/images/images.go`: `Postfix` (line 57) and `E2EClient`
  (line 60), both `...:3.11.7-1`;
- `Makefile`: `POSTFIX_IMAGE` (line 147) and `E2E_CLIENT_IMAGE` (line 148);
- `images/postfix/Dockerfile`: the build-example comment (line 9) and a new
  `ARG IMAGE_REVISION=1` next to `ARG POSTFIX_VERSION`;
- `.github/workflows/image-build.yml`: the step that reads the version
  (lines 37 to 39) also reads `IMAGE_REVISION`, and the `type=raw` tag value
  (line 64) becomes `<version>-<revision>`.

The Renovate custom manager in `renovate.json` has the regex
`cnms-(?:postfix|e2e-client):(?<currentValue>\d+\.\d+\.\d+)`. It is not
anchored, so it already matches `3.11.7` inside `3.11.7-1` and replaces
only that part; adding `(?:-\d+)?` after the group **may** be done for
clarity and is not required. A Postfix bump leaves the suffix in place and
the revision is reset to 1 by hand in the same PR, like the checksum of the
tarball. The digest pin of the published images stays in the backlog
(Phase 10).

### 10. Checklist per pull request

Each item is a place the change reaches that is easy to forget.

**3a** `MailDirectory`
- Scaffold the controller with the CLI, not by hand:
  `kubebuilder create api --group cnms --version v1alpha1 --kind MailDirectory --controller --resource=false`,
  then add the RBAC of section 7.
- The `userFilter` rename and every consumer of section 4.8, point 1.
- The e2e `MailServer` (`test/e2e/mailobjects.go`) sets `directoryRef` (to
  `sql-e2e`) **and** `submission.insecureStaticUsers`, which becomes
  `UserSourceConflict`; it moves to the directory only (the users come from
  the PostgreSQL fixture). The e2e tests that log in as the static user
  (`e2eUser`) change to a fixture user, and the test "should not replace a
  working data plane with a broken spec" (`test/e2e/e2e_test.go`, expects
  `DirectoryNotFound` after patching `directoryRef` to a missing name) is
  reworked, because the server it patches must have a real directory first.
- `IndexFields` (`cmd/main.go`, and the same registration in the envtest
  helper `watches_test.go`): the new `MailServer` by `directoryRef` index,
  once.
- Samples validate in envtest (`config/samples`), and the files of
  `config/samples/full-setup/` that describe the draft are marked **DRAFT**.
- Docs: how-to for directories, upgrade notes, `status-conditions.md`,
  `docs/reference/testing.md` (guard matrix with check 1).
- Licenses: `make license-check` and `make license-report`; NOTICE, ADR 0001
  and `docs/reference/data-plane-licenses.md` list libpq, and the test images
  where they are listed; go-ldap and pgx in the Go report.
- The Postfix image revision of section 9.

**3b** `MailUser` and sender binding
- `kubebuilder create api ... --kind MailUser` (controller and resource),
  which also generates the admin, editor and viewer role files; list them
  in the kustomization.
- Register the `MailUser` indexes in `IndexFields` once (`cmd/main.go`) and
  in the envtest helper.
- **The DKIM e2e** (`test/e2e/dkimutil.go`, `submitFromDomain`) sends as
  `e2eUser@<domain>` for several domains; strict sender binding refuses
  every address the static user does not own. The suite moves to `MailUser`s
  with aliases in those domains (which also exercises aliases); static users
  stay for the tests that do not send as a specific domain.
- Who writes `status.addresses[]`: the `MailUser` controller, with the shared
  decision function (section 3, point 6).
- The `update` verb on `secrets` (`kubebuilder:rbac` markers and
  `config/rbac/role.yaml` by `make manifests`).
- The `MailUser` CEL rules of section 4.1 (the uniqueness rule on the
  `aliases` field, the primary-equality rule at the root with `has()`):
  an envtest that **admits a `MailUser` with no `aliases`**, one with
  aliases, and refuses a duplicate alias and an alias equal to the primary
  address.
- The credential Secret: the rehash rule, create-once and the
  `resourceVersion` precondition on every update (section 3, point 4), and
  `status.credentialRevision` written on a hash change; the `MailUser` watch
  of the `MailServer` without a generation predicate (section 7). The
  envtest shows that a changed provided password reaches the server's render
  without waiting for the safety requeue.
- The load test of section 3 point 4, the `doveconf -n` check of the
  auth-worker setting (and the fallback of section 3, point 4, if it fails),
  and the e2e of the two live Dovecot files (kubelet projection): add a user
  and log in, change a password, delete, and a disabled user who **cannot log
  in by IMAP and by SASL on 587 and still receives mail** over LMTP.
- `soft_bounce = yes` on the `lmtp` service in `master.cf`
  (`internal/render/postfix/mastercf.go`, golden files), and its check on
  Postfix 3.11.7: a permanent reply of the LMTP side must be a deferral
  (section 4.7, cost 2).
- Check 2 is the last commit of the PR.
- Docs: upgrade notes, `status-conditions.md`, a failure-modes entry for
  the limits (section 4.7), the revocation latency, and in the runbook the
  manual end of open IMAP sessions after a revocation (`doveadm kick` in the
  Dovecot pod, or a restart of its StatefulSet; section 4.7, cost 3).
- Licenses: `make license-check` with `golang.org/x/crypto` direct.

**3c** `MailServiceAccount`
- `kubebuilder create api ... --kind MailServiceAccount` and its roles.
- `MailServer.spec.serviceAccounts.allowedNamespaces` as a list of objects
  (section 4.9), a CRD change on `MailServer` (CEL, generated reference).
- The cross-namespace map functions and indexes, in `IndexFields` once.
- The sender conflict with a directory (probe, section 4.9) and the
  withholding rule.
- The credential Secret (`username`, `password`, `server`, `tls`, `helo`,
  `tlsServerName`, `ca.crt`) and the NetworkPolicy in the application's
  namespace; e2e in a second namespace.

### 11. Amendments to earlier ADRs

The text of the earlier decisions stays. ADR 0008 and ADR 0004 (item 6)
carry their pointers now, inline, as the amendments of ADR 0014 do; the
pointers in the other ADRs are added by the pull request that implements
the change (3a, 3b), so an ADR never points at behaviour that does not
exist.

- **ADR 0004, item 6:** the CNMS image tags are
  `<POSTFIX_VERSION>-<revision>` (3a, section 9), for the Postfix and the
  e2e client image; an inline amendment note in ADR 0004 says so.
- **ADR 0008:** the `MailUser` name is a DNS-1123 subdomain; `MailUser` has
  `domainRef` and `aliases`; the three-PR split; sender binding is strict
  and covers every user source; header `From` binding for authenticated
  mail moves from the backlog into 3a (check 1) and 3b (check 2);
  `allowedNamespaces` holds objects; the one-source rule has a reason for
  its conflict and counts service accounts.
- **ADR 0003:** the address ownership and conflict rules (section 4.2) and
  the primary-domain rule (4.6) extend the revocation path to user data.
- **ADR 0009:** the Dovecot passdb and userdb files are two keys of a stable,
  mutable Secret outside the content hash (4.7), with the new `update` verb
  on Secrets; the
  credential Secret is updated in place; map files are not validated by the
  Job beyond `postmap -s`; limits and the refusal of an over-limit revision.
- **ADR 0013:** the known gap of section 3 is closed by check 1 in 3a for
  every user source and by check 2 in 3b for operator-managed accounts, with
  the directory residual.
- **ADR 0014:** `MailUser` uses `serverRef` and checks that `domainRef`
  resolves to a domain of the same server.

## Consequences

- **Easier:** a person gets several addresses across the domains of one
  server with one object; revocation, conflicts and limits fall out of the
  rules the project already uses; a directory works with the same semantics;
  a user, an alias or a password changes without a Dovecot restart; nothing
  in the API takes free-text SQL or credentials in URIs.
- **Harder:** the Dovecot Secret with the two user files is the one live,
  mutable object of the data plane, so the operator gets `update` on Secrets
  and the revocation of a login takes the propagation time (a minute or two,
  more for a changed provided password), with open IMAP sessions surviving it
  until someone ends them by hand (4.7); Dovecot and Postfix can disagree for
  a while, which `soft_bounce` on the `lmtp` service turns into deferrals
  (4.7, unverified); Postfix map changes are still a validated rollout;
  the map files get no real validation from the Job; check 2 needs Rspamd
  work in 3b and leaves a documented residual for directories; the Postfix image gains libpq, a
  rebuild and a build revision in its tag (section 9).
- **Limits:** 32 aliases per user, 1,000 users and 5,000 addresses per
  server, and a byte guard on every rendered object (4.7); no shared
  addresses, distribution lists, catch-all, forwarding to external
  addresses, per-alias send-only, plus-addressing, quotas (Phase 4),
  MySQL/MariaDB, automatic `postmaster@` (all in `docs/backlog.md`).
- **How we know it worked:** the e2e of section 1, the throughput and memory
  test of the password hash, the `NETWORK_TIMEOUT` test, the guard matrix
  with the new rules, and golden files for every table.

## Verification

Run on 2026-11-06 against the images pinned in `internal/images/images.go`
(Dovecot `dovecot/dovecot:2.4.5@sha256:43c14def08a57f25107593cbcf64a0a6fbae84a7b9de7aa038513c9b21e91914`,
started read-only like the manifests; `images.go` is authoritative if it
moves on) and the Postfix image built from `images/postfix` (3.11.7).
Throw-away containers on a private Docker network; mail was sent only
between those containers (bash `/dev/tcp` dialogue, no real mail). The
harness (container commands, configuration files) was **not committed**; the
column "How" lists the commands and configuration each check consists of,
written down from its result, so anyone can repeat it.

| # | Question | How | Result |
|---|---|---|---|
| 1 | Postfix map types in the image | `postconf -m` in the stock image; a copy of the Dockerfile with the additions below; the build log | The stock build lists `ldap`, no `pgsql`. Its flags (`images/postfix/Dockerfile`, the `make makefiles` line) are `-DHAS_LMDB -DHAS_SQLITE -DHAS_LDAP`, with `-DNO_DB`. A copy with `libpq-dev` (builder), `libpq5` (runtime), `-DHAS_PGSQL -I/usr/include/postgresql` and `AUXLIBS_PGSQL=-lpq` builds and lists `pgsql` (image 202 MB to 207 MB). The build also shows `-DNO_EAI`, `-DDEF_SMTPUTF8_ENABLE="no"` (ASCII addresses only). |
| 2 | Alias in a second `virtual_mailbox_domain` | `RCPT TO` dialogue on port 25 with `valias` and `vmailbox` as `texthash` | `alice@b.example`, `sales@b.example` accepted; an unknown address `550 5.1.1`; a foreign domain `554 5.7.1 Relay access denied`. |
| 3 | Delivery of an alias | SMTP dialogue to Postfix with LMTP to a Dovecot container; the Postfix log and `/srv/mail` | LMTP received `alice@a.example` (`orig_to=<sales@b.example>`); two aliases of one user produced one saved message; the file landed under `/srv/mail/alice`. |
| 4 | Sender binding | SMTP dialogue on 587 after STARTTLS and `AUTH PLAIN`, with `smtpd_sender_login_maps`, `reject_sender_login_mismatch`, `check_sender_access` | See 4.4: accepted `alice@a/b.example`, `sales@b.example`, upper case; refused `bob@b.example`, `nobody@a.example`, `alice@c.example`; null sender accepted without `check_sender_access`, refused with it; unauthenticated use of 587 refused by `smtpd_client_restrictions`. |
| 5 | Login forms | A passwd-file with `user=alice` as extra field; `doveadm auth test` and an authenticated Postfix session | Logins `alice` and `alice@a.example` (any case) succeed and report `user=alice`; Postfix then uses `alice` as the SASL name. An alias as login fails. |
| 6 | Lookup failures (3.11.7) | `RCPT TO` and a 587 session with the directory stopped, a wrong bind password, a wrong CA | LDAP down, wrong bind password, wrong CA (also through `proxy:`) and PostgreSQL wrong CA: `451 4.3.0 Temporary lookup failure` at `RCPT`; PostgreSQL down, sender-login lookup on 587: `451 4.3.0`; blocked 8 to 10 s per lookup when the LDAP server is unreachable. **Inferred, not run:** the LDAP sender-login lookup error (`451`). |
| 7 | `virtual_alias_domains` default | `postconf -d virtual_alias_domains`; the Postfix log with a failing alias table | `$virtual_alias_maps`; the log shows `virtual_alias_domains: ... table lookup problem` when the alias table fails. |
| 8 | `pgsql:` URI | A failing `pgsql:` lookup with the URI form and with `hosts`/`user`/`password`/`dbname`; `PGSSLMODE`, `PGSSLROOTCERT` in the container environment and in `import_environment` | A failed connection logs the URI with the password; the non-URI form logs the host only. That the `import_environment` extension is necessary was not tested. A wrong CA through the environment fails closed in the daemon (`451`). |
| 9 | Dovecot drivers | `dovecot --build-options` | passdb `ldap pam passwd passwd-file sql`, userdb `ldap passwd prefetch passwd-file sql`, SQL drivers `mysql postgresql sqlite`. |
| 10 | Dovecot against LDAP and PostgreSQL | `doveadm auth test`, `doveadm user` against the OpenLDAP and PostgreSQL fixtures with the settings of 4.8, point 4; keys `*)(uid=*` and `x' OR '1'='1` | LDAP: `bind = yes`, StartTLS with CA, read-only bind; PostgreSQL `sslmode = verify-full` with `sslrootcert`. The injection keys authenticate nobody. **Postfix side:** not run (inferred from the documented quoting of the tables). |
| 11 | Password scheme | `doveadm pw -l`; `doveadm pw -s ARGON2ID`; `doveadm pw -t` | `ARGON2ID` is listed; default `m=65536,t=3,p=1`; `doveadm pw -t` verifies a PHC hash with `{ARGON2ID}`; `passdb_use_worker` is `no` by default. The cost of a verification was **not measured** (a timing of `doveadm pw -t` included the container start; there is no figure for `m=19456`). |
| 12 | Reload behaviour | Editing, then atomically replacing, the passwd-file with a login test between; `postfix reload` for `texthash` | Dovecot picks up an in-place change and an atomic replace of the passwd-file without reload. Postfix `texthash` needs `postfix reload` (a file edit was not seen before it). |
| 13 | Secrets by file | `ldap_auth_dn_password = </path`, `password = </path` (pgsql); `doveconf -n` with the file missing | They work at runtime; `doveconf -n` hides and does not read them (exit 0 with the file missing); `!include_try` of a missing file exits 0. |
| 14 | What `postfix check` sees | `postfix check`, `postconf -n` and `postmap -q` with a missing map file, an unknown `.cf` key and an unreachable server | A missing map file passes; an unknown key in a `.cf` file is ignored; `postmap -q` against an unreachable server fails at query time. |
| 15 | `master.cf` `-o` with spaces | `postfix start` with and without the braces form; `postfix check` | `fatal: unexpected command-line argument` at service start; the braces form works; `postfix check` does not notice. |
| 16 | Outages in Dovecot | PostgreSQL or LDAP stopped while Dovecot runs and at its start; `doveadm auth test`, SASL through Postfix, LMTP `RCPT` | Runtime: `doveadm auth test` internal failure, SASL via Postfix `454 4.7.0`, LMTP `RCPT` `451 4.3.0` (PostgreSQL case captured; the LDAP LMTP reply was not captured); at start with PostgreSQL down the `auth` service is killed after 30 s and restarts; both recover without a restart. LDAP down at start: not tested. |
| 17 | `unionmap` | `postmap -q` with `unionmap:{texthash:..., ldap:...}` and one member failing | Returns the union; an error in either member fails the lookup. |
| 18 | Test images | `docker image inspect`, `apk info -a` in the images; upstream pages | `osixia/openldap:2.6.15-rc@sha256:caf6a875d6cab9543e415ac5623f1cefd20795b220a3aa896438be7eaaab02e0`: 51 MB, OpenLDAP 2.6.15, Alpine 3.24.3, licences from `apk info` (`OLDAP-2.8`) and upstream; `postgres:18-alpine@sha256:123b17998ccaf47e53b502e8c6a1e79afd8f579c6f3eec83c563da026f957395`: PostgreSQL 18.6, licence `PostgreSQL` from `apk info`. Upstream licences: osixia MIT, go-ldap MIT, pgx MIT, x/crypto BSD-3-Clause (the URLs in section 9; the osixia licence and the tags on Docker Hub were re-read when this ADR was finished). |

Row 19 was added when check 1 landed and was run on 2026-11-06 against the
Rspamd image pinned in `internal/images/images.go`.

| # | Question | How | Result |
|---|---|---|---|
| 19 | Rspamd check 1 (4.4) | `make test-rspamd-guard`: rendered configuration, `rspamc --user alice` with the header `From` variants below; a throw-away guard that printed what `task:get_from('mime')` returns | `From: Bob <bob@>` and `From: bob` both give one address with the **empty string** as `domain` (not `nil`), and no `From` header gives `nil`: the empty-domain case is real. Authenticated mail is rejected (`554 5.7.1`) with a From in an unserved domain, in a subdomain of a served domain, without a domain, with an empty domain, without a From header and with several From addresses none of them served. A served domain with a key is signed, one without a key is still deferred, unauthenticated mail stays untouched (loopback mail with a sender stays untouched for the From check only; the guard's other rules apply to it). The throw-away probe guard used for this row is not committed, so the row is only partly repeatable: the matrix cases are, the probe output is not. |

**Read from upstream documentation, not run** (the 2.4.5 pages exist on
`doc.dovecot.org` and were read on 2026-11-06; the full addresses are in
sections 3 and 4.7): the `service_process_limit` setting and the
`auth-worker` default of 30 (services page, settings summary and the 2.3 to
2.4 upgrade page, all under `https://doc.dovecot.org/2.4.5/`); the
replacement of `auth_worker_max_count` (upgrade page); the `nologin` passdb
extra field and `passdb_use_worker` (passdb page,
`https://doc.dovecot.org/2.4.5/core/config/auth/passdb.html`); the OWASP
Argon2id profiles (Password Storage Cheat Sheet, section 3, point 4).

**Not verified:** the Rspamd header-`From` check 2 (check 1 is row 19); the
throughput and memory of Argon2id with `passdb_use_worker = yes` and a
worker limit, and that the passwd-file passdb uses workers at all; the
spelling of the `auth-worker` process limit inside the 2.4.5 service block
(the `doveconf -n` gate of 3b decides); two separate passwd-files for passdb
and userdb, a passdb file with empty uid, gid and home and a userdb file
with an empty password (rows 5 and 12 used one file), and the `nologin`
alternative on 2.4.5, including whether it stops SASL; the kubelet's symlink
swap for the live Dovecot Secret (a file replace stood in); `soft_bounce` as
a per-service override on the `lmtp` client of Postfix 3.11.7; the use of
`doveadm kick` in the Dovecot pod; the `NETWORK_TIMEOUT` setting in
`ldap.conf`; that the API server accepts the two CEL rules of 4.1 and their
cost estimate; the `postmap -s` check of the validation Job (the parse of
each `texthash` file); whether the API server counts only the values of
`data` toward the 1 MiB limit (the guard counts keys as well, the
conservative reading); the LMTP reply with LDAP down and Dovecot's start
with LDAP down; `unionmap` errors inside a daemon (only `postmap` was run);
the `recipient_defer` table (`450 4.2.1`); a hash made by the Go Argon2id
library read by Dovecot; the LDAP guards `(!(<login>=*/*))` and
`(!(<login>=sa.*))` against an attribute with and without a SUBSTR rule, and a multi-valued `mail`
attribute; a `+` in a local part; the SMTP behaviour of Dovecot's
`auth_username_chars` for LMTP recipients; several `passdb`s with a file
first and a directory second; whether error texts of Dovecot, `pgx` or
`go-ldap` can contain credentials; the `Sender:` header (not bound, not
looked at); the exact PostgreSQL tag for the e2e; the Postfix `-DHAS_PGSQL`
build on the published `ghcr.io` image (only a local build).
