# ADR 0014: Domain-to-server binding and server hostname

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

A `MailServer` listed the `MailDomain`s it serves in `spec.domains`
(ADR 0002). That put the binding on the object with the larger blast
radius and the wrong owner: the domain, its keys, its records and its
authorization (ADR 0003, 0011, 0012) belong to the `MailDomain`, and a
tenant who adds a domain has to edit the server as well. It also tied
several things to a list order nobody chose:

- The name the server presents (HELO, certificate SAN, `postmaster`, the
  MTA-STS `mx`) is the first sorted domain it serves
  (`RenderRequest.ServerHostname`). Adding a domain that sorts earlier
  changes the name of a running server, its certificate and the policy of
  every other domain.
- Nothing owns the DNS records of that name: the `A`/`AAAA` record of the
  inbound address, the SPF record of the HELO name, the `MX` target.
- The name is not authorized on its own: it is a domain of the server,
  not a hostname the namespace was granted.

This ADR records both steps of the fix. The binding (part A) is
implemented with this decision. The server hostname (part B) is decided
here and implemented in a follow-up change; every statement in part B
describes that change, not the code of part A.

## Decision

### Part A: `MailDomain.spec.serverRef`

1. **The binding lives on the `MailDomain`.** `spec.serverRef` is a
   required `NameReference` (same namespace, ADR 0002). It is **mutable**
   (see below). `MailServer.spec.domains` is removed; there is no release
   to stay compatible with (upgrade notes list the steps). The domains a
   server serves are the `MailDomain`s of its namespace whose
   `spec.serverRef.name` names it. A domain belongs to one server: a
   second server of the namespace that wants it needs its own domain.

2. **Evaluation is a filter.** The authorization of a server (ADR 0003)
   is still a pure function of grants and claims. It filters the
   cluster-wide claim list the evaluator already loads (namespace and
   `serverRef` match), so the `MailServer` and the `MailDomain`
   controller reach the same verdict and no list in a reconcile uses a
   field selector (an uncached client rejects it, and the cache is not
   needed for a few objects). One field index on `spec.serverRef.name`
   exists for the watch map functions only.

3. **A server that does not exist.** The `MailDomain` still authorizes,
   generates and keeps its keys and reports its records, so DNS can be
   prepared before the server exists. `ServerFound=False` (reason
   `ServerNotFound`) says so; it never feeds into `Ready`. An enabled
   MTA-STS has no policy (`MTASTSReady=False`, reason `NoMailServer`).

4. **A server without domains.** Its `Ready` is `False` with reason
   `NoDomains` and `DomainsAuthorized` is `True` with the same reason
   (nothing is unauthorized, nothing is served). Nothing is created for
   a server that never had a data plane. One that has one is re-rendered
   fail closed (ADR 0003): the last domain leaving is a revocation.

5. **Detaching is revocation.** A domain leaves a server when its
   `serverRef` changes, when the `MailDomain` is deleted, or when its
   grant goes away. All three go through the same path: the server is
   re-rendered without the domain, the certificate loses the name, the
   served-domains annotation drives the fail-closed render when the
   normal one cannot be applied, and signing and submission for the
   domain stop with the new revision. Deleting a `MailDomain` therefore
   stops serving it; the key Secrets stay (ADR 0011).

6. **Visibility.** `MailServer.status.domains` lists the attached
   domains (`name` of the `MailDomain`, `domain`, `served`, `reason`).
   `served` means: authorized and part of the configuration the operator
   renders; the pods follow with `WorkloadsReady`. The list is derived
   and holds no state. It is capped at 128 entries (the cap of the CRD
   field): the domains that are not served come first, then the rest by
   name, so the entries that need attention survive the cut. The
   warning `DomainsListed` is `False` (reason `Truncated`) when the list
   is cut, and never feeds into `Ready`. `MailDomain` gets the printer
   column `Server` and the condition `ServerFound`. The reason
   `DomainNotFound` no longer exists.

7. **Watches.** A `MailDomain` event reaches the server in its
   `serverRef` and, for a changed object, the old one as well (a move
   reconciles both servers); the existing fan-out to the servers of the
   other claimants of the same domain stays. A `MailServer` event
   reaches its `MailDomain`s (indexed), so `ServerFound` and the MTA-STS
   host follow. A domain that joins or leaves also reaches the
   MTA-STS-enabled domains of the server, because the server hostname is
   its first sorted domain until part B.

**Why `serverRef` is mutable.** The rotation state of the DKIM keys is an
annotation on the `MailDomain` (ADR 0011), and the key Secrets and the
`DNSEndpoint` are named after it. An immutable reference would turn a
move into delete and create, which loses the rotation state and the
DNSEndpoint. A move keeps all three.

**Moving a domain between servers** (risks, and the runbook in the
how-to guide):

- The DKIM key Secrets and the `DNSEndpoint` stay with the `MailDomain`;
  the new server mounts the same keys, so signing continues with the same
  selectors.
