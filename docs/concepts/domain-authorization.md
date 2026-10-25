# Domain authorization

A `MailDomain` makes CNMS act for a domain with elevated privileges: it
generates DKIM keys, requests TLS certificates, writes DNS records,
publishes an MTA-STS policy, and signs outgoing mail. On a multi-tenant
cluster any tenant could therefore claim any domain — including one that
belongs to another tenant — and damage its reputation or spoof it. CNMS
authorizes domains with `MailDomainGrant`s.

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
  two labels; public suffixes like `org` are rejected by the operator.
- **`includeSubdomains`** grants subdomains at any depth, HSTS-style. The
  apex is always covered; subdomains only when asked. A more specific
  grant (e.g. for `mail.example.org`) takes precedence over a
  subdomain grant for `example.org`.
- **No wildcard syntax.** There is no `*.example.org`; there is only the
  domain plus the boolean.

## How authorization is enforced

The operator is the authoritative check, and it is fail closed: it
re-evaluates grants on every reconcile. A `MailDomain` (and the
`MailServer`s that reference it) only works for a domain when:

- a grant covers the domain and names the `MailDomain`'s own namespace
  (`Authorized=True, reason=Granted`), or
- the operator runs with `--domain-authorization=unrestricted`
  (single-tenant clusters; `Authorized=True,
  reason=AuthorizationDisabled`).

Otherwise the domain is inactive: no certificate is requested, no DKIM key
is published, no DNS records are written, no MTA-STS policy is served, and
nothing is signed for it. The condition explains why
(`NotGranted`), and `kubectl get maildomaingrants` shows who owns a
domain.

Because the check is a pure function of the current grants and the
claimed `MailDomain`s:

- **narrowing a grant revokes access** — there is no cached decision;
- **creation order never matters** — restores and re-apply cannot move a
  domain to a different tenant;
- **duplicates fail closed** — if the same domain is claimed more than
  once (in one namespace or across namespaces), every claimant is blocked
  until the misconfiguration is fixed.

### Derived names

A domain also owns the names it writes: `mta-sts.<domain>`,
`_mta-sts.<domain>`, `_smtp._tls.<domain>`, `_dmarc.<domain>`,
`<selector>._domainkey.<domain>`, and SPF/MX at the domain. A `MailDomain`
whose own name collides with another active domain's derived names (for
example `mta-sts.example.org` while `example.org` is active elsewhere)
gets a `DerivedNameConflict`; the more specific name wins.

### Revocation

When a grant stops covering a domain, the operator stops signing and
accepting submission for it immediately, and removes the DNS records and
the certificate after a short grace period (15 minutes, so a brief GitOps
prune-and-recreate does not delete DKIM records worldwide). The DKIM
secret is kept, so re-granting the domain does not rotate the key.

## Wiring-level bypasses

Policy engines are optional; the integrations are wired so that tenants
cannot bypass CNMS even when they can create the underlying objects:

- external-dns runs as a dedicated instance that only reads
  `DNSEndpoint`s from the operator's own namespace
  (`--namespace=cnms-system`, a tight `--domain-filter`, its own
  `--txt-owner-id`); tenant-created `DNSEndpoint`s elsewhere are ignored.
- shared `ClusterIssuer`s are intended to be used only through the grant
  model; tenant-created `Certificate`s are best contained with
  cert-manager approver-policy.
- a documented Kyverno sample can give early rejection at admission time
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
