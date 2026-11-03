# ADR 0006: NetworkPolicy floor and security floor

- **Status:** Accepted
- **Date:** 2026-10-25

## Context

Reputation isolation (principle 6) and the mail flow must hold even
before any tenant-specific policy exists: other pods in a cluster must
not be able to send mail from node IPs (they would damage the sending
reputation), AUTH must only happen over TLS, TLS below 1.2 must be
rejected, rate limits must exist, and the server must never be an open
relay. The project plan assigns the default-deny NetworkPolicy floor
and the security floor to Phase 2; the Cilium egress-gateway
narrowing of port-25 egress arrives with Phase 7.

## Decision

1. **Per-`MailServer` NetworkPolicies** (rendered by the operator into
   the `MailServer`'s namespace, labeled
   `cnms.rpp.gmbh/managed-by=cnms` plus the app label):
    - Ingress default-deny for CNMS pods, with allows: the inbound
      Service ports (25, 587, and the IMAP ports 143/993 when
      mailboxes are enabled) from anywhere; the component ports
      (postfix→rspamd milter 11332, postfix→dovecot 24 and the auth
      port, rspamd→valkey 6379) only between the pods of the same
      `MailServer`; the metrics ports (ADR-0005: Postfix 9150, Rspamd
      9151, Valkey 9121) from the cluster (documented: v1 allows
      in-cluster scraping without extra configuration). The "cluster"
      peer is an any-namespace `namespaceSelector: {}` next to the
      `0.0.0.0/0` and `::/0` ipBlocks: Cilium CIDR rules never match
      cluster endpoints (pods are selected by identity, not by IP), so
      with ipBlocks alone an in-cluster Prometheus could not scrape.
      The Rspamd controller (11334, loopback clients only) and normal
      worker (11333, unauthenticated scan API) ports are not allowed
      from any pod: the Rspamd metrics reach the cluster only through
      the loopback sidecar on 9151, which cannot reach any other
      controller endpoint.
    - Egress default-deny for CNMS pods, with allows: DNS (53) to the
      `k8s-app=kube-dns` pods of `kube-system` (one peer with a
      namespace and a pod selector, which are ANDed) plus the
      ipBlocks for resolvers outside the cluster, the component ports
      above, and port 25 egress from the Postfix pods (Phase 2: to
      any destination; Phase 7 narrows this to the assigned egress
      path via the Cilium egress gateway).
    - Egress for the **Rspamd pods only** to public addresses: TCP 443
      and UDP 11335. This is deliberate. The shipped Rspamd 4.2.3
      configuration queries the fuzzy storage of rspamd.com (SRV
      `_fuzzy._tcp.rspamd.com`, UDP 11335; it sends fuzzy hashes of
      messages, not their content) and fetches its maps from
      `https://maps.rspamd.com`; without the allow every message
      waits for the fuzzy timeout, the fuzzy module scores nothing and
      the map fetches fail forever. The targets are external, so the
      peers are `0.0.0.0/0` and `::/0` ipBlocks, with the private,
      link-local (cloud metadata) and unique-local ranges excepted so
      the allow does not open in-cluster services. Other pods get no
      such allow. Operators who do not want the traffic can delete the
      `<server>-egress-rspamd` policy; there is no API field for it
      yet.
2. **Port-25 egress deny for non-CNMS pods is a static, admin-owned
   manifest, not an operator-rendered policy.** A plain
   `NetworkPolicy` can only allow, so "everything except port 25" had
   to be written as an allow of all other ports. That first design
   (one `cnms-no-port25-egress` policy per namespace) failed in three
   ways: policies are additive, so it re-opened every other port for
   pods that a tenant's own default deny had closed; its single name
   was shared by all servers of a namespace, so deleting the server
   that owned it removed it for the others; and it exempted pods by
   the `cnms.rpp.gmbh/managed-by` label, which any tenant can set on
   its own pods. The operator no longer renders it. Instead
   `config/network-policy/smtp-egress-deny` ships a
   `CiliumClusterwideNetworkPolicy` with `egressDeny` for TCP 25 to
   the `world` entity (deny rules beat allow rules, and
   `enableDefaultDeny` stays off so nothing else changes). The Postfix
   pods are exempted by not selecting them: one rule selects pods
   outside an admin-maintained list of MailServer namespaces, another
   selects the non-Postfix pods inside those namespaces. The list
   lives in a cluster-scoped object that tenants cannot edit, which is
   the least spoofable option Cilium offers here: namespace names are
   admin-controlled, whereas pod labels and ServiceAccount names are
   tenant-controlled. Usage and verification:
   [Block direct SMTP egress](../how-to/block-direct-smtp-egress.md).
   Residual limitations, accepted: Cilium only (the vendor-neutral
   `ClusterNetworkPolicy` of the network-policy API, `v1alpha2`, is
   alpha and not widely implemented yet); inside a listed namespace a
   tenant who can create pods can set the Postfix label; host-network
   pods are not endpoints of the policy.
