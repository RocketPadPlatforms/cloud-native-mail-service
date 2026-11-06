# Domain authorization

A `MailDomain` makes CNMS act for a domain with elevated privileges: it
generates DKIM keys, requests TLS certificates, writes DNS records,
publishes an MTA-STS policy, and signs outgoing mail. On a multi-tenant
cluster any tenant could therefore claim any domain — including one that
belongs to another tenant — and damage its reputation or spoof it. CNMS
authorizes domains with `MailDomainGrant`s.

:::note
API version `v1alpha1`. The `MailServer` controller evaluates the grants
and enforces the result on the data plane, and the `MailDomain`
controller evaluates the same rule for the domain itself: only an
authorized `MailDomain` gets DKIM keys and DNS records, and the data plane
signs only for the authorized domains of a server. Derived-name
conflicts are not detected yet (marked below).
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
  two labels. A public suffix such as `co.uk` is never authorized, even
  when a grant names it: the `MailDomain` gets `Authorized=False` and every
  `MailServer` leaves it out (reason `PublicSuffix` in both).
- **`includeSubdomains`** grants subdomains at any depth, HSTS-style. The
  apex is always covered; subdomains only when asked. A more specific
  grant (e.g. for `mail.example.org`) takes precedence over a
  subdomain grant for `example.org`.
- **No wildcard syntax.** There is no `*.example.org`; there is only the
  domain plus the boolean.

## How authorization is enforced

The operator is the authoritative check, and it is fail closed: it
re-evaluates grants on every reconcile. A `MailServer` only serves a
`MailDomain` that references it in `spec.serverRef` (same namespace) when:

- a grant covers the domain and names the `MailDomain`'s own namespace
  (`DomainsAuthorized=True, reason=Authorized`), or
- the operator runs with `--domain-authorization=unrestricted`
  (single-tenant clusters; `DomainsAuthorized=True,
  reason=AuthorizationDisabled`).

Otherwise the domain is inactive: it is left out of the Postfix
configuration. The condition
explains why (`NotGranted`, `DuplicateDomain`, `PublicSuffix`; see
the [status conditions](../reference/status-conditions.md)), and
`kubectl get maildomaingrants` shows who owns a domain. The `MailDomain`
shows the same verdict the `MailServer` controller reaches in its own
`Authorized` condition (they share one check), and an inactive domain gets
no DKIM key, no DNS records and no MTA-STS report.

Because the check is a pure function of the current grants and the
claimed `MailDomain`s:

- **narrowing a grant revokes access** — there is no cached decision;
- **creation order never matters** — restores and re-apply cannot move a
  domain to a different tenant;
- **duplicates fail closed** — if the owning namespace claims the same
  domain more than once, every such claimant is blocked until the
  misconfiguration is fixed. A claim from a namespace without the grant
  is unauthorized on its own and never blocks the owner.

### The hostname of a server

The name a server presents (`MailServer.spec.hostname`: HELO, the
certificate, the target of the MX records and the `mx` host of the
MTA-STS policies) is authorized the same way, through the same grants
([ADR 0014](../decisions/0014-domain-server-binding-and-hostname.md)):

- a grant covers the hostname and names the namespace of the server
  (`mail.example.org` is covered by a grant of its own, or by
  `example.org` with `includeSubdomains`), or the operator runs with
  `--domain-authorization=unrestricted`;
- it is not a public suffix and not a cluster-internal name (`cluster.local`
  or below it);
- no other `MailServer` among the competing ones uses it: the servers of
  the namespace that owns the hostname in allowlist mode, all servers in
  unrestricted mode. Both servers are blocked until one of them gives it
  up (`DuplicateHostname`).

The hostname may equal the apex of a `MailDomain`. A hostname that is not
authorized is **not** a reason to stop serving: the verdict is the
`HostnameAuthorized` condition and is independent of the domains. The
server then presents the in-cluster name of its Postfix Service (HELO and
certificate), keeps serving the domains that are authorized, never lists
the hostname in an MX record or a policy, and is `Ready=False` with the
reason of the failure until the grant is fixed.