- Mailboxes and queued mail stay on the old server. Nothing moves them;
  graceful tooling is in the backlog.
- Receivers cache an MTA-STS policy for `max_age` (a week). Publish the
  MX change and the new policy before the old server stops answering.
- Both servers may serve the domain for a short time: the old one until
  its new revision is rolled out, the new one from its own. Inbound mail
  follows the MX record, submission follows the credentials.
- The old server is re-rendered without the domain. If its normal render
  cannot be applied at that moment, the fail-closed render replaces it
  and takes its other domains down until the blocking input is fixed
  (ADR 0003, the cost named there).

### Part B: the server hostname (implemented in a follow-up change)

1. **The field.** `MailServer.spec.hostname` is required and mutable (the
   same shape as `MailDomain.spec.domain`, without its immutability: a
   rename must not mean a new server). It replaces the first sorted
   domain as the name of the server: HELO, certificate name, MTA-STS
   `mx`, `postmaster`.

2. **Authorization.** The hostname is authorized through the grants
   exactly like a domain: the owner of the hostname per the grants must
   be the server's namespace; allowlist and unrestricted modes behave as
   for domains. It is unique cluster-wide among `MailServer`s: two
   servers with the same hostname are both blocked (`DuplicateHostname`).
   A public suffix is not a hostname. The hostname may equal the apex of
   a `MailDomain` (then no second SPF record is reported for it).

3. **Failure is scoped to the hostname.** A hostname that is not
   authorized sets `HostnameNotAuthorized`. The server does **not** fail
   closed for its domains: it falls back to the in-cluster Postfix
   Service FQDN as HELO and certificate name and keeps serving the
   domains that are authorized.

4. **Certificate.** Only the authorized hostname: CN and the single SAN
   are the hostname. The Service FQDN is the name only while no hostname
   is authorized; it is never added next to a hostname, because public
   ACME issuers reject `*.svc.cluster.local`. ACME needs the DNS-01
   challenge or a private CA (documented). In-cluster applications connect
   to the Service and verify the hostname with a `tlsServerName` key in
   their credential (ADR 0008, designed with Phase 3).
   The served names of the revision (the `servedDomains` read from the
   `Certificate`) exclude only the currently authorized hostname and the
   Service FQDN, so a revoked hostname that is still on the certificate is
   detected as served and triggers the fail-closed render.

5. **MTA-STS.** `mx` is the authorized hostname of the referenced server
   only. Without an authorized hostname no policy is published
   (`MTASTSReady=False`, reason `HostnameNotAuthorized`); the Service
   FQDN is never published.

6. **Records, reported and verified, not published.**
   `MailServer.status.records` lists the `A`/`AAAA` records of the
   hostname to the inbound LoadBalancer ingress addresses (none for a
   `ClusterIP` service; reason `LoadBalancerHostname` when the ingress is
   a DNS name) and the SPF `TXT` at the hostname.
   `MailDomain.status.records` gains the `MX` record pointing at the
   hostname. Verification never feeds into `Ready`.

7. **SPF sources.** `MailServer.spec.spf` (`ip4`, `ip6`, validated like
   `MailDomain.spec.spf`) lists the sending sources of the server and is
   **additive**: the SPF of a domain is the server sources plus the
   domain's own `ip4`, `ip6`, `include` and `qualifier`; the SPF of the
   hostname is the server sources.

8. **HELO.** The HELO name is the hostname until a bound
   `MailIPPoolClaim` supplies per-address PTR names (Phase 7, per-pod
   `smtp_helo_name`); the hostname stays the MX target and the TLS name.

### Amendments to earlier ADRs

Each carries a short pointer to this ADR; the text of the decisions
stays.

- **ADR 0002** (resource model): the domain-to-server relation is
  `MailDomain.spec.serverRef`; references stay same-namespace.
- **ADR 0003**: the evaluation filters the claims attached to a server;
  detaching is revocation; part B adds the hostname to the
  authorization and the derived names.
- **ADR 0008**: part B: the `helo` key of the service-account Secret is
  the hostname, and a `tlsServerName` key is added.
- **ADR 0012**: section 3 (HELO identity, MTA-STS hosts): the identity is
  the hostname after part B, not the primary domain.
- **ADR 0013**: section 5 (bounce SPF identity): the same.

## Consequences

- **Easier:** a tenant adds a domain with one object; a domain moves
  without losing its keys; the owner of every record is clear; no hidden
  coupling between list order and the server name once part B is in.
- **Harder:** the server's domains are found by scanning the claim list
  (cheap at this scale, and the same list already feeds the uniqueness
  check); a server shows nothing until a domain attaches; a move has the
  risks above and no tooling yet.
- **Until part B:** the server name is still the first sorted domain of
  the server, with the coupling described in the context.
- **Limits:** `status.domains` holds 128 entries; the cap is revisited in
  the backlog.
