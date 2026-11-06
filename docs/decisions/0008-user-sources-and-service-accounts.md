# ADR 0008: Operator-managed users and service accounts

- **Status:** Accepted, amended 2026-11-03 (tenant isolation, see
  "Amendment" below), partially amended by ADR 0015
- **Date:** 2026-10-25

## Context

Core principle 2 keeps users outside the operator: accounts come from an
external directory (`MailDirectory`: LDAP and SQL). Two gaps remain:

- **No directory service.** Environments without LDAP or SQL have no
  first-class way to provide accounts. The only static source today is
  `spec.submission.insecureStaticUsers` (a Secret with password hashes,
  accepted only under `--enable-insecure-test-features`), which is a Phase 2
  test mechanism, not a production path.
- **Application senders.** Applications that send mail through a
  `MailServer`'s submission port must currently provision their own users in
  the external directory. We want operator-managed per-application
  credentials, including for applications that run in a **different
  namespace** than the `MailServer`.

Both are user-source features on the same seam: Dovecot's authentication
passdb and Postfix's sender/recipient maps. `MailServer.spec.directoryRef`
is required today, so a directory-less server cannot be Ready at all.

## Decision

1. **Add a namespaced `MailUser` CRD (v1alpha1)** for operator-managed
   accounts in environments without a directory service.
   - `metadata.name` is the login name (DNS-label pattern), so per-namespace
     uniqueness is free; `spec.serverRef` references a `MailServer` in the
     same namespace. (Amended by ADR 0015: the pattern is a DNS-1123
     subdomain, so a login can hold a dot, and `spec.domainRef` and
     `spec.aliases` give the addresses.)
   - The password is operator-generated (high entropy, `crypto/rand`,
     stored only in a per-user Secret owned by the `MailUser`) or
     user-provided via a Secret reference; optional `displayName`,
     `status` (active/disabled) and a per-user `quota` override. (Amended
     by ADR 0015: the status is a `disabled` flag that keeps inbound
     delivery, the quota arrives with Phase 4, and the password is hashed
     with Argon2id.)
   - The operator renders all `MailUser`s of a server into the same static
     passdb and allowed-sender maps that the `insecureStaticUsers` mechanism
     already produces; rendering reuses that path. (Amended by ADR 0015:
     the Dovecot passdb and userdb files are keys of a stable-named, mutable
     Secret outside the content hash, an exception to ADR 0009, so a user
     change needs no Dovecot restart; the Postfix maps stay immutable
     revisions.)
   - `MailServer.spec.directoryRef` becomes optional. Exactly one user
     source must resolve — a directory **or** at least one `MailUser` —
     enforced as a `Ready` condition (CEL cannot count across objects),
     mirroring the existing `DirectoryNotFound` reason. (Amended by ADR
     0015: a directory together with `MailUser`s, or
     `insecureStaticUsers` together with either, is `UserSourceConflict`;
     service accounts add to either source, and a server with only service
     accounts is a send-only server, not `NoUserSource`.)
   - `insecureStaticUsers` stays a documented test-only mechanism.
   - This is a deliberate, documented exception to core principle 2: the
     operator manages the accounts only when the operator is explicitly
     configured to do so (a `MailUser` exists), never by default.
