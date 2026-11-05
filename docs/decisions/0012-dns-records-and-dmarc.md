# ADR 0012: DNS records, DMARC and DNS verification

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

A `MailDomain` owns the public DNS records of one domain (project plan,
section 3). Phase 5 has to decide what exactly is published, how it is
published (`manual` status or `DNSEndpoint`), and what "verified" means.
The DMARC policy CNMS publishes follows RFC 9989-9991; the project plan
asks for a record of how Rspamd evaluates DMARC on inbound mail, because
that is what the pinned engine does and not what the RFC says.

### Verified facts

**Rspamd 4.2.3 DMARC evaluation** (`4.2.3` tag of
[rspamd/rspamd](https://github.com/rspamd/rspamd/tree/4.2.3):
`src/plugins/lua/dmarc.lua`, `lualib/plugins/dmarc.lua`):

- The organizational domain is the effective second-level domain from
  the public suffix list built into Rspamd (`rspamd_util.get_tld`). There
  is **no DNS tree walk** (RFC 9989 section 4.10). The policy is looked
  up at `_dmarc.<From domain>` and, when that differs, at
  `_dmarc.<organizational domain>`; the first record found wins.
- Tags read: `p`, `sp` (only for the record at the organizational
  domain), `adkim`, `aspf`, `pct`, `rua`. The record grammar accepts any
  other alphabetic tag and ignores it, so **`t`, `np`, `psd`, `fo`,
  `ruf` have no effect**. A published `p=reject; t=y` is enforced as
  `reject`; the RFC 9989 meaning of `t=y` (apply one level lower) is not
  implemented. `pct` is still honoured on records that carry it.
- DMARC is not evaluated for authenticated or local clients, and a
  message with several `From` headers or none gets the `DMARC_NA` result.

**RFC 9989** ([rfc-editor.org](https://www.rfc-editor.org/rfc/rfc9989)): `v=DMARC1` must
be the first tag; `p` defaults to `none`; `sp` applies to existing
subdomains and `np` to non-existent ones (`np` falls back to `sp`, then
`p`); `t=y` asks receivers to apply the policy one level lower (`reject`
to `quarantine`, `quarantine` to `none`) and has no effect on `none`;
`psd` is for public suffix operators; `pct`, `rf` and `ri` are historic
and not to be published; `rua`/`ruf` are comma-separated URI lists;
unknown tags must be ignored. Reports to an address outside the policy
domain's organizational domain only go out when the report receiver
publishes `<policy domain>._report._dmarc.<receiver domain>` TXT
`v=DMARC1` (RFC 9990 section 4).

**external-dns v0.24.0** (`kubernetes-sigs/external-dns`, tag `v0.24.0`):
the `DNSEndpoint` CRD is `externaldns.k8s.io/v1alpha1`, namespaced, with
`spec.endpoints[]` of `dnsName`, `recordType` (`A`, `AAAA`, `CNAME`,
`DNAME`, `MX`, `NAPTR`, `NS`, `PTR`, `SRV`, `TLSA`, `TXT`), `recordTTL`,
`targets[]` (each 1-4096 characters) and `providerSpecific`. The source is
enabled with `--source=crd --crd-source-apiversion=externaldns.k8s.io/v1alpha1
--crd-source-kind=DNSEndpoint`. The managed record types default to `A`,
`AAAA` and `CNAME`: **`--managed-record-types=TXT` must be added**, and
`MX` is not among the supported managed types. Two objects that declare
the same `dnsName` and `recordType` conflict; external-dns publishes one.
The upstream TXT example passes the plain string as the target. How a
provider handles a value longer than 255 octets (the RSA-2048 DKIM
value) is provider specific and **was not verified**; the upstream
documentation does not say.

**Postfix 3.11.7** has no `{postfix_internal_origin}` macro (added in a
later release), which matters for ADR 0013.

**Go** `net.Resolver.LookupTXT` concatenates the strings of one TXT
record, as RFC 6376 and RFC 7208 require. A lookup error is
a `*net.DNSError` whose `IsNotFound` distinguishes NXDOMAIN or no data
from `IsTimeout`/`IsTemporary`.

## Decision

### 1. The records

| Purpose | Name | Type | Value | Published when |
|---|---|---|---|---|
| DKIM | `<selector>._domainkey.<domain>` | TXT | `v=DKIM1; k=rsa; p=<base64 SPKI>` or `v=DKIM1; k=ed25519; p=<base64 raw key>` (ADR 0011) | one per active, pending and retiring key |
| SPF | `<domain>` | TXT | `v=spf1 <mechanisms> <qualifier>all` | at least one sending source is known (section 3) |
| DMARC | `_dmarc.<domain>` | TXT | `v=DMARC1; p=<policy>[; sp=..][; np=..][; t=y][; rua=mailto:..][; ruf=mailto:..]` | `spec.dmarc` is set |
| MTA-STS | `_mta-sts.<domain>` | TXT | `v=STSv1; id=<policy id>` | `spec.mtaSts.enabled` |
| TLS-RPT | `_smtp._tls.<domain>` | TXT | `v=TLSRPTv1; rua=mailto:<address>` | `spec.tlsRpt.enabled` and an address is known |

All TXT, all with a TTL of 3600 seconds. CNMS does not publish `MX`,
`A`/`AAAA` (the inbound address is the admin's and the load balancer's),
`PTR` (Phase 7), the `mta-sts.<domain>` HTTPS host, or the
`_report._dmarc` authorization at a report receiver.

**DMARC composition.** The tag order is fixed (`v`, `p`, `sp`, `np`, `t`,
`rua`, `ruf`), the separator is `"; "`, there is no trailing `;`. `p`
defaults to `none` when `spec.dmarc` is set; with `spec.dmarc` absent no
record is published and none is required. `sp` and `np` are additive API
fields (`subdomainPolicy`, `nonExistentSubdomainPolicy`, no default, so
the tag is omitted unless set). Never emitted: `pct`, `rf`, `ri`, `psd`
(a public suffix cannot be a `MailDomain`, section 5), `adkim`/`aspf`/`fo`
(their defaults are what CNMS wants). `rua` and `ruf` get `mailto:`
prepended by the renderer. `t=y` is emitted when `testing` is true; the
API comment on `testing` ("evaluated but not enforced") is corrected to
the RFC 9989 meaning, and the docs state that the pinned Rspamd ignores
`t` on inbound records.

If a DMARC `rua` or `ruf` address is in another organizational domain than
the policy domain (public suffix list from `golang.org/x/net/publicsuffix`),
the status carries a `ReportAuthorization` warning naming the record the
receiver has to publish. CNMS neither publishes nor verifies it. TLS-RPT
has no such mechanism (RFC 8460 defines no authorization record), so a
TLS-RPT address in another domain gets no warning.

**MTA-STS** stays report-only. The policy text is

```
version: STSv1\r\n
mode: testing\r\n
mx: <hostname>\r\n          (the MailServer of spec.serverRef, when it serves the domain)
max_age: 604800\r\n
```

(RFC 8461 section 3.2; the id is `mtasts.PolicyID` of that exact text, as
implemented in `internal/mtasts`). `mode` becomes an additive field `spec.mtaSts.mode`
(`testing` default, `enforce`, `none`). The `mx` hosts are the server
hostnames (`RenderRequest.ServerHostname`) of the `MailServer`s in the
namespace that the domain references; none yet means no policy and a condition.
The record is required only when a policy exists.

**TLS-RPT** needs an address (RFC 8460 requires `rua`): `spec.tlsRpt.rua`,
else `spec.dmarc.rua`. Both are on the `MailDomain`, so a CEL rule
rejects enabled TLS-RPT without either at admission.

### 2. DNS modes

- **`manual`** (default): `status.records` lists every required record;
  nothing is created.
- **`externalDNS`**: in addition, one `DNSEndpoint` named
  `<maildomain>-dns` in the `MailDomain` namespace, controlled by the
  `MailDomain` (owner reference: the endpoints are recomputed from the
  spec and the keys, so unlike the key Secrets they hold no state and
  may be garbage-collected). The same ownership checks as for other
  created objects apply: a `DNSEndpoint` of that name that the
  `MailDomain` does not control is a conflict, reported and not
  overwritten. One endpoint per record, `recordType: TXT`, `recordTTL:
  3600`, one plain target (the full value). Retiring DKIM records stay in
  the object until they retire (ADR 0011). Revocation follows ADR 0003:
  stop signing at once, remove the `DNSEndpoint` after the 15-minute
  grace period, keep the key Secrets.
- A `DNSEndpoint` is created only for an authorized, unique domain (ADR
  0003); the `MailDomain` controller evaluates the same pure function the
  `MailServer` controller uses and sets `Authorized`.

**Optional integration.** The `DNSEndpoint` CRD is an optional
integration (principle 10), handled exactly like the `ServiceMonitor`:
read `dnsendpoints.externaldns.k8s.io` through the API reader with a TTL
cache (`crdsAvailable`, factored out of the `MailServer` controller so
both controllers share it), unstructured objects, a watch only when the
CRD is installed at startup, the next re-check picks up a later
installation. If the CRD is missing the records are still listed in the
status, `DNSPublished=False` with reason `ExternalDNSNotInstalled`, and
nothing crashes. `dnsMode: externalDNS` only states the wish; the status
is the truth.

The documentation states what external-dns needs: the CRD source with the
arguments above, `--managed-record-types=TXT`, a `--domain-filter`
covering the domain, and no namespace, label or annotation filter that
hides the `DNSEndpoint`. `DNSEndpoint.status.observedGeneration` only says
external-dns has read the object; the published state is established by
the verification below.

### 3. SPF

CNMS generates the record from the sending sources of the domain:

1. **`spec.spf`** (optional, added in Phase 5): `ip4` and `ip6` lists
   (address or CIDR, validated), `include` list (domain names, at most 5)
   and `qualifier` (`softfail` default `~all`, `fail` `-all`, `neutral`
   `?all`). This is the only source in Phase 5.
2. **Direct mode** (Phase 7): the addresses of the bound
   `MailIPPoolClaim` of the `MailServer` the domain references
   (`ip4:`/`ip6:`, merged and sorted). Placeholder until then: none.
3. **Relay mode** (Phase 6): `include:` of the `MailRelay`'s SPF include
   name. Placeholder until then: none.

Mechanisms are ordered `ip4`, `ip6`, `include`, each sorted, followed by
the qualifier and `all`, so the output is deterministic. With no source at
all no SPF record is published or required (a record `v=spf1 -all` would
declare a sending domain silent and fail its own mail); `SPFReady=False`
with reason `NoSendingSources` says so. Not checked: the RFC 7208
10-lookup limit across nested `include`s (needs lookups of other
people's records; documented). The apex SPF record also serves as the
SPF identity of the HELO name, because the HELO name is the primary
domain of the `MailServer`; with empty-sender mail (bounces) that is the
identity receivers check. (Amended by ADR 0014: after the server hostname
exists, the HELO identity is the hostname, not the primary domain, and its
SPF record is reported at the hostname.)

### 4. Verification

Behind an interface, so tests never touch real DNS:

```go
// Resolver is the DNS lookup the controller needs.
type Resolver interface {
    LookupTXT(ctx context.Context, name string) ([]string, error)
}
```

`*net.Resolver` satisfies it. The operator takes an optional
`--dns-resolver=host:port` (default: the pod's system resolver; the
blocklist checker of Phase 7 has the same need). Unit and envtest tests
use a fake with canned answers and errors; the kind e2e points the flag at
a small CoreDNS pod (Apache-2.0, license-checked when it is added to the
e2e suite) that serves a test zone, never at the Internet.

**What "verified" means**, per record, with all strings of one TXT record
concatenated and the comparison applied to every TXT record at the name
(other TXT records at the name, such as site verification tokens, are
ignored):

| Record | Verified when |
|---|---|
| DKIM | exactly one record at the name parses as a DKIM key record (tag-value pairs with a `p` tag and `v=DKIM1` or no `v`; several give an undefined result, RFC 6376 section 3.6.2.2), and its key type (`k`, default `rsa`) equals the expected one and its **decoded** `p` bytes equal the expected public key (whitespace removed from the base64, so provider re-wrapping does not matter); extra tags are allowed |
| DMARC, TLS-RPT, MTA-STS | exactly one record starts with the expected version tag (RFC 8460 and RFC 8461 treat a number other than one as no policy; Rspamd 4.2.3 treats several valid DMARC records as a permanent error), and after normalization (whitespace around `;` and `=`, trailing `;`, tag names compared case-insensitively) its tag-value pairs equal the expected ones |
| SPF | exactly one `v=spf1` record exists (several are a `permerror` for receivers) and its normalized text equals the expected text; a different SPF record is reported with the value found |

A not-found answer sets `verified=false`; a temporary error (timeout,
SERVFAIL) leaves the previous value alone and retries sooner, and after
repeated failures sets `RecordsVerified=Unknown` with reason
`LookupFailing`, never `False` (ADR 0003 applies the same rule to
revocation).

**Intervals.** Records that are not verified, and pending DKIM keys, are
checked after 1 minute, backing off to 10 minutes; verified records every
hour to notice drift. The controller watches nothing in DNS, so these
requeues are the mechanism. The schedule, the attempt counter and the
count of failed rounds in a row live in operator memory, not in the
status: a restart looks every record up again, which only costs the
lookups, and the status does not change when the answers are the same.
A record without an answer yet (a new key, a changed value) is looked up
at once. Status writes are skipped when nothing changed: `lastChecked`
is rewritten when `verified` flips or when a lookup runs and the stored
time is an hour old (the idea of ADR 0007, so a verified domain does not
write its status every reconcile). A pending key stays on the
one-to-ten-minute schedule even when its record is verified, until it is
active. Records that never answered stay `verified` unset and
`RecordsVerified` is `Unknown` with reason `Checking` until a lookup
answers or three rounds in a row fail (`LookupFailing`).

**Status.** `status.records` keeps its shape (`name`, `type`, `value`,
`verified`, `lastChecked`; at most 64 entries: five purposes, a few DKIM
selectors) and is rebuilt on every reconcile from spec, key set and
sources, so it needs no restore. `Ready` is true when the domain is
authorized, DKIM keys are valid, and every required record is verified;
manual-mode domains stay `Ready=False` until the user publishes the
records, which is the visible work item. The data plane does not wait for
`Ready`: serving and signing never depend on DNS (ADR 0011, bootstrap).
Further conditions: `Authorized`, `DKIMKeysValid`, `DKIMRotation`,
`RecordsVerified`, `DNSPublished` (only in `externalDNS` mode),
`SPFReady`, `MTASTSReady` (only with MTA-STS enabled) and the warning
`ReportAuthorization` (only with DMARC; `False` names the record a
receiver in another organizational domain has to publish). `SPFReady`,
`MTASTSReady` and `ReportAuthorization` never feed into `Ready`. The
`Ready` order is `Authorized`, `DKIMKeysValid`, `DNSPublished`,
`RecordsVerified`. The mx hosts of the MTA-STS policy come from the
`MailServer` in the domain's `spec.serverRef` when it serves the domain
(authorized, ADR 0003; amended by ADR 0014, which replaced the list in the
server's spec by this reference): it contributes its `ServerHostname`,
which is the first sorted domain it serves, so a server of several
domains gives the same host for each of them. While the server does not
exist or does not serve the domain an enabled MTA-STS has no policy and
`MTASTSReady` is `False` with reason `NoMailServer`. The `MailDomain`
controller watches `MailServer` spec changes and the domains that share
the server for this. The
name is the one in the certificate and the HELO; the MX record of the
domain has to point at it, which CNMS does not publish (section 1).

### 5. Public suffixes

The controller rejects a `MailDomain` whose domain is a public suffix
(`golang.org/x/net/publicsuffix`, BSD-3-Clause, already a transitive
dependency; license-checked when promoted to a direct one), as ADR 0003
announced: `Authorized=False` with reason `PublicSuffix`. The `MailServer`
controller rejects it the same way, so both controllers use one shared
check. The private section of the Public Suffix List counts too
(`github.io` is rejected, `x.github.io` is not), because the ICANN flag
is ignored on purpose. This is also why no `psd` field exists.

## Consequences

- **Easier:** the status is a complete, re-derivable to-do list for
  `manual` mode; `externalDNS` needs no state; verification is testable
  with a fake resolver.
- **Harder:** two controllers use the same CRD discovery code; SPF is
  incomplete until Phases 6 and 7 add sources, and until then depends on
  `spec.spf`; DNS verification asks a resolver the cluster operator has to
  allow.
- **Limits:** the pinned Rspamd evaluates inbound DMARC with the public
  suffix list and ignores `t`, `np` and `psd`; it will be revisited when
  Rspamd implements RFC 9989 (a change of the pinned version needs a new
  ADR). The `DNSEndpoint` route for values over 255 octets depends on the
  provider and is unverified. The verification only proves what the
  configured resolver sees.
- **How we know it worked:** unit tests of every record composition
  (golden), of normalization and of verification with the fake resolver;
  envtest for the `DNSEndpoint` with and without the CRD (like the
  `ServiceMonitor` tests); the `status.records` listing in an e2e with a
  test DNS server.
