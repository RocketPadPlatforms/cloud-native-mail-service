# Reputation isolation

Sender reputation is how receiving mail systems judge the sending
IPs and domains they see: volume, bounce and complaint rates, blocklist
membership, and matching DNS (PTR, SPF, DKIM, DMARC). On a
multi-tenant cluster, one noisy or misconfigured mail system must not
drag the others down with it.

CNMS therefore isolates every `MailServer` completely:

- **own Postfix instances and queues** — separate workloads,
  separate queue storage, nothing is shared between servers;
- **own sending addresses** — outbound delivery uses the addresses
  assigned to the server (via an IP pool claim, arriving with the
  IP management phase), never another server's;
- **own DKIM keys and selectors** — each server signs with its own
  keys, so a key rotation or a lost key affects one server only;
- **own HELO names** — derived from the server's own addresses;
- **own Rspamd Deployment and own Valkey instance** — rate-limit,
  reputation and Bayes state are never shared between servers
  (one dedicated set per `MailServer`, never pooled).

Inbound is isolated the same way: each `MailServer` has its own
inbound Service and its own postscreen state.

## What is shared

Only infrastructure: the cluster itself (nodes, CNI, etcd). No
mail-related state is shared between `MailServer`s.

## Operational consequences

- **Blast radius is one server.** A misconfiguration, a bad sender
  population or a compromised mailbox in one `MailServer` cannot
  damage the reputation of any other.
- **Scale horizontally by running more servers.** More
  `MailServer`s (with their own addresses) are the way to grow
  sending volume, not larger replicas.
- **Failures stay local.** A data-plane failure in one server
  (see [failure modes](failure-modes.md)) degrades that server
  only; the control plane keeps reconciling the rest.
