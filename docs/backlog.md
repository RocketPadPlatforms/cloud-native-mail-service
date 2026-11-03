# Backlog

Ideas parked outside the current phase, grouped by the phase that would pick
them up. Items move into a phase when they are scoped; nothing here is
committed.

## Phase 3: users and service accounts

- Bind the submission login to the sender address (`smtpd_sender_login_maps`
  plus `reject_sender_login_mismatch` on port 587; ADR 0008 amendment). Not
  rendered today, so any authenticated user can use any sender address.
- `MailDirectory` has no controller yet: the empty scaffold was removed so
  the operator holds no write access to it. Phase 3 re-scaffolds it with
  `kubebuilder create api --group cnms --version v1alpha1 --kind MailDirectory
  --controller --resource=false` and adds the RBAC it actually uses.
- OIDC directory backend for `MailDirectory` (after LDAP/SQL).

## Phase 5: `MailDomain` and domain authorization

- Derived-name conflicts (`DerivedNameConflict`, ADR 0003): a `MailDomain`
  whose own name is a derived name of another active domain. Not detected
  by the `MailDomain` controller yet.
- Check that a `DNSEndpoint` target longer than 255 octets (the RSA-2048
  DKIM value) is published as one TXT record by the common external-dns
  providers (ADR 0012 left it unverified); split it into character-strings
  in the target if one needs it.
- Re-verify a pending DKIM key right before it becomes active when the
  last lookup is older than the hold period, and verify against the
  authoritative servers instead of one resolver.
- Make the Secrets of bring-your-own keys cheaper to follow: they are read
  every five minutes because Secrets are not cached; a metadata-only watch
  on labelled Secrets would react at once.
- A "never unsigned" guard in Rspamd: a postfilter Lua rule that calls
  `task:set_pre_result('soft reject', ...)` for authenticated or local mail
  without `DKIM_SIGNED`. Exempt the empty-sender mail Postfix generates itself
  (bounces, notifications): the Postfix MILTER_README says `non_smtpd_milters`
  must not reject or tempfail it. Also decide
  `internal_mail_filter_classes = bounce, notify` (whether locally generated
  bounces are signed) with the same caveats; not set today. Check key
  presence before rollout.
- DNS TXT proof of control on `MailDomainGrant` (`verification.dnsTXT`), for
  self-service domain onboarding (see ADR 0003).
- Additive `MailDomainGrant` fields: `allowedClusterIssuers`,
  `allowedDNSModes`. `allowedClusterIssuers` replaces the all-or-nothing
  `--allow-cluster-issuers` operator flag (ADR 0003).
- Make the revocation grace period (15 minutes) configurable.
- Capsule integration documentation (namespace prefix enforcement for grant
  binding).
- Reconsider a ValidatingAdmissionPolicy for early rejection (dropped in
  ADR 0003).
- DANE/DNSSEC automation.

## Phase 7: IP management

- rDNS backends other than `manual` (externalDNS, provider APIs).

## Phase 9: hardening, backup and observability

- Sync the k8up annotations onto the PVCs of an existing StatefulSet (the
  claim templates are immutable, so toggling `spec.backup.enabled` or
  upgrading does not reach volumes that already exist) and remove them when
  `spec.backup.enabled` is switched off.
- Operator-managed k8up `Schedule` resources and repository credentials
  (user-created, documented examples for now).
- Postfix scale-down: move a removed replica's queue to the remaining
  replicas (or retain the PVC until empty), then lift the CEL rule that
  rejects decreasing `postfix.replicas` (ADR 0010).
- Operator-driven PVC resize for the Postfix queue and the mail volume
  (expand the PVCs, then allow `queueStorage`/`storage` edits); today the
  sizes are immutable and resized manually (ADR 0010).
- Show the validator output in the `ConfigRejected` message (needs `pods/log`
  access for the operator) and retry a failed validation Job after a backoff
  instead of waiting for its TTL (ADR 0009).
- Roll out a validated component while another is still validating (ADR 0009
  applies all components together).
- Restart the `RolloutStalled` clock on any template change (for example an
  engine image bump by an operator upgrade); today only a spec change
  restarts it (ADR 0009).
- `MailServer` field to opt out of the Rspamd egress policy (public TCP 443
  and UDP 11335, ADR 0006) for clusters that do not want the fuzzy and map
  traffic; the policy is always rendered today.
- Optional Rspamd message history (`MailServer` field with a retention period
  and subject privacy); disabled today (ADR 0005).
- `MailServer` field for the resource requests and limits of each component
  (`spec.<component>.resources`); the defaults are constants in
  `internal/manifests/resources.go` today.
- Valkey authentication (`requirepass` from a Secret) and a `maxmemory` below
  the container limit: Valkey runs without a password and without a memory
  cap today, protected only by the NetworkPolicies (ADR 0006), and is
  OOM-killed instead of refusing writes when it reaches its memory limit.

## Phase 10: docs and release

- Pin the CNMS-built images (`cnms-postfix`, `cnms-e2e-client`), which the
  `images` workflow publishes to `ghcr.io/rocketpadplatforms`, by digest in
  `internal/images/images.go` (tag only today, ADR 0004).
- List the licenses of the npm tooling of the docs site (Docusaurus and its
  dependencies, not shipped) in the third-party notices; only the Go modules
  and the data-plane images are listed today.

## No phase yet

- Tracing.
- Webmail/UI, billing, multi-cluster federation.
- IMAP horizontal scaling.
