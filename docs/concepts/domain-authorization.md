# Domain authorization

A `MailDomain` makes CNMS act for a domain with elevated privileges: it
generates DKIM keys, requests TLS certificates, writes DNS records,
publishes an MTA-STS policy, and signs outgoing mail. On a multi-tenant
cluster any tenant could therefore claim any domain — including one that
belongs to another tenant — and damage its reputation or spoof it. CNMS
authorizes domains with `MailDomainGrant`s.

:::note
API version `v1alpha1`. Today the `MailServer` controller evaluates the
grants and enforces the result on the data plane. `MailDomain` has no
controller yet, so everything that acts on a domain through it (DKIM
keys, DNS records, the MTA-STS report, derived-name conflicts, the
revocation grace period) arrives with Phase 5 and is marked as such
below.
:::

## The grant model

A `MailDomainGrant` is a cluster-scoped resource owned by the platform
admin (in GitOps, like other admin resources). Its name is the domain, and
its spec names the tenant namespace:

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDomainGrant
metadata:
  name: example.org        # the domain itself
spec:
  namespace: tenant-a      # the tenant allowed to use it
  includeSubdomains: false # also grant *.example.org (default: no)
```

- **One grant per exact domain.** The object name is the domain, so the
  API server guarantees a single owner per domain and normalizes the name
  (lowercase, no trailing dot, no underscores). A grant must have at least
  two labels. Rejecting public suffixes such as `co.uk` is planned for
  Phase 5; until then the admin must not grant one.
- **`includeSubdomains`** grants subdomains at any depth, HSTS-style. The
  apex is always covered; subdomains only when asked. A more specific
  grant (e.g. for `mail.example.org`) takes precedence over a
  subdomain grant for `example.org`.
- **No wildcard syntax.** There is no `*.example.org`; there is only the
  domain plus the boolean.

## How authorization is enforced

The operator is the authoritative check, and it is fail closed: it
re-evaluates grants on every reconcile. A `MailServer` only serves a
domain of its `spec.domains` when:

- a grant covers the domain and names the `MailDomain`'s own namespace
  (`DomainsAuthorized=True, reason=Authorized`), or
- the operator runs with `--domain-authorization=unrestricted`
  (single-tenant clusters; `DomainsAuthorized=True,
  reason=AuthorizationDisabled`).

Otherwise the domain is inactive: it is left out of the Postfix
configuration and of the certificate's names. The condition
explains why (`NotGranted`, `DuplicateDomain`, `DomainNotFound`; see
the [status conditions](../reference/status-conditions.md)), and
`kubectl get maildomaingrants` shows who owns a domain. Once the
`MailDomain` controller exists (Phase 5), an inactive domain also gets no
DKIM key, no DNS records and no MTA-STS report.

Because the check is a pure function of the current grants and the
claimed `MailDomain`s:

- **narrowing a grant revokes access** — there is no cached decision;
- **creation order never matters** — restores and re-apply cannot move a
  domain to a different tenant;
- **duplicates fail closed** — if the owning namespace claims the same
  domain more than once, every such claimant is blocked until the
  misconfiguration is fixed. A claim from a namespace without the grant
  is unauthorized on its own and never blocks the owner.

### Derived names (Phase 5)

A domain also owns the names it writes: `mta-sts.<domain>`,
`_mta-sts.<domain>`, `_smtp._tls.<domain>`, `_dmarc.<domain>`,
`<selector>._domainkey.<domain>`, and SPF/MX at the domain. A `MailDomain`
whose own name collides with another active domain's derived names (for
example `mta-sts.example.org` while `example.org` is active elsewhere)
gets a `DerivedNameConflict`; the more specific name wins.

### Revocation

When a grant stops covering a domain, the `MailServer` stops accepting
mail and submission for it with its next validated rollout, as described
below. With the `MailDomain` controller (Phase 5) the operator also
removes the DNS records after a short grace period (15 minutes, so a
brief GitOps prune-and-recreate does not delete DKIM records worldwide)
and keeps the DKIM Secret, so re-granting the domain does not rotate the
key. Neither exists today.

For a `MailServer` this means:

- Every domain is evaluated on its own. The authorized subset is
  rendered; the others are left out of the Postfix configuration and the
  certificate's names. `DomainsAuthorized` is `False` with reason
  `PartiallyAuthorized` and a message naming each blocked domain, and a
  Warning Event is emitted. A blocked domain is still served until the new
  configuration is validated and rolled out, so `Ready` shows the
  validation or rollout state first; afterwards it stays `False` with
  `PartiallyAuthorized` until the whole spec is realised, even though the
  authorized domains keep working.
- If no domain is authorized any longer, a running server is re-rendered
  fail closed: Postfix rejects all recipients, submission is off, and the
  certificate keeps only the in-cluster Service name. A server that never
  had a data plane gets none.
- The new configuration goes through the usual [validation
  gate](../decisions/0009-config-validation-gate.md): until its Job
  passes, the previous configuration keeps running. A revoked domain is
  the exception: if the new configuration cannot be applied (it is still
  validating or was rejected, or the directory, the users Secret or the
  issuer is missing), a running server that still serves a revoked domain
  gets the fail-closed configuration instead, so the remaining
  domains of that server stop too until the problem is fixed.
- Sender-address binding on submission is not enforced yet (backlog), so a
  login that still exists can use a revoked domain as sender address until
  the Phase 3 binding lands; there is no DKIM signing before Phase 5.

## Wiring-level bypasses

Policy engines are optional. The integrations are meant to be wired so
that tenants cannot bypass CNMS even when they can create the
underlying objects. Only the ClusterIssuer rule is implemented; the
rest is guidance for the Phase 5 integrations:

- external-dns runs as a dedicated instance that only reads
  `DNSEndpoint`s from the operator's own namespace
  (`--namespace=cnms-system`, a tight `--domain-filter`, its own
  `--txt-owner-id`); tenant-created `DNSEndpoint`s elsewhere are ignored.
- shared `ClusterIssuer`s are refused unless the operator runs with
  `--allow-cluster-issuers` (a per-grant allow-list is on the backlog);
  tenant-created `Certificate`s are best contained with
  cert-manager approver-policy.
- a Kyverno sample (not shipped yet) can give early rejection at admission time
  and enforce rules the operator cannot (e.g. tenants creating
  `DNSEndpoint`s directly). It is a convenience layer, not the security
  boundary.

## Delegation caveat

A tenant granted `sub.example.org` still falls under the parent domain's
DMARC policy (`sp=` tag) unless it publishes its own `_dmarc` record. The
parent owner's choices affect the subdomain tenant's deliverability.

See [ADR 0003](../decisions/0003-security-defaults-and-domain-ownership.md)
for the full decision record, and the [API reference](../reference/api.md)
for the `MailDomainGrant` schema.
