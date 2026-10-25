# ADR 0003: Security defaults and domain ownership

- **Status:** Accepted (2026-10-25)
- **Supersedes:** ADR 0002, item 4 (submission test users)

## Context

CNMS is multi-tenant: one namespace per tenant, and the operator performs
privileged actions for every domain a tenant claims: it generates DKIM
keys, requests TLS certificates (possibly via a shared `ClusterIssuer`),
writes DNS records (possibly via a shared external-dns instance), publishes
an MTA-STS policy, and signs outgoing mail. Without authorization, any
tenant could claim any domain:

- overwrite or hijack DNS records in a shared zone,
- obtain certificates from a shared `ClusterIssuer`,
- send mail as that domain, damaging its reputation and enabling spoofing
  (with shared egress IPs, the domain's SPF record may even authorize the
  cluster's IP pool),
- and two tenants could claim the same domain at once.

Requirements driving the decision:

- **Secure by default, fail closed.** An unauthorized domain gets no
  certificate, no DKIM key, no DNS records, no MTA-STS policy, no signing.
  Conditions explain why.
- **Optional integrations degrade gracefully.** cert-manager features,
  external-dns, Kyverno, and admission policies must not be required for
  core security.
- **State rule.** Custom resources are restored from GitOps without status,
  so authorization must not depend on status or on creation order (creation
  timestamps change after a restore). "The older object wins" is therefore
  not an option: it can move a domain to the wrong tenant silently.
- **Keep the public API small.**

Considered and rejected as the source of truth:

- **Namespace annotation** (`cnms.rpp.gmbh/allowed-domains`): untyped, no
  status, hard to audit (scan every namespace), tamper-resistant only if the
  namespace provisioning tool cooperates, and keyed by namespace, so two
  namespaces can be granted the same domain.
- **Capsule**: good complementary layer (namespace prefix per tenant), but
  an optional integration.
- **cert-manager approver-policy**: covers certificates only, not DKIM keys,
  DNS records, or signing.
- **Admission policy as the check**: fails open when absent, only checks at
  admission time (cannot revoke after the allowlist changes), and cross
  namespace uniqueness is race-prone there. The operator performs the
  privileged actions, so it enforces the check.
- **DNS TXT proof of control in v1**: circular in `externalDNS` mode (the
  operator would publish the token through the shared external-dns, proving
  control to itself).

## Decision

### Domain grants (`MailDomainGrant`)

Domain authorization is a cluster-scoped, admin-owned CRD: `MailDomainGrant`.
The object name is the domain itself; the spec names the tenant:

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: example.org        # the domain itself
spec:
  namespace: tenant-a
  includeSubdomains: false
```

- The API server guarantees **one grant per exact domain** (object name
  uniqueness) and normalizes names (DNS-1123: lowercase, no trailing dot, no
  underscores, 253 max). A CEL rule on the name requires at least two
  labels; the controller additionally rejects public suffixes (Phase 5,
  `golang.org/x/net/publicsuffix`, BSD-3-Clause).
- `spec.namespace` is an explicit namespace name, not a label selector
  (tenants can label their own namespaces).
- `spec.includeSubdomains` (default `false`) also grants subdomains at any
  depth, HSTS-style: the apex is always included, subdomains only when
  asked. No glob syntax; `*.` never appears.
- Grants get their own RBAC (admin role), separate from tenant permissions;
  tenants get **read-only** access so they can self-diagnose
  `NotGranted` conditions.
- The operator warns (condition + Event) when a grant points at a namespace
  that does not exist (Phase 2).
- Residual tamper risk: namespace name reuse. If `tenant-a` is deleted and
  re-created by someone else, the grant follows the name. Namespace names
  must be admin-controlled (e.g. enforced by prefix, as Capsule does).
  Binding to a namespace UID is impossible: UIDs change on restore.

Why a CRD instead of an annotation: typed and CEL-validated (typos are
rejected at apply time), status-capable, auditable with
`kubectl get maildomaingrants`, GitOps-native, and it matches the existing
admin-resource pattern (`MailIPPoolClass`).

### Authorization evaluation

Authorization is a **pure function of the current grants and the claimed
`MailDomain`s**, re-evaluated on every reconcile:

- narrowing a grant revokes access (no stale state, survives restores),
- there is no "older wins" tie-breaking anywhere.

Rules:

- **Owner:** the most specific matching grant owns the domain. A grant
  matches when its name equals the domain, or when it covers the domain as
  a subdomain (`includeSubdomains`). The longest matching grant wins, so a
  grant for `sub.example.org` carves it out of a grant for
  `example.org` with `includeSubdomains: true` (DNS-delegation semantics).
- A claim from any other namespace is simply **unauthorized**
  (`Authorized=False, reason=NotGranted`); another tenant cannot knock out
  the real owner by creating a competing claim.
- **Duplicates** (the same domain claimed by more than one `MailDomain`,
  in one or across namespaces) block **every** claimant. This is a tenant
  misconfiguration; failing closed beats an arbitrary winner.
- **Unrestricted mode:** the operator flag
  `--domain-authorization=allowlist|unrestricted` (default `allowlist`)
  disables the grant requirement for single-tenant clusters. It logs a
  warning at startup, still sets the condition
  (`Authorized=True, reason=AuthorizationDisabled`) so the mode is visible
  on every object, and **still enforces uniqueness**.

**Derived names.** A domain owns all the names it writes:
`mta-sts.<X>`, `_mta-sts.<X>`, `_smtp._tls.<X>`, `_dmarc.<X>`,
`<selector>._domainkey.<X>`, and SPF/MX at `<X>`. All of them are implied by
X's grant. If a `MailDomain`'s own name collides with another active
domain's derived names (e.g. `mta-sts.example.org` claimed while
`example.org` is active), the **more specific name wins** and the other
domain gets a `DerivedNameConflict` condition; this holds even with a
carve-out grant for the subdomain.

**Normalization.** Grant names (DNS-1123 + two-label CEL) and
`MailDomain.spec.domain` (A-labels, two labels min, 253 max) use the same
rules, so `EXAMPLE.org.` and Unicode spellings cannot slip past both the
authorization match and the uniqueness check.

### TXT proof of control (deferred)

A grant means the admin vouches for the domain. A DNS TXT proof is for
self-service onboarding (tenants add domains without an admin) and is
deferred to the backlog as an additive grant field
(`verification.dnsTXT: Required`). If built later: generate the DKIM key
once the domain is authorized, gate signing/certificates/MTA-STS on
verification; derive the token deterministically (e.g. HMAC of an operator
key over domain and namespace) so it survives a restore; cache results in
status (re-verification is a cheap lookup); revoke only after repeated
clear "record absent" answers, never on SERVFAIL or timeouts.

### Revocation

When a grant is deleted or no longer covers a domain:

- **immediately**: stop signing and stop accepting submission for the
  domain (fail closed);
- **after a 15-minute grace period**: delete the `DNSEndpoint` (external-dns
  then removes the records) and the `Certificate`. A GitOps sync that
  briefly prunes and re-creates a grant must not delete DKIM records
  worldwide; stopping signing immediately already keeps the domain closed.
  The grace timer lives in status; a restore restarts it, which is the safe
  direction. Not configurable in v1.
- **kept**: the DKIM `Secret`, so a domain that is granted again does not
  rotate its key.

### Bypasses are closed at the wiring level

Tenants who can create `DNSEndpoint` or `Certificate` objects directly
bypass CNMS entirely; that is stopped by how the integrations are wired,
not by policy engines:

- **DNSEndpoints:** the operator writes them only into its own namespace
  (e.g. `cnms-system`). A dedicated external-dns instance runs with
  `--namespace=cnms-system`, a tight `--domain-filter`, and its own
  `--txt-owner-id`. `DNSEndpoint`s that tenants create in their own
  namespaces are ignored by that instance. Cross-namespace owner references
  are not allowed, so CNMS cleans up through finalizers and labels.
- **ClusterIssuers:** a tenant's own namespaced `Issuer` is their business.
  A shared `ClusterIssuer` is allowed only through a future grant field
  (`allowedClusterIssuers`); clusters where tenants can create
  `Certificate`s themselves should additionally run cert-manager
  approver-policy.
- **Multi-domain certificates:** if one of several domains on a
  `MailServer` is not authorized, that domain is left out of the
  certificate's SANs instead of failing the whole certificate.
- **Apex records:** external-dns manages a name/type as a whole record set.
  CNMS detects apex `TXT` records it does not manage (SPF collision) and
  reports a condition instead of fighting over the record set (Phase 5).

### Layering

- The **controller is the authoritative, fail-closed check**: it re-checks
  on every reconcile, handles cross-namespace uniqueness, and revokes on
  change. Admission layers cannot replace it.
- **No built-in ValidatingAdmissionPolicy.** `paramRef` with a selector
  evaluates the policy against every matching param and requires all to
  pass; CNMS needs "any grant matches", the opposite. Maintaining a weaker
  second source of truth is avoided.
- A **documented Kyverno sample** gives early rejection at admission time
  and can enforce rules CNMS cannot (tenants creating `DNSEndpoint`s
  directly, `ClusterIssuer` use). It is optional and fails open when
  absent.

### Directory TLS defaults

Directory connections are encrypted and verified by default:

- **LDAP:** `tls.mode` defaults to `StartTLS` (port 389); `LDAPS` connects
  directly over TLS and the operator derives port 636; `None` disables
  transport security and is rejected by CEL when `bindDN` is set
  (cleartext bind). `tls.caSecretRef` carries the CA bundle for
  verification.
- **SQL (PostgreSQL):** `tls.mode` defaults to `verify-full`; `require`
  encrypts without hostname verification; `disable` is explicit cleartext.

### Test users

`submission.staticUsers` is renamed to `submission.insecureStaticUsers`:
the Secret holds password hashes (never plaintext), and the operator only
accepts the field when started with `--enable-insecure-test-features`
(Phase 2). Supersedes ADR 0002 item 4.

## Consequences

- **Easier:** one owner per domain is structural (object name uniqueness)
  and survives any restore; `kubectl get maildomaingrants` is the audit
  command; revocation works because authorization is recomputed, not
  cached; the grant is a plain GitOps object.
- **Harder:** admins must create a grant per domain (cheap, and it catches
  typos); the Phase 2/5 controllers must evaluate a cross-object function
  (grants + all claimed `MailDomain`s); one more startup flag.
- **How we know it worked:** envtest pins the grant schema, the name rule,
  and the TLS defaults; Phase 2 shows that a `MailServer` with an
  unauthorized domain is not Ready and renders/signs only authorized
  domains; Phase 5 shows uniqueness, derived-name conflicts, and revocation
  behavior end to end.
- **Assumptions and limits:** the 15-minute revocation grace period is a
  starting value; `golang.org/x/net/publicsuffix` is license-checked when
  added in Phase 5; the Kyverno sample is documented before it is
  validated against a real cluster; namespace names are assumed
  admin-controlled.