2. **Add a namespaced `MailServiceAccount` CRD (v1alpha1)** that lives in
   the **application's namespace**.
   - `spec.serverRef {name, namespace}` is an explicit cross-namespace
     reference to a `MailServer`; `spec.sender` is the single address the
     application may send as (v1; a list is a later addition).
   - The operator creates a Secret in the application's namespace
     (controller-owned, so same-namespace garbage collection applies) with
     the documented keys `username`, `password` (operator-generated),
     `server` (in-cluster submission FQDN), `tls`
     (`mandatory`/`opportunistic`), `helo` (the server's HELO name;
     amended by ADR 0014: the server hostname, with a `tlsServerName` key
     for the name to verify the certificate against) and
     `ca.crt` (the CA chain of the server's TLS certificate). Everything an
     application needs is in one mounted Secret; the application needs no
     extra RBAC.
   - `ca.crt` is optional-integration (principle 10): it is omitted with a
     status note when cert-manager is not available.
   - The operator also creates a NetworkPolicy in the application's
     namespace (controller-owned) allowing egress to the server's
     submission Service on port 587; without it the port-25-era default
     egress floor would block the application.
   - The credential is rendered into the same passdb as a mailbox-less
     (auth-only) user, so it works with or without `mailboxes.enabled`.
     The allowed-sender map binds the credential to `spec.sender`, so the
     application cannot spoof arbitrary `From` addresses. (Amended by ADR
     0015: this is one case of a strict sender binding for every account,
     the envelope in Postfix and the header `From` in Rspamd, and the
     account name is a reserved form that cannot collide with a user.)
   - When the referenced `MailServer` is deleted, the account reports
     `NotReady` with a clear reason; no finalizer is needed (cross-namespace
     owner references do not garbage-collect).
   - Two CRDs instead of one CRD with a `type` field: the service account
     has materially different ownership semantics (different namespace,
     app-namespace Secret and NetworkPolicy) than a mailbox account.
3. **Fold both into Phase 3**, renamed to "user sources and service
   accounts": `MailDirectory` (LDAP and SQL) plus `MailUser` and
   `MailServiceAccount`, sharing one passdb/Postfix-map rendering seam and
   one e2e fixture set. (Amended by ADR 0015: delivered as three pull
   requests, `MailDirectory`, then `MailUser` with sender binding, then
   `MailServiceAccount`.)

## Amendment: cross-namespace use needs the server's consent

As first written, decision 2 let any namespace obtain sending credentials
for any `MailServer` by naming it in `spec.serverRef`, and did not tie
`spec.sender` to a domain the server may use. That contradicts the tenant
isolation principle ("tenants cannot reference other tenants' resources")
and ADR 0002, decision 1 ("no `namespace` fields"). Nothing is implemented
yet, so this amends the design before Phase 3:

1. **The server opts in.** A new optional field
   `MailServer.spec.serviceAccounts.allowedNamespaces` (a list of namespace
   names, not selectors, because tenants can label their own namespaces)
   names the namespaces whose `MailServiceAccount`s may reference the
   server. The default is empty: only `MailServiceAccount`s in the server's
   own namespace are accepted. The field is edited by whoever may edit the
   `MailServer`, so a tenant cannot grant itself access to another
   tenant's server. A reference from a namespace that is not listed gets
   `Ready=False, reason=NamespaceNotAllowed`; no Secret, no passdb entry
   and no NetworkPolicy are created. Removing a namespace from the list
   revokes its accounts on the next reconcile (the credential Secret is
   deleted and the account is dropped from the passdb), like a revoked
   `MailDomainGrant`. The field is added in Phase 3 and is not implemented
   now. (Amended by ADR 0015: the list holds objects with a `name` and
   optional `senderDomains`, so that a namespace can be limited to some
   of the server's domains and the field can grow without a breaking
   change.)
2. **The sender must belong to an authorized domain.** `spec.sender` must
   be an address in a domain of the referenced server that passes the
   domain authorization of ADR 0003 (the server's authorized subset, not
   every domain it lists). Otherwise the account is
   `Ready=False, reason=SenderNotAuthorized`, and an account whose domain
   is revoked later loses its credential in the same reconcile that drops
   the domain from the server.
3. **Reconciling with ADR 0002, decision 1.** `spec.serverRef.namespace` is
   the single cross-namespace reference in the API. It is a request, not an
   authorization: it takes effect only when the server's namespace has
   consented through `allowedNamespaces` (the same two-sided pattern as
   Gateway API's `ReferenceGrant`). All other references stay
   same-namespace, and no cross-namespace RBAC is granted to tenants: the
   operator does the cross-namespace work. When `serverRef.namespace` is
   omitted it defaults to the account's own namespace, which needs no
   consent.
4. **Watches.** The operator maps `MailServer` changes (including
   `allowedNamespaces` and domain authorization changes) to the accounts
   that reference the server in other namespaces, and account changes to
   the server; this joins the cross-namespace mapping already named in the
   consequences.

## Consequences

- Directory-less environments can run CNMS for submission and mailboxes,
  and applications get operator-provided sending credentials that work from
  their own namespace.
- Making `directoryRef` optional is a schema relaxation, safe in v1alpha1
  (upgrade notes remain mandatory per the docs policy).
- The cross-namespace watch mapping (`MailServer` changes must update the
  status of service accounts in other namespaces) is the fiddliest
  controller part; envtest coverage of it is part of the phase definition
  of done.
- Exposing the CA chain lets applications verify submission TLS against
  cluster-issued certificates; the key is documented as optional.
- Security floor: service-account sender binding prevents `From` spoofing
  (amended by ADR 0015: for every account, and the header `From` of
  authenticated mail must be in a served domain); generated passwords live
  only in Secrets (never in the CR or logs); the per-user and per-account
  Secrets are garbage-collected with their CR.
- How we know it worked: e2e shows a `MailUser` logging in via swaks, an
  application pod in a second namespace sending with the operator-provided
  credential Secret, and the account going `NotReady` when the `MailServer`
  is deleted.