### Derived names (not detected yet)

A domain also owns the names it writes: `mta-sts.<domain>`,
`_mta-sts.<domain>`, `_smtp._tls.<domain>`, `_dmarc.<domain>`,
`<selector>._domainkey.<domain>`, and SPF/MX at the domain. A `MailDomain`
whose own name collides with another active domain's derived names (for
example `mta-sts.example.org` while `example.org` is active elsewhere)
gets a `DerivedNameConflict`; the more specific name wins.

### Revocation

When a grant stops covering a domain, the `MailServer` stops accepting
mail and submission for it with its next validated rollout, as described
below. The same path applies when a domain leaves the server for another
reason: its `MailDomain` is deleted, or `spec.serverRef` points at another
server ([ADR 0014](../decisions/0014-domain-server-binding-and-hostname.md);
see [moving a domain](../how-to/dkim-keys-and-dns-records.mdx#move-a-domain-to-another-server)). The `MailDomain` controller stops listing records at once and
removes the `DNSEndpoint` after a grace period of 15 minutes, counted
from the `Authorized` transition (so a brief GitOps prune-and-recreate
does not delete DKIM records worldwide, and a restore, which has no
status, only restarts the timer). It keeps the DKIM Secrets, so
re-granting the domain does not rotate the key.

For a `MailServer` this means:

- Every domain is evaluated on its own. The authorized subset is
  rendered; the others are left out of the Postfix configuration (the
  certificate lists no domains). `DomainsAuthorized` is `False` with reason
  `PartiallyAuthorized` and a message naming each blocked domain, and a
  Warning Event is emitted. A blocked domain is still served until the new
  configuration is validated and rolled out, so `Ready` shows the
  validation or rollout state first; afterwards it stays `False` with
  `PartiallyAuthorized` until the whole spec is realised, even though the
  authorized domains keep working.
- If no domain is authorized any longer, or no `MailDomain` references
  the server any more (`Ready` reason `NoDomains`), a running server is
  re-rendered fail closed: Postfix rejects all recipients (a permanent
  `554 5.7.1 Relay access denied`, so senders get a bounce, not a retry)
  and submission is off. The certificate keeps the authorized hostname (or the
  in-cluster Service name without one). A server that never had a data
  plane gets none.
- A hostname that is revoked or renamed never takes the domains down,
  whether or not the server is blocked for another reason. The
  certificate is re-applied for the new name at once, because the old
  one must not keep being renewed (also while the directory, the users
  Secret or the issuer is missing). For a refused `ClusterIssuer` the
  operator deletes the `Certificate` instead, since the shared issuer
  would keep renewing a name the namespace no longer owns; the pods keep
  the Secret they mounted. The running pods keep serving their domains, and
  keep presenting the old name in HELO and TLS, until the new
  configuration is validated and rolled out. Only a revoked domain
  triggers the fail-closed configuration.
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
  the Phase 3 binding lands. The data plane no longer signs for the revoked
  domain (its keys are removed from the Rspamd configuration and the key
  Secrets are kept), and the guard rejects authenticated mail with a
  header `From` in that domain with `554 5.7.1`, because the domain is no
  longer served ([ADR 0013](../decisions/0013-never-unsigned-guard.md),
  [ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md)).

## Wiring-level bypasses

Policy engines are optional. The integrations are meant to be wired so
that tenants cannot bypass CNMS even when they can create the
underlying objects. Only the ClusterIssuer rule is implemented; the
rest is guidance for the integrations:

- the operator writes the `DNSEndpoint` of a `MailDomain` into the
  namespace of the `MailDomain`. external-dns runs as a dedicated
  instance with a tight `--domain-filter` and its own `--txt-owner-id`,
  and tenants have no RBAC on `dnsendpoints`: a tenant who can create one
  in a namespace the instance watches bypasses the grants.
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
