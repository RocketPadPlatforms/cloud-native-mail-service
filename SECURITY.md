# Security policy

## Reporting a vulnerability

Please report security issues privately via the
[GitHub security advisories](https://github.com/RocketPadPlatforms/cloud-native-mail-service/security/advisories/new)
for this repository. Do not open a public issue for vulnerabilities that are
not yet disclosed.

For out-of-band contact: `management@rocketpadplatforms.de`.

## Supported versions

The current minor release is supported. The previous minor release receives
critical security fixes. `v1alpha1` APIs may change before v1.

## Response time

We aim to acknowledge reports within 7 working days and to ship fixes within
30 working days, depending on impact.

## Scope notes

- CNMS is a control plane: it renders configuration for Postfix, Rspamd, and
  Dovecot and manages their lifecycle. Bugs in the engines themselves should
  be reported to the upstream projects.
- Secrets (TLS keys, DKIM keys, directory credentials) must not appear in
  logs, status fields, or events. If you find one, report it here.
