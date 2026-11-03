# Upgrade notes

:::note
API version `v1alpha1`. No CNMS release exists yet, and the API may
change between any two snapshots. Once releases start, every release
gets its own section here.
:::

## Unreleased

Changes that an operator running an earlier snapshot of `main` notices.
Update the CRDs together with the operator: the new CRDs reject or
prune the old field names.

### API fields

- **`MailDomain.spec.dkim.selector` is now `selectorPrefix`.** The
  selector of a key is `<prefix>-<yyyymmdd>`; the selectors in use are
  in `status.dkim`. Rename the field in your manifests. The CRD no
  longer defaults it (the operator uses `cnms`), because it must stay
  unset when `keys` is used.
- **`MailDomain.status.dkim` is a list of keys.** The fields `selector`,
  `keyAge` (a duration) and `pendingSelector` are replaced by `active`,
  `pending` and `retiring` (each key with `selector`, `algorithm` and a
  `keyCreated` timestamp; retiring keys also have `until`) and
  `nextRotation`.
- **`MailDomain.spec.dkim.rotation.overlap` defaults to 120h** (was
  24h), which covers the Postfix queue lifetime of 5 days. An interval
  of 5 days or less is rejected unless `overlap` is set to a shorter
  value.
- **`MailDomain.spec.dmarc.testing` publishes `t=y`** with its RFC 9989
  meaning (receivers apply the policy one level lower), not "evaluated
  but not enforced".
- **`MailDomain.spec.tlsRpt.enabled` requires a report address** in
  `tlsRpt.rua` or `dmarc.rua`; such an object is rejected at admission.
- **A `MailDomain` name is limited to 63 characters.**
- **`MailDomain.status.mtaSts.policyId` is a string**, derived from the
  policy content (a truncated SHA-256), no longer an increasing integer.
- **`MailDomain.status.records` is an atomic list**, no longer a map
  keyed by name. Clients that patch single records must replace the
  whole list.
- **`MailDirectory.spec.ldap.userFilter` uses the placeholder
  `{user}`** (exactly once, default `(mail={user})`) instead of `%s`.
  The operator translates it for Postfix and Dovecot; a filter with `%`
  is rejected.
- **Storage sizes are immutable:** `spec.postfix.queueStorage` and
  `spec.mailboxes.storage` cannot be edited once set, and decreasing
  `spec.postfix.replicas` is rejected. Resize the PersistentVolumeClaims
  by hand ([ADR 0010](../decisions/0010-storage-immutability-and-scale-down.md)).

### Data plane

- **Dovecot listens on the container ports 31024 (LMTP), 31143 (IMAP)
  and 31993 (IMAPS)**; the Service ports stay 24, 143 and 993. The auth
  port is 12345. When the Dovecot pod rolls to the new ports there is a
  short LMTP and IMAP interruption; Postfix defers LMTP mail and does
  not lose it. Custom NetworkPolicies or probes that name the old
  container ports must follow.
- **Configuration lives in immutable ConfigMaps named
  `<server>-<component>-<hash8>`** (and an immutable Secret for the
  Dovecot users). The operator creates them, validates the
  configuration in a Job before the rollout and deletes the ones no
  workload references after the rollout; a configuration object of the
  previous naming that carries the CNMS labels falls under the same
  cleanup ([ADR 0009](../decisions/0009-config-validation-gate.md)).
- **The images are named `ghcr.io/rocketpadplatforms/cnms-postfix` and
  `ghcr.io/rocketpadplatforms/cnms-e2e-client`** (before `cnms/postfix`
  and `cnms/e2e-client`). Clusters without access to ghcr.io need a
  mirror.

### Network and operator

- **The operator no longer renders `cnms-no-port25-egress`.** Port-25
  egress for other pods is denied by an admin-owned
  `CiliumClusterwideNetworkPolicy`; start from the example in
  `config/network-policy/smtp-egress-deny` ([how-to](../how-to/block-direct-smtp-egress.md)).
  Delete the old per-namespace policies, which the operator no longer
  manages.
- **A `ClusterIssuer` in `spec.tls.issuerRef` needs the operator flag
  `--allow-cluster-issuers`.** Without it such a `MailServer` is
  `Ready=False, reason=ClusterIssuerNotAllowed`
  ([install and operate](../how-to/02-install-and-operate.mdx)).
