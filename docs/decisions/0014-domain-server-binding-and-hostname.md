# ADR 0014: Domain-to-server binding and server hostname

- **Status:** Accepted; part A (binding) and part B (hostname) are implemented;
  partially amended by ADR 0015
- **Date:** 2026-11-05

## Context

A `MailServer` listed the `MailDomain`s it serves in `spec.domains`
(ADR 0002). That put the binding on the object with the larger blast
radius and the wrong owner: the domain, its keys, its records and its
authorization (ADR 0003, 0011, 0012) belong to the `MailDomain`, and a
tenant who adds a domain has to edit the server as well. It also tied
several things to a list order nobody chose:

- The name the server presented (HELO, certificate SAN, `postmaster`, the
  MTA-STS `mx`) was the first sorted domain it served
  (`RenderRequest.ServerHostname`). Adding a domain that sorts earlier
  changes the name of a running server, its certificate and the policy of
  every other domain.
- Nothing owns the DNS records of that name: the `A`/`AAAA` record of the
  inbound address, the SPF record of the HELO name, the `MX` target.
- The name is not authorized on its own: it is a domain of the server,
  not a hostname the namespace was granted.

This ADR records both steps of the fix. The binding (part A) and the
server hostname (part B) are both implemented. Part B arrived in a
second change; where its implementation had to settle what the first
text left open, the text below says so (marked "settled in the
implementation").

## Decision

### Part A: `MailDomain.spec.serverRef`

1. **The binding lives on the `MailDomain`.** `spec.serverRef` is a
   required `NameReference` (same namespace, ADR 0002). It is **mutable**
   (see below). `MailServer.spec.domains` is removed; there is no release
   to stay compatible with (upgrade notes list the steps). The domains a
   server serves are the `MailDomain`s of its namespace whose
   `spec.serverRef.name` names it. A domain belongs to one server: a
   second server of the namespace that wants it needs its own domain.
   (Amended by ADR 0015: a `MailUser` names its server in `spec.serverRef`
   too, and its controller checks that the `domainRef` of the primary
   address and of every alias resolves to a `MailDomain` of that same
   server (`DomainNotServed`, `AliasDomainNotServed` for an alias).)

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
   re-rendered without the domain, the served-domains annotation drives
   the fail-closed render when the normal one cannot be applied, and
   signing and submission for the domain stop with the new revision. Deleting a `MailDomain` therefore
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
   host follow. (Part A also mapped a domain that joined or left to the
   MTA-STS-enabled domains of the server, because the server name was its
   first sorted domain. Part B removed that: the name is the hostname.
   Part B adds the watches between the `MailServer`s of one hostname,
   and from a `MailDomain` to the servers whose hostname is its domain.)

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

### Part B: the server hostname

1. **The field.** `MailServer.spec.hostname` is required and mutable (the
   same shape as `MailDomain.spec.domain`, without its immutability: a
   rename must not mean a new server). It replaces the first sorted
   domain as the name of the server: HELO, certificate name, MTA-STS
   `mx`. The pattern of the field, of `MailDomain.spec.domain` and of the
   name of a `MailDomainGrant` is one rule, kept in sync by comment.
   (Settled in the implementation: the renderer has no `postmaster`
   address, so there was nothing to rename there. Bounces and local mail
   carry `myorigin = $myhostname`, so the hostname is also the identity
   of bounces, see ADR 0013.)

2. **Authorization.** The hostname is authorized through the grants
   exactly like a domain: the owner of the hostname per the grants must
   be the server's namespace; allowlist and unrestricted modes behave as
   for domains. It is unique cluster-wide among `MailServer`s: two
   servers with the same hostname are both blocked (`DuplicateHostname`).
   A public suffix is not a hostname, and neither is a cluster-internal
   name (`cluster.local` or below it): that is the name a server falls
   back to without a hostname. The hostname may equal the apex of
   a `MailDomain` (then no second SPF record is reported for it).
   Settled in the implementation: "cluster-wide" means among the servers
   that compete for the name, as for domains (ADR 0003): in allowlist
   mode the servers of the namespace that owns the hostname per the
   grants, in unrestricted mode all of them. A server of a namespace
   without the grant is not authorized anyway and does not block the
   owner.

3. **Failure is scoped to the hostname.** A hostname that is not
   authorized sets the condition `HostnameAuthorized` to `False`, with
   the reason `HostnameNotAuthorized` (no grant for the namespace, a
   public suffix, a cluster-internal name, or no hostname set on a stored
   object; the message says which) or `DuplicateHostname`. The
   server does **not** fail closed for its domains: it falls back to the
   in-cluster Postfix Service FQDN as HELO and certificate name and keeps
   serving the domains that are authorized. Settled in the
   implementation: `HostnameAuthorized` feeds into `Ready` (after
   `DomainsAuthorized`), because a server with a hostname it may not
   present is not ready for what its spec asks, although its data plane
   runs; the verification of records never does (item 6). The failure
   shows in `MailDomain` as `MTASTSReady=False` with the same reason
   `HostnameNotAuthorized` (item 5), also for a duplicate, with the
   message of the failure.

4. **Certificate.** Only the authorized hostname: CN and the single SAN
   are the hostname. The Service FQDN is the name only while no hostname
   is authorized; it is never added next to a hostname, because public
   ACME issuers reject `*.svc.cluster.local`. ACME needs the DNS-01
   challenge or a private CA (documented). In-cluster applications connect
   to the Service and verify the hostname with a `tlsServerName` key in
   their credential (ADR 0008, designed with Phase 3).
   Settled in the implementation: a rename or a revocation of the
   hostname **never** takes the domains down. The workloads record only
   the domains they serve (`served-domains`); the hostname is not
   recorded, and the fail-closed decision looks at domains alone. The
   normal render re-applies the `Certificate` before the validation
   gate, so the certificate lists only the authorized name from then on
   (and cert-manager stops renewing the old one); the running pods keep
   serving their domains, and keep presenting the old name in HELO,
   until the new revision is validated and rolled out. The fail-closed
   render would turn a rename into a window in which Postfix answers
   `554 5.7.1 Relay access denied` for every recipient, so it is not
   applied for a hostname. This holds while the server is blocked for
   another reason (directory or users Secret missing) as well: the old
   data plane still delivers, so the controller re-applies only the
   `Certificate` (for a refused `ClusterIssuer` it deletes the
   `Certificate` the server owns instead, so the shared issuer does not
   keep renewing the name, and the Secret stays mounted; only for a
   server that has a data plane), and the pods change with the next
   validated rollout. The same holds for a stored server upgraded from
   an earlier snapshot, whose `Certificate` lists the domains: its first
   reconcile replaces them and does not fail closed. The fail-closed
   render keeps an authorized hostname: revoking the last domain does
   not revoke the name, and the certificate is not reissued (an ACME
   issuer has rate limits).
   **Accepted exposure:** until the next validated rollout the pods
   present the old hostname in HELO (self-asserted, no secret) and keep
   the certificate Secret they mounted. cert-manager replaces that
   Secret only if the issuer can issue the new name; a public ACME issuer
   cannot issue the Service name that a revoked hostname falls back to,
   so the old Secret stays mounted until it expires. A server that is
   blocked for a long time keeps this window open for as long.
   Not rejected by the cluster-internal rule: `foo.default.svc` does not
   end in `.cluster.local`; in allowlist mode only a granted name passes,
   in unrestricted mode such a name is accepted.

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
   hostname. Verification never feeds into `Ready`. Settled in the
   implementation:
   - The condition `AddressRecords` says why no address record is listed
     (`NoExternalAddress` for a `ClusterIP` Service, `LoadBalancerHostname`,
     `AddressPending` while the Service or its address does not exist);
     `RecordsVerified` is the verification state of the list, with the
     reasons of the `MailDomain` condition of the same name. Both are
     warnings and are dropped while the hostname is not authorized.
   - The SPF record of the hostname ends in `-all`: only the server
     sources send as the hostname (the HELO identity). It is left out when
     the server serves a `MailDomain` that has the hostname as its domain
     (attached to the server and authorized), which carries the one
     record of that name with the sources of its server. A `MailDomain`
     of that name that references another server, or is not authorized,
     does not: this server still reports the record.
   - The `MX` record is `10 <hostname>` and is required while the server
     serves the domain and its hostname is authorized. It is verified when
     one `MX` record at the domain points at the hostname; other records
     (a backup exchanger) are not compared. The `MX` and the address
     records are verified through the resolver (`LookupMX`, `LookupHost`)
     with the schedule of the other records, and the `MailServer`
     controller uses `--dns-resolver` as well.
   - Only `TXT` records are published by the `DNSEndpoint`. Publishing
     `A`/`AAAA`/`MX` is in the backlog.

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
- **Part B:** the server name is the hostname, so the coupling to the
  order of the domains is gone. The cost: the hostname is one more name
  to grant, to create records for, and to rename with care (until the new
  revision is validated and rolled out the pods present the old name,
  which the Certificate no longer requests; the domains keep being
  served, also while the server is blocked for another reason).
  Existing servers need a hostname and the
  steps in the upgrade notes.
- **Limits:** `status.domains` holds 128 entries; the cap is revisited in
  the backlog.
