# ADR 0008: Operator-managed users and service accounts

- **Status:** Accepted
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
     same namespace.
   - The password is operator-generated (high entropy, `crypto/rand`,
     stored only in a per-user Secret owned by the `MailUser`) or
     user-provided via a Secret reference; optional `displayName`,
     `status` (active/disabled) and a per-user `quota` override.
   - The operator renders all `MailUser`s of a server into the same static
     passdb and allowed-sender maps that the `insecureStaticUsers` mechanism
     already produces; rendering reuses that path.
   - `MailServer.spec.directoryRef` becomes optional. Exactly one user
     source must resolve — a directory **or** at least one `MailUser` —
     enforced as a `Ready` condition (CEL cannot count across objects),
     mirroring the existing `DirectoryNotFound` reason.
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
     (`mandatory`/`opportunistic`), `helo` (the server's HELO name) and
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
     application cannot spoof arbitrary `From` addresses.
   - When the referenced `MailServer` is deleted, the account reports
     `NotReady` with a clear reason; no finalizer is needed (cross-namespace
     owner references do not garbage-collect).
   - Two CRDs instead of one CRD with a `type` field: the service account
     has materially different ownership semantics (different namespace,
     app-namespace Secret and NetworkPolicy) than a mailbox account.
3. **Fold both into Phase 3**, renamed to "user sources and service
   accounts": `MailDirectory` (LDAP and SQL) plus `MailUser` and
   `MailServiceAccount`, sharing one passdb/Postfix-map rendering seam and
   one e2e fixture set.

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
- Security floor: service-account sender binding prevents `From` spoofing;
  generated passwords live only in Secrets (never in the CR or logs); the
  per-user and per-account Secrets are garbage-collected with their CR.
- How we know it worked: e2e shows a `MailUser` logging in via swaks, an
  application pod in a second namespace sending with the operator-provided
  credential Secret, and the account going `NotReady` when the `MailServer`
  is deleted.
