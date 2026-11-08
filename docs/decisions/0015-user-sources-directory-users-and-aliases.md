# ADR 0015: User sources, directory users and addresses

- **Status:** Accepted; 3a is implemented (the five commits of section 1: the
  Postfix image, the `activeFilter` rename, the `MailDirectory` controller,
  the render seam and the directory maps, `UserSourceConflict` with the e2e
  fixtures); the Rspamd header `From` check 1 is delivered ahead of 3a in its
  own pull request (#40); 3b is under way (the `MailUser` API, the controller,
  the address decision and the sender binding are there, and the `MailServer`
  serves the `MailUser`s with the live Dovecot Secret of section 4.7; the
  limits of section 4.7 are enforced; the Rspamd check 2 (header `From`) is
  done; the load test of section 3, point 4 (cgroup `memory.peak`, delivery
  during the flood) is not);
  3c has the API (the `MailServiceAccount` resource and `allowedNamespaces`)
  and the controller (consent, sender decision, directory check, credential
  Secret and egress policy); the `MailServer` does not serve the accounts yet
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
| **3a** `MailDirectory` | LDAP and PostgreSQL maps for Postfix (recipient validation, alias to canonical address; `451` on lookup errors) and Dovecot (auth and user lookup against the directory); the `MailDirectory` controller (configuration validity, reachability); the Postfix image with the `pgsql` map type; the `userFilter` rename (section 4.8); e2e with an OpenLDAP and a PostgreSQL fixture | e2e: valid, alias and invalid recipients; tempfail (not reject) with the directory down; a directory user logs in by login name and by address, not by alias; IMAP reads a delivered message |
| **3b** `MailUser` and sender binding | `MailUser` with aliases across domains; credential Secrets and password hashing; the live Dovecot passdb and userdb files (section 4.7); the accounts rendered into Dovecot and Postfix; address uniqueness; **strict sender binding for every user source** (envelope in Postfix); `soft_bounce` on the `lmtp` service (4.7); the Rspamd check 2 as the last, separable commit | e2e: a `MailUser` with aliases in two domains logs in, receives on all addresses into one mailbox, sends as each, is refused for another user's address, for a foreign domain and for the null sender; a disabled user cannot log in (IMAP and SASL on 587) and still receives mail; a user added, changed and removed with no Dovecot restart; revoking the alias domain stops its addresses in the same reconcile |
| **3c** `MailServiceAccount` | The CRD, `allowedNamespaces` (a list of objects, section 4.9), the credential Secret and the NetworkPolicy in the application namespace, sender conflicts across namespaces | e2e: an application in a second namespace sends with the operator-provided Secret, only as `spec.sender`; revoking the namespace removes the credential |

The Rspamd header `From` check 1 (section 4.4) is not part of 3a: it is
delivered ahead of it, as its own pull request (#40, branch
`feat/guard-from-domain`), with the changed guard matrix. 3a starts from the
guard it leaves.

3a is useful alone: a deployment with a directory can go live. Its gap is
the missing envelope sender binding for directory users until 3b; 3a says so
in the docs. Each PR carries its docs, golden files and envtest coverage, and
the checklist of section 10.

**Commit order of 3a.** Five commits. Every commit builds and passes
`make test` on its own; the order is what makes that true:

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
     it (verified); a hash made by the Go code is accepted by Dovecot (row
     39), and the memory under load was sampled once (row 40).
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
     service-specific process limit"). **Run on the pinned 2.4.5 (row 33):**
     `service auth-worker { process_limit = 4 }` is accepted by
     `doveconf -n`, which rejects an unknown setting. **Fallback**, if the gate rejects the name or
     the load test below fails: first lower the limit (2 workers), then lower
     `m` (for example to the OWASP profile `m=12288,t=3,p=1`, 12 MiB, listed
     on the cheat sheet above), and keep `p=1`; if no worker limit exists
     at all, set `passdb_use_worker = no` so that hashing is serialized in
     the auth process (one verification at a time, 19 MiB), and accept that a
     login flood queues behind it. The upstream [passdb
     page](https://doc.dovecot.org/2.4.5/core/config/auth/passdb.html) says
     the setting is "only used by some of the passdb drivers"; that the
     passwd-file passdb honours `passdb_use_worker` **was run (row 33)**: an
     auth worker process appears only with the setting. (Had it not, the
     hashing would run one at a time in the auth process, memory would be
     19 MiB and the limit moot, but a login flood would queue behind it.)
     3b adds a **load test** as a done
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
   other. **Verified on 2.4.5 (row 32):** two separate passwd-files, a passdb file
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
   previous revision keeps running. A `MailUser` whose server has
   `submission.insecureStaticUsers` reports `UserSourceConflict`; with a
   directory (alone or together with static users) it reports
   `DirectoryConfigured`. The same conflict arises for
   `submission.insecureStaticUsers` together with a directory (3a: this
   makes the server of the e2e suite, which sets both, a conflict; see
   section 10).
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
   the claimants and not every user of the server. The decision function
   (`internal/addresses`) keys a conflict by the folded DNS name of the
   domain instead: only a claim in a domain that is served and authorized
   takes part in the ranking, ADR 0003 allows one authorized object per DNS
   name in a namespace, and a static test user has a DNS name and no
   `MailDomain`. For valid input both keys select the same claimants.
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
  for `pgsql:`; for `ldap:` verified in row 34).
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

