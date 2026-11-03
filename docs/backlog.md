# Backlog

Ideas parked outside the current phase. Items move into a phase when they are
scoped; nothing here is committed.

- DNS TXT proof of control on `MailDomainGrant` (`verification.dnsTXT`),
  for self-service domain onboarding (see ADR 0003).
- Additive `MailDomainGrant` fields: `allowedClusterIssuers`,
  `allowedDNSModes`.
- Make the revocation grace period (15 minutes) configurable.
- Capsule integration documentation (namespace prefix enforcement for
  grant binding).
- Reconsider a ValidatingAdmissionPolicy for early rejection (dropped in
  ADR 0003).
- OIDC directory backend for `MailDirectory` (after LDAP/SQL).
- rDNS backends other than `manual` (externalDNS, provider APIs).
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
- Optional Rspamd message history (`MailServer` field with a retention
  period and subject privacy); disabled today (ADR 0005).
- Licence policy call for GPL-2.0-only `busybox` in the CNMS Postfix
  image (queue-metrics and Rspamd metrics sidecars): decide whether it
  fits section 9 of the project plan, then add it to `NOTICE` and the
  third-party notices (ADR 0005, licence note).
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
- Tracing.
- Webmail/UI, billing, multi-cluster federation.
- IMAP horizontal scaling.