3. **Hard-coded Postfix floor** (the renderer always emits these, not
   exposed in the v1 API):
   - `smtpd_tls_auth_only = yes` on the submission listener (AUTH
     only over TLS).
   - TLS ≥ 1.2 for smtpd and smtp, for mandatory TLS
     (`*_tls_mandatory_protocols`) and for opportunistic TLS
     (`*_tls_protocols`; the Postfix 3.11 default is `>=TLSv1`, so
     without it port 25 would still negotiate TLS 1.0/1.1), modern
     cipher floors (`*_tls_mandatory_ciphers`). A peer limited to
     older TLS versions has to retry port 25 without TLS.
   - `reject_unauth_destination` (no open relay), present in both
     `smtpd_recipient_restrictions` and `smtpd_relay_restrictions` —
     the latter's default (`defer_unauth_destination`) would otherwise
     tempfail (454) unauthenticated relay attempts before the recipient
     restrictions run.
   - Anvil rate limits with explicit values as renderer constants
     (connection/message/recipient rate per client), verified against
     the 3.11 `postconf.5` at render time.
   - postscreen on port 25 (PROXY protocol support documented;
     `externalTrafficPolicy: Local` preserves client IPs).
   - `milter_default_action = tempfail` with deliberate milter
     timeouts, plus `non_smtpd_milters` (principle 5, "never
     unsigned").
   - `mynetworks = 127.0.0.0/8` (loopback only); everything else
     relays via authentication or the accepted-domain maps.
4. **Dovecot floor** (verified against `dovecot/dovecot:2.4.5`): the
   global `ssl = required` with `ssl_min_protocol = TLSv1.2`.
   Verified 2.4 behaviour: the listener named `imaps` does implicit
   TLS; on the `imap` listener AUTH is only possible after STARTTLS;
   the auth protocol listener (4190) is not affected by `ssl` and
   stays plain TCP, reachable only from the Postfix pods of the same
   `MailServer`. The internal LMTP listener is exempt from
   `ssl = required` via a per-listener `ssl = no` (without it, 2.4
   forces implicit TLS on the listener); it is TCP without TLS,
   reachable only via the NetworkPolicies (documented).
5. **E2E contract (open-relay and floor tests):** unauthenticated
   inbound mail on 25 for a foreign domain is rejected (554);
   `AUTH` on 587 before STARTTLS is not advertised (or 503). The
   port-25 egress deny (item 2) is not part of the e2e suite: kind's
   default CNI does not implement Cilium policies, so the manifest is
   only checked with `kustomize build`.

## Consequences

- The floor is not configurable in the v1 API (working agreement:
  keep the public API small). Per-`MailServer` overrides (e.g. custom
  rate limits, no postscreen) go to the backlog.
- Without the admin-applied deny manifest (item 2) other pods can
  still send port-25 mail from node addresses; the operator cannot
  close that itself. The namespace list in the manifest has to follow
  the set of namespaces that run a `MailServer`.
- With postscreen enabled, the first connections from new clients go
   through the postscreen challenge; clients without reverse DNS or
   with broken banners may be temporarily challenged (documented in
   the operations docs).
- Every NetworkPolicy the operator renders carries the
  `cnms.rpp.gmbh/managed-by` label so it is identifiable and
  deletable; manual policies in the same namespace are untouched.
