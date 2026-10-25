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
- Tracing.
- Webmail/UI, billing, multi-cluster federation.
- IMAP horizontal scaling.