1. **Check 1, delivered ahead of 3a (PR #40): every header `From`
   address has a domain, and that domain is a served domain** (data-free,
   every user source, and the only check that needs no data about
   accounts). A missing `From` header, a
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
   for operator-managed accounts only. **Implemented.** The map `from_owners`
   is the data ConfigMap `<server>-rspamd-d-<hash8>`, mounted as a directory at
   `/etc/rspamd/cnms` in the Rspamd pod and in its validation Job; it is part of
   the config hash, so a new account is a validated rolling update of Rspamd
   (`maxUnavailable: 0`). Its lines are the rows of `smtpd_sender_login_maps`
   (the served addresses of the accounts that may send), so the owner of a
   header `From` is the owner of the same envelope sender. The guard reads a
   rendered **mode**, `owners_mode` `"on"` for every operator-managed server
   (also one without a single account: the table is then empty and every
   address is refused) and `"off"` for a server with a directory (no table, no
   data ConfigMap); the mode is not inferred from an empty table. The login it
   compares with is `task:get_user()` in lower case (row 45). A table that is
   missing, unreadable or has a malformed line makes the check fail with an
   error, and the error path of ADR 0013 defers the mail of logins (`451`), as
   for a key that cannot be read. The reply is `554 5.7.1 From address is not
   owned by the login`, without an address. For a directory the owned set is not
   in the cluster; the **residual risk** is that a directory user can put any
   *served* address in the header `From` and have it signed. Options for
   later: a directory lookup from Rspamd (`ldap`/SQL module), or a policy
   service (backlog). The residual risk is documented in the docs; check 1
   still applies.

Both checks use the guard's existing view (`task:get_user()`,
`task:get_from('mime')`, the served-domain table). Check 1 is implemented
and verified (row 19 of Verification; the reply is `554 5.7.1`, message
`From must be an address in a served domain`); check 2 is verified by the
matrix (row 46) and the e2e suite (row 45). Check 1 also refuses
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
one passwd-file; the two-file form is verified in row 32 (section 3, point
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
  load; row 41 measured 21 s to 1 min 28 s. Dovecot re-reads a changed
  passwd-file by itself (verified for an in-place edit and an atomic replace
  of one file, and through the kubelet's symlink swap in the e2e of row 41:
  add a user and log in, change a password, disable, delete).
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
  until the queue lifetime ends. **Verified on Postfix 3.11.7 (row 31):** that
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
- **Implementation of the live Secret (3b).** The `MailServer` controller
  writes `<server>-dovecot-users` with the `resourceVersion` it read (a
  conflict retries the reconcile), **before** the validation gate and the
  workloads and whether or not the gate passes, because the files need no
  validation: a user is added, changed or removed in the same reconcile that
  sees it. The order against Postfix is therefore *Dovecot first*: a new
  user is known to Dovecot (after the projection) before the validated
  Postfix revision accepts mail for it; for a removed or disabled user the
  login goes with the projection and Postfix follows with the rollout, and
  `soft_bounce` defers the mail it still accepts. The directory password and
  CA are not in this Secret: they stay in the hash-named Secret of the
  revision and are mounted at `/etc/dovecot-secret` (the path named in 4.8,
  point 6); only the `passdb` and `userdb` files are live. The Secret is
  rendered whatever the user source is (a directory leaves it without a
  line), so the pod always has the mount. The fail-closed render writes it
  empty. A server whose last `MailUser` is gone has no user source
  (`NoUserSource`, the running Postfix revision stays) and gets the empty
  files as well: a removed account must not keep its login. A `MailUser` event
  reaches the server after a debounce of three seconds (the delaying queue
  keeps one entry per key), and the watch has no generation predicate.
  An account whose credential Secret is missing or does not hold a hash of the
  form the operator writes is left out of the passdb file and stays in the
  userdb file and the Postfix maps: it cannot log in, and its mail is delivered.
  **The live Secret follows only the render that is meant to run.** The
  fail-closed fallback that Postfix gets while a narrowed revision (one domain
  revoked) validates does not write it: the accounts of the domains that stay
  authorized keep their logins, and only the revoked addresses leave the
  maps (section 4.6, per address). Only a real fail-closed render (no
  authorized domain, or a block with a domain still served) writes it empty.
  **While the server is blocked for another reason** (a render error,
  `UserSourceConflict`, `DirectoryInvalid`, `ClusterIssuerNotAllowed`, relay,
  IP pool claim) the live Secret is not written either: an account that is
  disabled or deleted meanwhile keeps its login until the block clears. The
  rule "logins are never blocked" below is for the limit blocks only (it is
  implemented for them, see "The limits of this section"); extending it to every
  block is in the backlog. A
  conflict on the update (another writer) is not an error: the reconcile
  requeues after a second and leaves `Ready` alone. The settled write of a blocked
  revision is computed from one read of the Secret and carries that read's
  resourceVersion, so it is refused, not applied over a newer content, when
  another writer changed the Secret in between.
  **The limits of this section (3b).** The counts are checked on the accounts
  of the server (the `MailUser`s, or the static test users) before the render
  (`addresses.CheckLimits`), and the byte guard on every ConfigMap and Secret
  of the revision, the live Secret included, right after the render and before
  anything is sent to the API server (`addresses.CheckObjects`; the check
  counts keys and values and is strictly below 1,000,000 bytes). A block is
  `Ready=False` on the `MailServer` with the reason `UserLimitExceeded`,
  `AddressLimitExceeded` or `ConfigTooLarge`; the message has the counts, or
  the (hash-named, non-secret) object and its size, and what the block does;
  a Warning Event is emitted when the reason or the message changes. Nothing
  of the revision is applied, so Postfix and Rspamd keep the data of the
  running revision, and any other change of the same pass (a new domain)
  waits with it; the cluster is not told to drop anybody. A first revision that
  is blocked creates nothing.
  **The previous accepted set is the live Secret itself.** The controller reads
  it back (`dovecot.ParseLive`: a passdb line is an account that can log in,
  with its hash and primary address; an account that is only in the userdb
  cannot), so it is in the cluster and survives a restart of the operator, and
  it is exactly what Dovecot reads. `addresses.Settle` with `blocked=true` then
  gives the passdb file: the lines that are in it, rendered again with the
  current hash, minus the accounts that are gone, disabled or suspended; no
  line is added. The userdb file is written back unchanged. **A removed or
  disabled account therefore loses its login (after the kubelet projection) but
  its address stays in the frozen Postfix maps and its userdb line stays**: mail
  for it is still accepted and delivered to the retained mailbox until the
  block clears. The count of the changes that wait (`Settled.Held`) is in the
  message; it does not count alias changes, because the live files do not carry
  aliases. Domain revocation does not come here: its fail-closed render has no
  users, and no limit applies to it. When the count falls below the cap, or the
  objects fit again, the next reconcile applies the revision as a whole.
  **Known limits, not covered by user docs.** That a running Dovecot pod keeps
  the files it last got when the Secret is deleted is expected of the kubelet
  and **not verified**; a pod that is rescheduled meanwhile cannot start until
  the Secret is written again by the next reconcile (Secrets are not
  watched). An oversized live Secret is no longer a case: the byte guard
  refuses the revision before the write. While a limit blocks the server a
  missing live Secret is not created either (a blocked revision only revokes):
  it stays missing until the block clears.
- **Alternative: restart.** One restart-based rollout per user change is the
  design this replaces. It stays in the backlog as the option for clusters
  that cannot grant `update`.

**Sizes and limits.** A ConfigMap or Secret is limited to 1 MiB (the API
server sums the values of `data`; the keys are not counted, verified on the
pinned Kubernetes version in row 43, so the guard below, which counts the keys
as well, is the conservative reading). That limit is **per object**: the revisions of
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
  DKIM tables of the main Rspamd ConfigMap over the limit. It is not split: it
  is one object, `<server>-rspamd-d-<hash8>`, checked like the others. A line
  is `address login` and a newline, so about 40 bytes at realistic sizes (the
  cap of 5,000 addresses makes 200 KB) and 384 bytes in the worst case (a
  318-byte address and a 64-byte login), which fills the guard at about 2,600
  addresses. Beyond that the revision is refused with
  `ConfigTooLarge`, like any other object, and the message names the object; a
  shard per range of addresses would be the answer if it ever shows, and is not
  built. Splitting the Postfix tables into one object each (the files are
  `subPath` mounts, so the source object does not matter) is in the backlog. It
  is a modest gain, not a fix: `valias` is 638 of the 1,022 worst-case bytes per alias, so one
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
   (Verification); on the Postfix side the same keys found nothing against
   the rendered `ldap:` and `pgsql:` tables (row 22; Postfix writes the
   key as `x\29` in the LDAP filter).
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
   `a.target` with `WHERE lower(a.alias) = '%s'`, joined to the users
   relation on `lower(u.mail) = lower(a.target)` with the login guard on
   `u.login`, so an alias of a removed or guarded account is `550` at
   `RCPT` as with LDAP, and not accepted and bounced later; row 28), a mailbox table (`mail=%s`), and, in
   3b, a sender table (`(|(mail=%s)(mailAlias=%s))` returning the login;
   run in row 34; it also carries `domain = <authorized domains>`, row 38). All
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
   temporary failures above. An LDAP directory with `None` is not usable by a
   `MailServer`: Dovecot checks a password by binding as the user, so every
   IMAP and submission login would send its password in clear text; the
   renderer refuses it and the server is `DirectoryInvalid` (Postfix alone
   could look up without TLS, but a `MailServer` always runs Dovecot). Without a `caSecretRef` the engines verify
   against the system roots of their images, which both images carry (the
   Postfix image installs `ca-certificates`; row 26). PostgreSQL `require` with a CA is verified
   like `verify-ca` (chain, no host name): libpq does this when a root
   certificate is present ([libpq SSL
   support](https://www.postgresql.org/docs/current/libpq-ssl.html)), and
   the probe of the operator does the same.
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
7. **Timeouts and caching.** The first measurement said Postfix's `timeout`
   covers the search but not the connection (a lookup blocked for 8 to 10 s
   against an unreachable server). **Re-measured with the directory maps on 3.11.7
   (Verification, row 22):** the `ldap:` table applies `timeout` to the
   connection as well (an address that drops packets fails after about
   1.2 to 1.5 times the value: `timeout = 3` after 4.5 s, `5` after 6.2 s,
   `10` after 11 s), so the renderer sets `timeout = 5` and no `ldap.conf`
   with `NETWORK_TIMEOUT` is needed. The `pgsql:` table has no timeout
   setting and libpq has none by default: a `RCPT` against a directory
   that drops packets was not answered within 300 s, so the renderer sets
   `PGCONNECT_TIMEOUT` to the same 5 s next to `PGSSLMODE` (and adds it to
   `import_environment`); the `RCPT` is then answered `451` after about
   5 s with LDAP and about 10 s with PostgreSQL, which connects twice (row
   27). The consequence is a slow `451`, not a loss, and the per-client
   limits of ADR 0006 apply. Dovecot's `ldap_timeout` (30 s) is left at its
   default; Dovecot 2.4.5 has no connect timeout setting for `pgsql` (only
   `pgsql_connection_limit`, `pgsql_host` and `pgsql_parameters`; `mysql`
   has `mysql_connect_timeout`), and a libpq `connect_timeout` in the
   parameters did not shorten a lookup against dropped packets (about two
   minutes either way, the query timeout of its SQL pool; row 27). Dovecot's `auth_cache_size` stays 0 (default): no stale
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
   (a local-part grant is in the backlog). **`senderDomains` holds DNS
   names** (the `spec.domain` of the `MailDomain`s), not object names: object
   names are per namespace, DNS names match `spec.sender` without a lookup
   and survive a `MailDomain` being deleted and created again. An empty list
   is refused at admission (`MinItems=1`): unset means every served domain,
   so `[]` would read as "no domains" and mean "all"; to allow nothing, leave
   the namespace out of `allowedNamespaces`.
5. **The credential Secret and the hash (3c).** The Secret `<name>-credential`
   in the namespace of the account holds, next to the keys of ADR 0008 (the
   `username` is the login of point 1, `server` the in-cluster name of the
   submission Service, `tls` is `mandatory`, `helo` and `tlsServerName` the
   hostname the server presents, `ca.crt` the `ca.crt` of its certificate
   Secret when it has one), the `password` and the `passwordHash`. The hash
   lives there as for a `MailUser` (section 3, point 4): the application has
   the plaintext anyway, so the hash adds nothing to what the namespace can
   read. The namespace can **write** the Secret, so the `MailServer` must take
   from it nothing but the hash: the login comes from the object (point 1,
   never from `username`), the hash is taken only from a Secret the account
   controls and only if it has the form the operator writes. The controller
   verifies the hash against the password (once per resourceVersion of the
   Secret: any write is checked, an unchanged Secret is not) and replaces both
   when they do not match. A pair that matches, with the pinned parameters, is
   **kept** whoever wrote it, so a tenant can choose the password of the login
   of **its own account**, and of no other: the login comes from the account
   object and `username` is written again at every reconcile. The right to
   edit the Secret is the right to set that one password. There is no
   `passwordSecretRef`: the password is always generated, so a deleted Secret or
   a restore from GitOps gives the application a new one.
6. **The NetworkPolicy (3c).** `<name>-submission` selects the pods that carry
   the label `cnms.rpp.gmbh/mailserviceaccount: <name>` and allows them egress
   to the Postfix pods of the server (a `namespaceSelector` on
   `kubernetes.io/metadata.name` and the pod selector of the server's Postfix)
   on port 587, and DNS. It selects labelled pods only: a policy isolates every
   pod it selects, so a policy over the whole namespace would cut off the
   application from everything else.
7. **Admission is recorded in `status.admittedSender` and
   `status.admittedFor` (3c).** `admittedFor` is
   `<server namespace>/<server>/<directory name, or empty>`. The probe of
   point 3 runs when `spec.sender` differs from `admittedSender` or the
   server and directory differ from `admittedFor`, so "new account or changed
   sender" is a comparison, not a history, and an account that names another
   server, or a server that gains or changes its directory, is asked again.
   Both are set when the checks pass and cleared when the namespace or the
   sender is revoked or the directory has the address. They are in the status,
   which tenants cannot write (an annotation on the Secret could be edited to
   skip the probe), and are rebuilt by a probe after a restore: a restored
   account is new. The `MailServer` serves an account only while they equal
   its `spec.sender` and its own server and directory.

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
  exist and carry their keys, TLS mode and CA are consistent (a CEL rule
  refuses `caSecretRef` with LDAP `None` or SQL `disable`), the LDAP filter
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
  golden files keep their content. **In the first version:** `Account` has the fields
  the renderers read (`Name`, `PasswordHash`, `Address`); `Aliases`,
  `Disabled` and `SendOnly` come with the commit that renders them (3b).
  `DirectorySource` holds the `LDAPTarget` or `SQLTarget` of
  `internal/directory` that the `MailDirectory` probe uses, read by the same
  resolver (`directoryResolver`), so the probe and the renderers see one
  configuration. `RenderResult` gets `Env` (the environment the engine
  process needs; part of the config hash only when set). A server that
  sets both a directory and `insecureStaticUsers` is `UserSourceConflict`
  (before, the static users won, and the e2e server set both). The Postfix renderer emits `vmailbox`,
  `valias`, `sender_login`, `recipient_defer`, `null_sender` (ConfigMap) and
  the `.cf` files (Secret); the Dovecot renderer emits the `passdb` and
  `userdb` files (the live Secret of 4.7) or the directory passdb/userdb
  blocks (ConfigMap, secret values by file reference); the Rspamd renderer
  emits `from_owners`. Rendering is deterministic: sorted, stable, no
  timestamps.
- **Validation gate (ADR 0009).** `postfix check` and `postconf -n` cover
  `main.cf` and `master.cf`; they do **not** query maps (verified: an
  unknown key in a `.cf` file is silently ignored, and a query against an
  unreachable server fails at query time). The new map files therefore get
  no real check from the Job; they are covered by golden files, a Go parser
  test per table, the `MailDirectory` probe, and e2e. The Job runs
  `postmap -s` on each `texthash` file (parse check, no server; it warns
  about a line it ignores and fails on a missing file, so the gate treats a
  `warning:` as a failure as it does for `postconf -n`). `doveconf -n`
  covers the Dovecot structure. A `master.cf` argument error is not caught
  (section 4.4); the e2e suite starts the listener.
  **Amended with the directory maps: the Postfix Job mounts the directory Secret.**
  The first text said the Job mounts no Secret and that a missing table
  file passes. That is wrong for `ldap:` and `pgsql:` tables (Verification,
  row 21): `postconf -n` and `postfix check` open the configuration of a
  table named in `main.cf` and warn `open "ldap" configuration ...: No such
  file or directory`, which fails the gate. The Postfix Job therefore
  mounts the Postfix Secret of the revision (the `.cf` files and the CA)
  read-only, exactly as the Postfix pod does; with it the gate passes
  without a network, so it never needs the directory. The Dovecot Job still
  mounts no Secret (`doveconf -n` does not read `<file` references).
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
| key of the secret-file digests | `<server>-hash-key` Secret, immutable, owned by the `MailServer`, 32 random bytes, created once, and only when a render holds secret content (the fail-closed render and a server without a directory or accounts need none, so a Secret of a tenant under that name cannot keep a revoked domain from being withdrawn) | the name of a revision's Secret and the config hash cover the bind password; a plain SHA-256 of it would let anyone who can read the pod template confirm a guess offline, so that part is an HMAC-SHA256 under this key. Losing it (a restore from GitOps) costs one rollout: the names and hashes change once. The hash of the non-secret files stays plain |

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
  can update any Secret). The `MailUser` has no finalizer, and its credential
  Secret is garbage-collected through the owner reference. That reference is
  set by `SetControllerReference`, which sets `blockOwnerDeletion=true`, so
  where the OwnerReferencesPermissionEnforcement admission plugin is on the
  operator needs `update` on `mailusers/finalizers`, the rule that
  `mailservers/finalizers` and `maildomains/finalizers` already carry. Tenant roles: a `mailuser-editor` and
  `mailuser-viewer`, and the generated admin role. Creating a `MailUser`
  lets its author create mailboxes and passwords, but only for domains that
  are authorized for the namespace.

### 8. Conditions

`MailUser`:

| Condition | Reasons |
|---|---|
| `Ready` | `Ready`, or the reason of the first failing condition below; plus `ServerNotFound`, `DirectoryConfigured`, `UserSourceConflict`, `DomainNotFound`, `DomainNotServed`, `DomainNotAuthorized`, `Disabled` (not a failure: `Ready=True`, reason `Disabled`) |
| `CredentialReady` | `Ready`, `SecretNotFound` (the existing reason, for the Secret or its key), `CredentialSecretConflict` (a Secret `<name>-credential` exists that the `MailUser` does not own; it is never taken over), `PasswordInvalid`, `HashFailed` |
| `AddressesServed` (warning, never feeds `Ready`) | `AllServed`, `PartiallyServed` (the message names the addresses and their reasons, conflicts included); `Unknown` with the reason of `Ready` (`ServerNotFound`, `DirectoryConfigured` or `UserSourceConflict`) when nothing serves the account, so no address is decided |

The reasons of one **address** are in `status.addresses[]`, not on the
condition: `Served`, `AliasDomainNotFound`, `AliasDomainNotServed`,
`AliasDomainNotAuthorized`, `AddressConflict`. The row of the primary
address, and the row of an alias of a suspended account (section 4.6), show
the cause on the **primary** domain instead: `DomainNotFound`,
`DomainNotServed` or `DomainNotAuthorized`.

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
| PostgreSQL | `postgres:18.6-alpine`, pinned by digest (`sha256:123b17998ccaf47e53b502e8c6a1e79afd8f579c6f3eec83c563da026f957395`, the digest of row 18; the exact tag was confirmed with the e2e fixtures, row 25) | **PostgreSQL License** ([licence](https://www.postgresql.org/about/licence/); package metadata) | 433 MB on disk; ready with schema about 2.4 s; `/docker-entrypoint-initdb.d` seeds SQL; TLS needs the key owned by `postgres` mode 0600 |

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
  `docs/reference/testing.md`.
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
- **The DKIM e2e** (`test/e2e/dkimutil.go`, `submitFromDomain`) sends with
  the mailbox that the directory user owns as the envelope sender and
  `--h-From` set to `<user>@<domain>` for the domain under test. Rspamd signs
  by the domain of the header `From`, and check 2 is off for a directory
  server, so those specs stay on the directory. The `MailUser` specs
  (aliases, sender binding, the header `From` binding) are their own block in
  `test/e2e/mailuser_test.go`.
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

The text of the earlier decisions stays. ADR 0008, ADR 0004 (item 6),
ADR 0003, ADR 0009, ADR 0013 and ADR 0014 carry their pointers now, inline,
as the amendments of ADR 0014 do; each was added by the pull request that
implements the change, so an ADR never points at behaviour that does not
exist.

- **ADR 0004, item 6:** the CNMS image tags are
  `<POSTFIX_VERSION>-<revision>` (3a, section 9), for the Postfix and the
  e2e client image; an inline amendment note in ADR 0004 says so.
- **ADR 0008:** the `MailUser` name is a DNS-1123 subdomain; `MailUser` has
  `domainRef` and `aliases`; the three-PR split; sender binding is strict
  and covers every user source; header `From` binding for authenticated
  mail moves from the backlog into check 1 (PR #40, ahead of 3a) and 3b
  (check 2);
  `allowedNamespaces` holds objects; the one-source rule has a reason for
  its conflict and counts service accounts.
- **ADR 0003:** the address ownership and conflict rules (section 4.2) and
  the primary-domain rule (4.6) extend the revocation path to user data.
- **ADR 0009:** the Dovecot passdb and userdb files are two keys of a stable,
  mutable Secret outside the content hash (4.7), with the new `update` verb
  on Secrets; the
  credential Secret is updated in place; map files are not validated by the
  Job beyond `postmap -s`; limits and the refusal of an over-limit revision.
- **ADR 0013:** the known gap of section 3 is closed by check 1 (PR #40,
  ahead of 3a) for every user source and by check 2 in 3b for
  operator-managed accounts, with the directory residual.
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
  (4.7, row 31); Postfix map changes are still a validated rollout;
  the map files get no real validation from the Job; check 2 needs Rspamd
  work in 3b and leaves a documented residual for directories; the Postfix image gains libpq, a
  rebuild and a build revision in its tag (section 9).
- **Limits:** 32 aliases per user, 1,000 users and 5,000 addresses per
  server, and a byte guard on every rendered object (4.7); no shared
  addresses, distribution lists, catch-all, forwarding to external
  addresses, per-alias send-only, plus-addressing, quotas (Phase 4),
  MySQL/MariaDB, automatic `postmaster@` (all in `docs/backlog.md`).
- **How we know it worked:** the e2e of section 1, the throughput and memory
  test of the password hash, the LDAP timeout measurement (row 22), the guard matrix
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
| 19 | The committed Postfix image (3a, the Postfix image) | `docker build -t ghcr.io/rocketpadplatforms/cnms-postfix:3.11.7-1 images/postfix` (amd64) with `libpq-dev`, `libpq5`, `-DHAS_PGSQL -I/usr/include/postgresql`, `AUXLIBS_PGSQL=-lpq`; in the image `postconf -c <dir with an empty main.cf> -m`, `postconf -h mail_version`, `ldd /usr/libexec/postfix/smtpd`, `/usr/share/doc/libpq5/copyright` | Builds in about 1.5 minutes (warm cache); `postconf -m` lists `ldap` **and** `pgsql` (`btree cidr debug environ fail hash inline internal ldap lmdb memcache pgsql pipemap proxy randmap regexp socketmap sqlite static tcp texthash unionmap unix`); `mail_version` 3.11.7; `smtpd` links `libpq.so.5`; Debian `libpq5` 17.11 (trixie), licence `PostgreSQL` (upstream [licence](https://www.postgresql.org/about/licence/), read 2026-11-06). Image size 207 MB on disk (the stock build was 202 MB, row 1). Query behaviour of the `pgsql` table is not exercised here. |
| 20 | The filter and the probe of the `MailDirectory` controller (3a, the MailDirectory controller) | Go unit tests (`internal/directory`) against `go-ldap/ldap/v3` v3.4.15 and `jackc/pgx/v5` v5.12.0 (both MIT, licences read in the module cache; `make license-check` passes) | `CompileFilter` accepts the composed probe filter of 4.8, point 2, with `(!(<login>=.*))(!(<login>=*/*))(!(<login>=sa.*))`, and refuses `mailEnabled=TRUE`, `(mailEnabled=TRUE`, `()`, `(a=b)x` and a filter that closes early and appends a second one as an active filter, so the active filter is compiled alone as well as composed; `(&)` is a valid filter. The pgx configuration built field by field (no connection string) has no fallbacks to plain text in any TLS mode, takes the password verbatim, and `pgx.Identifier` quotes a relation name that holds a quote or a semicolon. The classification of connection, TLS, bind and search errors is tested with synthetic errors, and the real error values of pgx and go-ldap with loopback servers (a server that refuses TLS, a plain-text server on an LDAPS port, a closed port); a refused connection to a host name that contains `tls` is not a TLS failure, and PostgreSQL `require` with a CA verifies the chain without the host name. **Not run:** the probe against a real OpenLDAP or PostgreSQL (CI e2e only), and whether a login attribute without a SUBSTR rule makes the guard match nothing. |
| 21 | The validation gate with directory tables (3a, the directory maps) | The rendered `main.cf` and `master.cf` of the golden files (`directory-ldap`, `directory-pgsql`) and the `validateScript` of the renderer, run with `sh` in the pinned Postfix image, with a tmpfs queue: the ConfigMap files only, then the Secret mounted too (`--network none`), then a `vmailbox` with a line `onlykey` and then none | Without the Secret the gate fails: `postconf` (and so `postfix check`) warns `open "ldap" configuration "/etc/postfix-secret/ldap-mailbox.cf": No such file or directory` for every table named in `main.cf`. This corrects the first text of section 5. With the Secret mounted, and no network, it exits 0 for both sources (the tables are opened, not queried). `postmap -s texthash:` warns `expected format: key whitespace value -- ignoring this line` for a bad line (exit 0) and fails on a missing file (exit 1); the script fails on both. |
| 22 | The rendered Postfix tables against OpenLDAP and PostgreSQL (3a, the directory maps) | `osixia/openldap:2.6.15-rc` and `postgres:18-alpine` (digests of row 18) on a private network, with TLS from a throw-away CA, seeded with `alice` (aliases `sales@b.example`, `alice@b.example`), `bob`, a disabled `carol`, and accounts whose login is `x/../bob`, `.hidden` and `sa.0123456789abcdef`; the `.cf` files, `main.cf` and `master.cf` of the golden files (the LDAP bind as the directory root, the CA replaced by the real one, the plain `smtpd` on port 25 and no milter instead of postscreen and Rspamd), started with the image entrypoint; an SMTP dialogue to `RCPT TO` from loopback, `postmap -q` for the quoting. The LDAP and the PostgreSQL service stopped, a wrong CA, a wrong bind password | LDAP: `alice@a.example` (also in upper case), `sales@b.example`, `alice@b.example` (alias to the canonical address), `bob@a.example` `250`; the disabled account, its alias, and the three guarded logins `550 5.1.1`; an unknown address `550 5.1.1`. Directory stopped, wrong CA, wrong bind password: `451 4.3.0 Temporary lookup failure` for known and unknown recipients; the directory back: `250` and `550` again. `StartTLS` and `LDAPS` (`ldaps://host:636`, `start_tls = no`) work with the CA; without the CA file the verification fails. PostgreSQL: the same matrix, `saturn@a.example` (`sa` is not the reserved form) `250`; `x'` and the key of an injection find nothing; PostgreSQL stopped, a wrong CA (`PGSSLROOTCERT`): `451 4.3.0`; after the restart the answers return after about 60 s (the `pgsql` client waits that long after a failed connection: `451` at +8 s, `250` at +70 s). Without `PGSSLMODE` in the environment the connection falls back to the default (no verification): the variables must reach the master. **Quoting and escaping:** `bind_pw`, `password` and `bind_dn` are taken literally, with `$`, `%`, `'`, `"`, `\` and `#` in them (no `$name` expansion, no `%` expansion); `search_base` expands `%` and needs `%%`; Postfix writes the key RFC 4515 escaped into the LDAP filter and `pgsql` quotes it (`x)(uid=*` gives `x\29`, no entry). **Timeout:** the `ldap:` table's `timeout` bounds the connection to an unreachable address too (`3` after 4.5 s, `5` after 6.2 s, `10` after 11 s), unlike the first measurement of row 6. The failure text of `pgsql:` names the host and port, not the password. **Not run:** the LDAP table through `proxy:`, `LDAPS` against a public CA, `require` without a CA. |
| 23 | The rendered Dovecot configuration against OpenLDAP and PostgreSQL (3a, the directory maps) | The `dovecot.conf` and the secret files of the golden files in `dovecot/dovecot:2.4.5` (read-only root, tmpfs runtime, `/etc/dovecot-users` mounted, the LDAP lookup as the directory root); `doveconf -n` with the ConfigMap only; `doveadm auth test`, `doveadm auth login`, `doveadm user`; an LMTP dialogue from the Postfix image with and without `DATA`; the directory stopped | `doveconf -n` exits 0 with no Secret, no certificate and the `<file` files missing (both sources), and hides `ldap_auth_dn_password`. LDAP and PostgreSQL: login by `alice`, `alice@a.example` and `ALICE@A.EXAMPLE` succeeds and reports `user=alice` (and `original_user`); the alias as login fails; the disabled account (LDAP), the three guarded logins, `alice)(uid=*`, `*`, `x' OR '1'='1` and `alice' --` fail; `doveadm user` finds `alice` and `alice@a.example` with `home=/srv/mail/alice`, and not the alias or a guarded login. LMTP delivers to `alice@a.example` (Saved) and answers `550 5.1.1` for the alias and for an unknown address. The directory stopped: `doveadm auth test` is an `internal auth failure` (not `auth failed`); the LMTP `RCPT` is `451 4.3.0 Temporary internal error` (PostgreSQL at once, LDAP with an unresolvable host name after about 2.5 minutes); both recover without a restart. `ldap_starttls`, `ldaps://`, `ssl_client_ca_file` and `sslrootcert` verify against the CA; a wrong CA is an internal failure. **Escaping:** `ldap_base` and `ldap_auth_dn` take a lone `%` literally (`ou=50%`), a doubled one does not match (`ou=50%%` finds nothing), so the renderer does not double it and refuses `%{` in the base; a `#` in a value is a comment unless the value is double-quoted. The `<file` password with `$ % ' @` works for PostgreSQL. The regular expression `'^sa\\.'` in a double-quoted query reaches PostgreSQL as `'^sa\.'` (`saturn` logs in, `sa.0123456789abcdef` does not). |
| 24 | The Dovecot 2.4.5 setting names of the directory blocks (3a, the directory maps) | `doveconf -a` and `doveconf -n` of the image for `ldap_uris`, `ldap_starttls`, `ldap_base`, `ldap_auth_dn`, `ldap_auth_dn_password`, `ssl_client_ca_file`, `passdb ldap { bind, filter, fields }`, `userdb ldap { ldap_connection_group, filter, fields }`, `sql_driver`, `pgsql <host> { parameters { host, port, user, password, dbname, sslmode, sslrootcert } }`, `passdb sql { query }`, `userdb sql { query }`; the docs of 2.4.5 for the LDAP and SQL databases | All accepted, and shown in `doveconf -n` under their 2.4 names (`ssl_client_ca_file` as `ssl_client { ca_file }`). `passdb_use_worker` is left at its default (`no`) for both drivers: the PostgreSQL and LDAP lookups run in the `auth` process, and the auth-worker limit of section 3 applies to the passwd-file only. |
| 25 | The e2e fixtures and the directory specs (3a, the e2e fixtures) | `docker pull` of both fixture images by digest and `docker image inspect` (entrypoints, ports, users, bootstrap directories); the Docker Hub tag list of `library/postgres` for `18.6-alpine`; the full e2e suite (`go test -tags=e2e ./test/e2e/`, the Makefile's kind node image `kindest/node:v1.37.0` on a kind v0.32.0 cluster, without the Prometheus Operator CRDs) with an OpenLDAP and a PostgreSQL fixture in the data plane namespace | `postgres:18.6-alpine` and `18.6-alpine3.24` both resolve to `sha256:123b17998ccaf47e53b502e8c6a1e79afd8f579c6f3eec83c563da026f957395`, the digest of row 18, so the e2e pins `postgres:18.6-alpine@sha256:123b...`. `osixia/openldap:2.6.15-rc` listens on 3890 and 6360 as user `ldap` (911), bootstraps from `.../ldif/data/custom` and takes TLS from `OPENLDAP_BOOTSTRAP_TLS` and the default certificate paths; its default access rules hide `mailAlias` and `mailEnabled` from a bound user, so the lookup account is the image's read-only account. The suite passed (41 of 41, about 15 minutes) against both directories: the `MailDirectory` probe is `Ready` and `Reachable`; valid recipient, alias and unknown address give `250`, `250` and `550 5.1.1` (LDAP: the `mailEnabled=FALSE` account `550 5.1.1`); a message for an alias lands once in the mailbox of the canonical address; SASL and IMAP accept the login name and the primary address (also in upper case) and refuse the alias; with the directory refusing connections recipients get `451 4.3.0` (known, unknown and alias) and recover (PostgreSQL after the client's wait); a server with a directory and static users is `UserSourceConflict`, keeps its pod template and answers from the directory, and serves the static users alone once the directory is removed. **Not as written elsewhere in this ADR:** with PostgreSQL refusing connections, SASL on 587 answered `535 5.7.8` (Dovecot: "Not connected to database"), not `454 4.7.0` (LDAP gave `454 4.7.0`); and after a few failed logins from one client Postfix sometimes reported "Connection lost to authentication server" (`454`) for a login that should be `535`. **Found:** the egress NetworkPolicy of the data plane blocks the directory (the fixtures need a policy of their own), and a `pgsql:` lookup against an address whose packets are dropped had not answered after the 30 seconds the test client waited (no connect timeout is set). |
| 26 | System roots in the Postfix and Dovecot images (3a, the Postfix image) | The runtime stage of `images/postfix` before the change, rebuilt from the base with the same package list: `ls /etc/ssl/certs`. The rebuilt `cnms-postfix:3.11.7-1` (now with `ca-certificates`) and a throw-away derived image that adds a test CA with `update-ca-certificates`, on a private Docker network with OpenLDAP and PostgreSQL fixtures (row 18 images) whose certificates chain to that CA: `postmap -c <dir> -q alice@a.example ldap:<table>` with `start_tls = yes`, `tls_require_cert = yes` and **no** `tls_ca_cert_file`, and `pgsql:<table>` with `PGSSLMODE=verify-full PGSSLROOTCERT=system`, each in the image without and with the test CA. The pinned Dovecot image (`docker export`, `dpkg` status) and a Dovecot started with the LDAP and the PostgreSQL blocks of the renderer minus `ssl_client_ca_file` and with `sslrootcert = system`, `doveadm auth test`, with and without the test CA as a hashed link in `/etc/ssl/certs` (`openssl x509 -hash`, what `update-ca-certificates` links) | **Before:** the Postfix runtime image had no `/etc/ssl/certs` at all (`ca-certificates` is not installed by `--no-install-recommends`), so a directory with a public certificate and no `caSecretRef` verified against nothing and answered `451` while the operator's probe (its own image has roots) said `Reachable`. **After:** `/etc/ssl/certs/ca-certificates.crt` exists (148 roots, `ca-certificates` 20250419); without the test CA `postmap` fails (`Unable to set STARTTLS: -11: Connect error`; `SSL error: certificate verify failed`), with it both lookups return `alice@a.example`. **Dovecot 2.4.5 image:** Debian trixie with `ca-certificates` 20250419 (`/etc/ssl/certs/ca-certificates.crt` and 151 hashed links), `libpq5` 17.11 (`sslrootcert=system` needs 16 or later) and `libldap2` 2.6.10; without a CA setting both the LDAP StartTLS and the pgsql (`sslrootcert = system`, `verify-full`) lookups fail (`ldap_start_tls_s() failed: Connect error`, `certificate verify failed`, `temp_fail`) and with the test CA in the system store `doveadm auth test` succeeds for both. So no API restriction is needed: an omitted `caSecretRef` means the system roots in all three places (probe, Postfix, Dovecot). The Postfix image content changed under the unreleased tag `3.11.7-1`, which was introduced in the same pull request, so the revision is not raised. |
| 27 | Lookup timeouts against an address that drops packets (3a, the directory maps) | The rendered `main.cf`, `master.cf` and tables of the golden files, with the directory host `10.255.255.1` (no route answers, packets are dropped), in the Postfix image with only `smtpd` and a `lmtp` virtual transport that is not used; a bash `/dev/tcp` `RCPT TO` of a recipient and its timing (read timeout 300 s); the same with the environment of the old render (no `PGCONNECT_TIMEOUT`). Dovecot 2.4.5 against a PostgreSQL fixture, then `iptables -A OUTPUT -d <fixture> -j DROP` in the shared network namespace (a container with `NET_ADMIN`) and `pg_terminate_backend` of the connection, then `doveadm auth test` timed; `doveconf -a` and the 2.4 pages on the SQL database for the settings of `pgsql` | Postfix `ldap:` with `timeout = 5`: `451 4.3.0 Temporary lookup failure` after 5 s. Postfix `pgsql:` with `PGCONNECT_TIMEOUT=5`: `451 4.3.0` after 10 s (the client connects twice). Without `PGCONNECT_TIMEOUT` the `RCPT` was not answered within 300 s. Dovecot: `doveconf -a` knows `pgsql_connection_limit`, `pgsql_host` and `pgsql_parameters` and no connect timeout for `pgsql` (`mysql_connect_timeout` exists for `mysql`); `connect_timeout = 5` in `parameters` is accepted, but a lookup with the packets dropped took about 120 s with it and without it (Dovecot logs `sqlpool(pgsql): Finished query ... Query timed out (no free connections)` after about 117 s, then answers `temp_fail`), and the first lookup after the connection was cut answers `temp_fail` at once. With `ldap_timeout` at its default Dovecot's LDAP side was not measured against dropped packets. At start with the fixture unreachable the `auth` service did not answer the handshake within 30 s with either variant (row 16). |
| 28 | The SQL alias table joined to the users relation (3a, the directory maps) | A PostgreSQL 18.6 fixture (row 18 image) with TLS, users `alice`, `bob`, `x/../bob` (guarded) and `sa.0123456789abcdef` (reserved), aliases to `alice`, to `BOB@a.example` (upper case), to an address that is no user, to the guarded login and to the reserved one; the rendered `pgsql:` mailbox and alias tables in the Postfix image, started like row 27 with a `lmtp` transport; `RCPT TO` for each address; the same with the former alias query (`SELECT target FROM aliases WHERE lower(alias) = '%s'`) | New query: `alice@` `250`, the alias to `alice` `250`, the alias to `BOB@a.example` `250`, the alias with no user, the alias to the guarded login, the alias to the reserved login, the guarded login itself and an unknown address `550 5.1.1`. Former query, same data: the three aliases with no usable target were accepted with `250` (mail that bounces later). An alias whose target is an account of the users relation in a domain the server does not serve gets `250` too, and the message is rewritten to the target and handed to the smtp client for that domain (outbound delivery; here it bounced for want of DNS). |
| 29 | The validation gate with an unreachable directory (3a, the directory maps) | The `validateScript` of the renderer (`postfix check`, `postmap -s`, `postconf -n`) in the Postfix image with the rendered `main.cf`, `master.cf`, `vmailbox` and the Secret of the golden files, once with the PostgreSQL tables and once with the LDAP tables pointing at `10.255.255.1`, a tmpfs queue and no network access to a directory | Exit 0 in about 1.5 s for both: the gate opens the tables but makes no lookup, so a directory that is down or unreachable neither hangs nor fails it. |
| 30 | The CEL rules and the cost estimate of 4.1 on the API server (3b, the MailUser API) | envtest (`make test`, the API server of the Kubernetes 1.37 envtest assets), `internal/controller/api_validation_test.go` | The CRD installs. A `MailUser` with **no** `aliases`, one with 32 aliases in two domains and names with dots are admitted; a duplicate alias, an alias equal to the primary address, 33 aliases and a name that starts with `sa.` are refused. The numeric cost estimate was not read out: the API server accepted both rules at 32 items. |
| 31 | `soft_bounce` as a per-service override on the `lmtp` entry (3b, the account tables) | `docker build -t cnms-postfix-3b:test images/postfix` (the Dockerfile of `3.11.7-1`, Postfix 3.11.7, a local tag); the `accounts` golden files of the Postfix renderer (`main.cf`, `master.cf` and the five tables) with three test-only changes (no milters, `smtpd` in place of `postscreen`, LMTP to the listener port 31024), Dovecot 2.4.5 with the rendered `dovecot.conf` and users files behind the network alias of the Service, published ports, `swaks`; a message to `dave@a.example`, which is in `vmailbox` and not in the Dovecot userdb; then the same `master.cf` without the `-o soft_bounce=yes` line; then the user added to the userdb file (appended in place) and `postqueue -f` | With the override: `RCPT` is accepted, Dovecot answers `550 5.1.1 User doesn't exist` to the LMTP `RCPT`, and Postfix logs `dsn=4.1.1, status=SOFTBOUNCE`; the message stays in the queue (`postqueue -p`). Without it: `dsn=5.1.1, status=bounced`, the queue is empty. With it set, an unknown recipient is still `550 5.1.1` at the SMTP `RCPT` (the override is for the `lmtp` service alone). After the user line was appended and the queue flushed, the message was delivered (`status=sent`), and Dovecot had read the appended file without a reload. **Not run:** the retry timing (`postqueue -f` stood in), and the kubelet's own projection. |
| 32 | Two passwd-files, the `user=` field and the login forms (3b, the account tables) | The `accounts.passdb` and `accounts.userdb` golden files (hashes replaced by a real `{SHA512-CRYPT}` hash from `doveadm pw`) in `dovecot/dovecot:2.4.5` (read-only root, tmpfs runtime), `doveadm auth test`, `doveadm auth login`, `doveadm user`; the same files behind Postfix (row 36) | The passdb file with empty uid, gid and home fields and the extra field `user=<name>` works: `alice` and `alice@a.example` (also in upper case) succeed and report `user=alice`; the alias, the disabled `bob`, the suspended `carol` and a wrong password fail. The userdb file without a password field works: `doveadm user` finds `alice`, the disabled `bob@a.example` and `bob`, the suspended `carol@revoked.example`, all with the home `/srv/mail/<name>`, and not the alias `alice@b.example`. LMTP delivered to the disabled `bob` (through the alias `bobby@b.example`) and a SASL login of `bob` through Postfix was `535 5.7.8`. |
| 33 | The auth worker settings of 2.4.5 (3b, the account tables) | `doveconf -n` of the rendered `dovecot.conf` with `passdb_use_worker = yes` inside the `passdb passwd-file` block and `service auth-worker { process_limit = 4 }`; the same with an invented setting in the service block (negative control); `docker top` of the running container after `doveadm auth test`, with and without the setting | Both settings are accepted (`doveconf` prints the first as `use_worker = yes`); the invented setting exits 89 (`Unknown setting: bogus_limit (service_bogus_limit or service_auth-worker_bogus_limit not found either.)`). With the setting an auth worker (`dovecot/auth -w`) exists after a login, without it there is only `dovecot/auth`: the passwd-file passdb honours `passdb_use_worker`. **Not run:** the memory under 50 parallel failing logins, and that the limit holds under load (section 3, point 4). |
| 34 | The LDAP sender table (3b, the account tables) | `osixia/openldap:2.6.15-rc` (digest of row 18, no TLS here: TLS is rows 22 and 23) with `alice` (`mail`, two `mailAlias` values), `bob`, a `mailEnabled=FALSE` `carol` with an alias, and accounts whose login is `x/../bob`, `.hidden` and `sa.0123456789abcdef`; the rendered `ldap-sender.cf` and `main.cf` in the Postfix image of row 31; `postmap -q`, then an SMTP `MAIL FROM` / `RCPT` with `smtpd_sender_restrictions = reject_sender_login_mismatch` and `smtpd_sasl_auth_enable = yes` (no login), the directory stopped and started | `postmap -q`: `alice@a.example`, `ALICE@A.EXAMPLE`, `sales@b.example` and `alice@b.example` return `alice`, `bob@a.example` returns `bob`; the disabled account and its alias, the three guarded logins, their aliases and an unknown address return nothing. In the session an owned sender is `553 5.7.1 ... not logged in` (no login) and an unowned one is accepted, so the table is read; with the directory stopped both are `451 4.3.0 Temporary lookup failure`, and after the start the answers return. This closes the "inferred" of rows 6 and 4.4 for LDAP. |
| 35 | The PostgreSQL sender table (3b, the account tables) | `postgres:18.6-alpine` (digest of row 18, no TLS) with `alice` and `bob` (`Bob@A.example`), the guarded logins and aliases to them, an alias of an upper-case target, an alias of an address that is no user; the rendered `pgsql-sender.cf` and `main.cf`; `postmap -q` and the session of row 34; the database stopped | `alice@a.example` in any case, `sales@b.example` and `alice@b.example` return `alice` once (the `UNION` merges the rows); `BOB@A.EXAMPLE` and the alias `Bobby@B.example` (target `BOB@a.example`) return `bob`; the alias of a missing user, the guarded logins and their aliases, an unknown address and `x' OR '1'='1` return nothing. In the session an owned sender is `553 5.7.1`, an unowned one is accepted, and with the database stopped both are `451 4.3.0`. |
| 36 | The rendered account tables against Postfix and Dovecot (3b, the account tables) | The setup of row 31, `swaks` against port 25 and, with `STARTTLS` and `AUTH PLAIN`, against the `587` entry of the rendered `master.cf` (braces form); accounts `alice` (aliases `alice@b.example`, `Sales@b.example`), `testuser`, a disabled `bob` (alias `bobby@b.example`), a suspended `carol` (primary in an unauthorized domain, aliases in an authorized and in a foreign domain) and a conflicted `shared@b.example` | **Port 25:** `alice@a.example` (also `ALICE@A.EXAMPLE`), `alice@b.example`, `sales@b.example`, `bob@a.example` and `bobby@b.example` are `250`; `carol@a.example` and `shared@b.example` are `450 4.2.1 ... The address is temporarily not available` (the `recipient_defer` table); `carol@revoked.example` and `carol@other.example` are `554 5.7.1 Relay access denied`; unknown addresses `550 5.1.1`. One message to `alice@a.example`, `alice@b.example`, `sales@b.example` and `bobby@b.example` was delivered once to `alice` (one message in the INBOX, the three log lines carry the same message id and `orig_to`) and once to `bob`, the disabled account. **Port 587:** `alice` as sender `alice@a.example`, `alice@b.example`, `sales@b.example`, `ALICE@B.EXAMPLE` passes the sender check (the reply is the later recipient check); login by `alice@a.example` works too; `testuser@a.example`, an unknown local part, a foreign domain, `bobby@b.example`, `carol@a.example`, `shared@b.example` are `553 5.7.1 ... not owned by user alice`; `MAIL FROM:<>` is `554 5.7.1 ... The null sender is not allowed on submission`; `testuser` as `alice@a.example` is refused; a sender without a domain is `504 5.5.2`. `bob` and `carol` cannot log in (`535 5.7.8`). The restriction is evaluated at `RCPT`, not at `MAIL`, because `smtpd_delay_reject` is on: a probe that stops after `MAIL` sees nothing. |
| 37 | The validation gate with the five tables (3b, the account tables) | The `validateScript` of the renderer in the Postfix image of row 31 with the rendered `main.cf`, `master.cf` and tables, then with one line of `valias` that has no value, then without the `sender_login` file | Exit 0 with all tables; exit 1 with `postmap: warning: /etc/postfix/valias, line 1: expected format: key whitespace value -- ignoring this line`; exit 1 with `postmap: fatal: open database /etc/postfix/sender_login: No such file or directory`. |
| 38 | The `domain` parameter of the sender tables (3b, the account tables) | The rendered `ldap-sender.cf` and `pgsql-sender.cf` with `domain = a.example, b.example`, the `587` entry of the rendered `master.cf` and the Postfix image of row 31; the fixtures of rows 34 and 35 extended with the alias `alice@c.example` of `alice` (LDAP `mailAlias`, a row of the aliases relation) and an account `dave` with `dave@c.example`; Dovecot 2.4.5 with a passwd-file passdb for `alice` as the SASL backend (the login name equals the login of the directory); `AUTH PLAIN` as `alice`, `MAIL FROM` / `RCPT` (the check runs at `RCPT`); the same with the `domain` line deleted (control); the directory stopped; `postmap -q` against the stopped directory | With the line: `alice@a.example` and `sales@b.example` pass the sender check; `alice@c.example`, which the directory gives to `alice` but in a domain the server does not serve, and `dave@c.example` are `553 5.7.1 ... not owned by user alice`, for LDAP and PostgreSQL. Control without the line: `alice@c.example` passes (the gap). With the directory stopped, `alice@c.example` is still `553` and `postmap -q` of it prints no connection warning (the directory is not asked), while `alice@a.example` is `451 4.3.0` and `postmap -q` warns that it cannot contact the server (LDAP) or resolve the host (PostgreSQL). The fail-closed render has no authorized domain, no submission and no sender table (`TestDirectoryOnlyWhereRead`); a domain name that is not a plain name (a path, a list, a blank) is refused by the renderer. |
| 39 | A hash made by the Go code, read by Dovecot (3b, the MailUser controller) | The hash of `hashWithSalt("correct horse battery", 00 01 ... 0f)` of `internal/controller/mailuser_credential.go` (`{ARGON2ID}$argon2id$v=19$m=19456,t=2,p=1$AAECAwQFBgcICQoLDA0ODw$quyusfeeuAGbpiw09i+j4VKJ+I/5h3mugd4HghmS/x8`, 107 bytes); in the pinned `dovecot/dovecot:2.4.5` image `doveadm pw -t '<hash>' -p '<password>'` with the right and a wrong password, and `doveadm auth test` against a passwd-file passdb (`login:hash::::::user=alice`, the rendered `dovecot.conf` with `passdb_use_worker` and `service auth-worker { process_limit = 4 }`) by name and by address; `doveadm pw -s ARGON2ID -p 'correct horse battery'` of the same image | Dovecot answers `(verified)` (exit 0) for the right password and `Fatal: reverse password verification check failed: Password mismatch` (exit 75) for a wrong one; `doveadm auth test` succeeds for `alice` and `alice@a.example` and fails for a wrong password. The hash that Dovecot makes (`m=65536,t=3,p=1`) is recomputed by the Go Argon2 library from the parameters it carries, in `TestTheLibraryVerifiesTheHashDovecotMade`; the verifier of the operator reads **only** the pinned parameters (`m=19456,t=2,p=1`, a 16-byte salt, a 32-byte tag) and treats any other hash, such as one of Dovecot's own, as not matching, so that a hash edited into the Secret cannot make a verification cost more (`TestVerifyHash`). The known answer is fixed in `TestHashKnownAnswer`. This closes "a hash made by the Go Argon2id library read by Dovecot". |
| 40 | The load of the hash on a Dovecot with the worker limit (3b, the MailUser controller) | One `dovecot/dovecot:2.4.5` container, `docker run -m 512m --memory-swap 512m`, the rendered conf (`passdb_use_worker = yes`, `process_limit = 4`) and the passdb of row 39; five rounds in 65 s of 50 parallel failing `doveadm auth test` logins each (`docker exec`), with one correct login started in each round; the memory of the container sampled with `docker stats` about every 2 s | The container used 20.7 MiB before the load, and the **sampled peak was 95.6 MiB** (limit 512 MiB; a sample every 2 s can miss a higher peak); every correct login succeeded, in 8.6 to 15.6 s while the flood ran. The latency was **not analysed**: it is much more than 50 hashes of well under 0.1 s on 4 workers would take, so the 50 parallel `docker exec` clients or the failure delay of the auth service are the likelier causes than the worker limit; this row says nothing about the latency of a real login. One login alone takes well under 0.1 s. **The load-test criterion of section 3, point 4 is not met by this row and remains a 3b done item:** the `memory.peak` of the cgroup (not readable on this host, rootless Docker), IMAP logins, and mail delivered during the flood were not measured. The numbers are of one host and are no guarantee. |
| 41 | The live users Secret read by Dovecot without a restart, and the latency of the kubelet projection (3b, the `MailServer` serves the `MailUser`s) | e2e suite on kind v0.33.0 with the node image `kindest/node:v1.37.0`, one node, the pinned Dovecot 2.4.5 pod `mail-dovecot-0`; `MailUser`s with `passwordSecretRef`; the account is created, its password Secret patched, and the object deleted, and a SASL login on 587 is repeated every 3 s until it succeeds (or is refused); the pod UID and the restart counts of its containers are compared before and after | Three runs: an added account logged in after **56 s** to **1 min 21 s**, the new password worked after **1 min 3 s** to **1 min 28 s** (the old one was refused 2 s later in the first run), a removed account was refused after **1 min 6 s** to **1 min 22 s**, and a re-enabled one logged in after **21 s** to **42 s**. All accounts had a `passwordSecretRef`; the operator-generated path was not measured. The times include the operator (the object event and its debounce of 3 s; for the password up to the polling interval of one minute), the kubelet sync (1 minute by default) and the cache delay, so they are what a user of that cluster sees, not a bound. The UID and the restart counts of the Dovecot pod did not change: Dovecot re-read the passwd-files after the kubelet swapped the projected directory (the symlink swap and the two files, which row 32 and section 4.7 listed as not verified, work). `doveadm who` in the `dovecot` container reaches the master; `doveadm kick mulate` for an account without a session answers `0 connections kicked`, exit 0. **Not verified:** that `doveadm kick` ends an open IMAP session. Three runs, one node, no load. |
| 42 | Sender binding, the null sender and the revoked alias domain on a `MailServer` with `MailUser`s (3b) | the same e2e run: an account with the aliases `mailuser@e2etwo.example.org` and `sales@e2etwo.example.org` in a second `MailDomain`; `swaks` through 587 with `--quit-after RCPT` | Postfix judges the sender when it sees the first recipient (`smtpd_delay_reject`), so `MAIL FROM` is answered `250` and the refusal comes with `RCPT TO`: `553 5.7.1` for another account's address, an address nobody owns and a foreign domain, `554 5.7.1` for `MAIL FROM:<>`; a message from each of the three own addresses reached the Mailpit sink. Mail to the three addresses was delivered into one mailbox, once each, read over IMAP by name and by primary address and found in no other mailbox. A disabled account was refused over SASL and IMAP, still received mail, and read it after it was enabled again. Deleting the `MailDomain` of the aliases made them `554 5.7.1` and `553 5.7.1` as senders while the account kept its primary address and its login, also while Postfix was fail closed and the narrowed revision still validated (checked in the last two runs; the live files come from the narrowed render only). The whole suite (50 specs, the MailUser block adds nine) took 1240 s, 1253 s and 1124 s. |
| 43 | What the API server counts against the 1 MiB of a Secret and a ConfigMap, and the margin of the byte guard (3b, the limits) | envtest with the `kube-apiserver` 1.37.0 of `make test`, spec "the size limit of the API server" in `internal/controller/mailserver_limits_test.go`: a Secret and a ConfigMap with one key `k` and a value of 999,998, 1,048,576 and 1,048,577 bytes | A Secret of 999,999 bytes (key and value, `addresses.ObjectSize`) is accepted. A value of exactly 1,048,576 bytes is accepted, a value of 1,048,577 bytes is refused: `Secret "over" is invalid: data: Too long: may not be more than 1048576 bytes`; the ConfigMap behaves the same. **The API server counts the values only, not the keys** (a one-byte key does not move the limit), so the guard of 1,000,000 bytes over keys and values is the conservative reading, with a margin of 48,576 bytes; the 1 MiB limit is not reached by anything the guard lets through. |
| 44 | A limit blocks a revision, and what still applies (3b, the limits) | envtest, specs "MailServer limits" in `internal/controller/mailserver_limits_test.go`, the caps lowered through `MailServerReconciler.Limits`; every step after the first block runs in a reconciler that has seen nothing before (nothing is kept in memory) | Over the cap of 3 accounts (5 given) and over the cap of 5 addresses (6 given), and over the byte guard (the live Secret just too large for one account more): `Ready=False` with `UserLimitExceeded`, `AddressLimitExceeded` and `ConfigTooLarge`, the message with the counts, or the live Secret and its size; the pod-template hash of Postfix and the Postfix tables are those of the running revision, no validation Job is created, the new accounts and aliases are in no file; a Warning Event is emitted once. A changed provided password replaces the hash in the passdb file and leaves the userdb file byte-identical; a disabled account loses its passdb lines and keeps its userdb lines and its Postfix entries; a deleted account loses its login and keeps its mailbox lines. When the count falls to the cap the next reconcile applies the whole revision (the new account in the Postfix tables, the removed one gone from userdb). The byte guard does not send the oversized Secret: its `resourceVersion` is unchanged. A first revision that a limit blocks creates no live Secret. No condition or Event holds an address, a domain or a hash. |
| 45 | Rspamd check 2 on a real `MailServer` with `MailUser`s: which login `task:get_user()` sees, and the refusal (3b, the header `From` binding) | the full e2e suite on kind v0.33.0, 51 of 51 specs in 1,224 s, spec "binds the header From to the login": `swaks` through 587 with the envelope sender the account owns and `--h-From` set | A login typed as the account name and a login typed as the primary address both pass with the owned header `From` and the message in the Mailpit sink carries a `DKIM-Signature` of the domain. So the name that Rspamd receives in `{auth_authen}` is the account name that Dovecot reports (`user=`), not the typed login, the same one that Postfix compares for `sender_login`; the guard lower-cases it anyway. Another account's address in the same domain and an address nobody owns are answered `554 5.7.1 From address is not owned by the login` and nothing reaches the sink. The directory specs of the suite (DKIM, header `From` of another domain, check 2 off) and the other MailUser specs passed unchanged. The e2e installed cert-manager from its chart because the release download was unavailable. |
| 46 | The guard matrix with check 2 (3b) | `make test-rspamd-guard` against `rspamd/rspamd:4.2.3@sha256:47e00f62105f8a60009a30a590ebcf3bfd0b992d45468222d34afa5ca2103690`, rendered configuration and table of the production renderer (`hack/render-rspamd-fixture`), `rspamc --user` with header `From` variants | 70 checks, 0 failed (29 new ones for check 2 and the 41 of check 1 and signing that were there). Owned address, owned alias, a case variant of the address (`Alice@DUAL.Example`), a display name with a comma, and the login in other case pass and are signed; another account's address, an address nobody owns, a login that owns another address and a login that is no account give `reject` with `From address is not owned by the login`; unauthenticated mail with such a `From` gives no action and loopback mail is signed. The directory variant (`owners_mode` `"off"`, no table) passes the address of another account and still refuses an unserved domain. The variant without accounts (mode `"on"`, empty table) refuses every address of a login. A table that is missing, unreadable (mode 000) or has a line of three words defers the mail of logins (`451`, `DKIM signing unavailable`), is logged as `CNMS DKIM guard failed`, and leaves unauthenticated mail untouched and loopback mail signed |




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

**Not verified:** the memory of Argon2id under load measured with the cgroup
(`memory.peak`), with IMAP logins and with mail delivery running (row 40 used
`docker stats`, `doveadm auth test` and no delivery; that the passwd-file
passdb uses a worker, and the spelling of the limit, are row 33); the
`nologin` alternative on 2.4.5, including whether it stops SASL; the use of
`doveadm kick` in the Dovecot pod (row 41 only shows that `doveadm who` and
`kick` reach the master; that `kick` ends an open IMAP session is not
verified); Dovecot's start with LDAP down; `unionmap` errors inside a daemon
(only `postmap` was run); the LDAP guards `(!(<login>=*/*))` and
`(!(<login>=sa.*))` against an attribute **without** a SUBSTR rule (rows 22
and 23 ran them on `uid`, which has one), and a multi-valued `mail`
attribute; the `ldap:` and `pgsql:` tables of the Postfix pod as the
manifests start them (rows 21 to 23 used the image entrypoint with the
rendered files, not the StatefulSet), the `PGSSLMODE` and `PGSSLROOTCERT` of
the pod environment through the master in a pod, `LDAPS` and `StartTLS`
against a CA that is a public one, and a `pgsql` or `ldap` table through
`proxy:`; a `+` in a local part; the SMTP behaviour of Dovecot's
`auth_username_chars` for LMTP recipients; several `passdb`s with a file
first and a directory second; whether error texts of Dovecot, `pgx` or
`go-ldap` can contain credentials; the `Sender:` header (not bound, not
looked at); the Postfix `-DHAS_PGSQL` build for arm64 and on the published
`ghcr.io` image (row 19 is a local amd64 build; the multi-arch build shows in
CI).
