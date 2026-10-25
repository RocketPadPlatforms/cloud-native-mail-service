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
     Service ports (25, 587) from anywhere; the component ports
     (postfix→rspamd milter 11332, postfix→dovecot 24 and the auth
     port, rspamd→valkey 6379) only between the pods of the same
     `MailServer`; the metrics ports (ADR-0005) from the cluster
     (documented: v1 allows in-cluster scraping without extra
     configuration).
   - Egress default-deny for CNMS pods, with allows: DNS (53), the
     component ports above, and port 25 egress from the Postfix pods
     (Phase 2: to any destination; Phase 7 narrows this to the
     assigned egress path via the Cilium egress gateway).
2. **Cluster-wide port-25 egress deny for non-CNMS pods:** one
   NetworkPolicy per namespace (namespace selector `{}`, pod selector
   excluding the CNMS label, egress 25 denied). Phase 2: the operator
   renders it into the namespaces it manages; the Helm chart (Phase
   10) ships it for cluster-wide rollout. Namespaces without a CNMS
   policy are not protected — documented limitation, fail-closed
   contract proven in e2e.
3. **Hard-coded Postfix floor** (the renderer always emits these, not
   exposed in the v1 API):
   - `smtpd_tls_auth_only = yes` on the submission listener (AUTH
     only over TLS).
   - TLS ≥ 1.2 for smtpd and smtp (`*_tls_mandatory_protocols`),
     modern cipher floors (`*_tls_mandatory_ciphers`).
   - `reject_unauth_destination` (no open relay).
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
4. **Dovecot floor:** `ssl = required` with a TLS 1.2 minimum
   (`ssl_min_implied_version = tls12`) on the submission/auth-facing
   listeners; the internal LMTP/auth listeners are TCP without TLS,
   reachable only via the NetworkPolicies (documented).
5. **E2E contract (open-relay and floor tests):** unauthenticated
   inbound mail on 25 for a foreign domain is rejected (554);
   `AUTH` on 587 before STARTTLS is not advertised (or 503); a
   non-CNMS pod in a namespace with the port-25 egress deny cannot
   deliver mail.

## Consequences

- The floor is not configurable in the v1 API (working agreement:
  keep the public API small). Per-`MailServer` overrides (e.g. custom
  rate limits, no postscreen) go to the backlog.
- The port-25 egress deny is per-namespace; protection is only as
  wide as the namespaces where the policy is rendered.
- With postscreen enabled, the first connections from new clients go
   through the postscreen challenge; clients without reverse DNS or
   with broken banners may be temporarily challenged (documented in
   the operations docs).
- Every NetworkPolicy the operator renders carries the
  `cnms.rpp.gmbh/managed-by` label so it is identifiable and
  deletable; manual policies in the same namespace are untouched.
