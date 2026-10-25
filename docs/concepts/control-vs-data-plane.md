# Control plane vs data plane

CNMS splits the system into a control plane (the operator) and a
data plane (the workloads that actually handle mail). The two never
depend on each other at runtime: **if the operator is down, mail
keeps flowing.**

## What is which

| Plane | Components | Depends on the other at runtime? |
|---|---|---|
| Control | the CNMS operator | no |
| Data | Postfix, Rspamd, Valkey, Dovecot workloads, their Services, the rendered ConfigMaps/Secrets, NetworkPolicies | no |

The data plane runs entirely from rendered ConfigMaps and Secrets:
Postfix, Rspamd and Dovecot read their configuration from mounted
files and never call the operator. The MTA-STS policy endpoint
(when implemented) is data plane as well — it serves traffic and
must not depend on the operator either.

## What the split means in practice

- **Operator outages delay, never stop.** With the operator down,
  reconciliation pauses: no new rollouts, no DKIM rotation, no status
  updates. Mail flow, filtering and authentication continue with the
  last rendered configuration.
- **Operator upgrades are safe.** Because the data plane does not
  read the operator, upgrading (or restarting) the operator never
  touches in-flight mail. The operator runs with leader election and
  is safe with 2+ replicas.
- **Bad changes fail before they land.** A configuration change only
  reaches the data plane after the validation gate
  (see [architecture](architecture.md)) has checked it with the
  engine's own tooling.
- **Failures are local.** A data-plane failure (for example
  Rspamd) degrades or queues mail for the affected `MailServer`
  only; the control plane keeps reconciling, and other
  `MailServer`s are untouched
  ([reputation isolation](reputation-isolation.md)).

## What is *not* covered by the split

The data plane is only as healthy as its inputs: certificates (from
cert-manager), the external user directory, DNS. The failure
behaviour of those dependencies is defined in
[failure modes](failure-modes.md) — the rule is the same: temporary
failures (4xx and queueing), never lost mail.
