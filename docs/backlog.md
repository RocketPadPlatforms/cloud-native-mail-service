# Backlog

Ideas parked outside the current phase. Items move into a phase when they are
scoped; nothing here is committed.

- DNS TXT proof of control on `MailDomainGrant` (`verification.dnsTXT`),
  for self-service domain onboarding (see ADR 0003).
- Additive `MailDomainGrant` fields: `allowedClusterIssuers`,
  `allowedDNSModes`. `allowedClusterIssuers` replaces the all-or-nothing
  `--allow-cluster-issuers` operator flag (ADR 0003).
- Make the revocation grace period (15 minutes) configurable.
- Capsule integration documentation (namespace prefix enforcement for
  grant binding).
- Reconsider a ValidatingAdmissionPolicy for early rejection (dropped in
  ADR 0003).
- OIDC directory backend for `MailDirectory` (after LDAP/SQL).
- rDNS backends other than `manual` (externalDNS, provider APIs).
- Sync the k8up annotations onto the PVCs of an existing StatefulSet
  (the claim templates are immutable, so toggling `spec.backup.enabled`
  or upgrading does not reach volumes that already exist) and remove
  them when `spec.backup.enabled` is switched off.
- Operator-managed k8up `Schedule` resources and repository credentials
  (user-created, documented examples for now).
- DANE/DNSSEC automation.
- Show the validator output in the `ConfigRejected` message (needs
  `pods/log` access for the operator) and retry a failed validation
  Job after a backoff instead of waiting for its TTL (ADR 0009).
- Roll out a validated component while another is still validating
  (ADR 0009 applies all components together).
- Restart the `RolloutStalled` clock on any template change (for
  example an engine image bump by an operator upgrade); today only a
  spec change restarts it (ADR 0009).
- Postfix scale-down: move a removed replica's queue to the remaining
  replicas (or retain the PVC until empty), then lift the CEL rule
  that rejects decreasing `postfix.replicas` (ADR 0010).
- Operator-driven PVC resize for the Postfix queue and the mail
  volume (expand the PVCs, then allow `queueStorage`/`storage` edits);
  today the sizes are immutable and resized manually (ADR 0010).
- `MailServer` field to opt out of the Rspamd egress policy (public
  TCP 443 and UDP 11335, ADR 0006) for clusters that do not want the
  fuzzy and map traffic; the policy is always rendered today.
- Optional Rspamd message history (`MailServer` field with a retention
  period and subject privacy); disabled today (ADR 0005).
- Phase 5 (`MailDomain`, DKIM): a "never unsigned" guard in Rspamd, a
  postfilter Lua rule that calls `task:set_pre_result('soft reject', ...)`
  for authenticated or local mail without `DKIM_SIGNED`. Exempt the
  empty-sender mail Postfix generates itself (bounces, notifications):
  the Postfix MILTER_README says `non_smtpd_milters` must not reject or
  tempfail it. Also decide `internal_mail_filter_classes = bounce, notify`
  (whether locally generated bounces are signed) with the same caveats;
  not set today. Check key presence before rollout.
- Phase 3 (users, ADR 0008 amendment): bind the submission login to the
  sender address (`smtpd_sender_login_maps` plus
  `reject_sender_login_mismatch` on port 587). Not rendered today, so any
  authenticated user can use any sender address.
- `MailDomain` (Phase 5) and `MailDirectory` (Phase 3) have no controller
  yet: the empty scaffolds were removed so the operator holds no write
  access to them. Phases 3 and 5 re-scaffold them with
  `kubebuilder create api --group cnms --version v1alpha1 --kind <Kind>
  --controller --resource=false` and add the RBAC they actually use.
- Pin the CNMS-built images (`cnms-postfix`, `cnms-e2e-client`), which the
  `images` workflow publishes to `ghcr.io/rocketpadplatforms`, by digest in
  `internal/images/images.go` (tag only today, ADR 0004).
- `MailServer` field for the resource requests and limits of each
  component (`spec.<component>.resources`); the defaults are constants
  in `internal/manifests/resources.go` today.
- Valkey authentication (`requirepass` from a Secret) and a
  `maxmemory` below the container limit: Valkey runs without a password
  and without a memory cap today, protected only by the NetworkPolicies
  (ADR 0006), and is OOM-killed instead of refusing writes when it
  reaches its memory limit.
- Tracing.
- Webmail/UI, billing, multi-cluster federation.
- IMAP horizontal scaling.
