# ADR 0002: v1alpha1 API design for MailServer, MailDomain, MailDirectory

- Status: Accepted (2026-10-25)
- Supersedes: none

## Context

Phase 1 defines the CRDs needed by Phases 2-5: `MailServer`, `MailDomain`,
`MailDirectory`, all in group `cnms.rpp.gmbh`, version `v1alpha1`. The API must
be small, engine-neutral (no Postfix syntax in field names), and designed with
the implementing phases in mind. `v1alpha1` is mutable: fields may be renamed
or removed before `v1beta1`.

## Decision

1. **Same-namespace references.** All cross-resource references (`directoryRef`,
   `domains`, `outbound.relay.relayRef`, `outbound.direct.ipPoolClaimRef`,
   secret refs) are name-only and resolve within the object's own namespace.
   No `namespace` fields; no cross-namespace RBAC.
2. **Outbound is exactly one of `direct` or `relay`** (CEL `has()` XOR).
   `direct.ipPoolClaimRef` is optional: without a claim, mail leaves via the
   pods' source addresses. Claims arrive with the IP management phase (7); the
   field exists now so the API does not churn.
3. **TLS via cert-manager.** `tls.issuerRef` is an `Issuer` or `ClusterIssuer`
   reference; the certificate secret defaults to `<name>-tls`. cert-manager is
   a hard requirement for a `MailServer` (documented, not optional per
   resource).
4. **Submission test users.** `submission.staticUsers.secretName` provides a
   static user source for Phase 2 (test environment) that overrides
   `directoryRef` for authentication. The secret format is defined by the auth
   renderer in Phase 2; the field is a test convenience and may change before
   `v1beta1`.
5. **Mailboxes are opt-in.** `mailboxes.enabled` defaults to `false`; with
   submission enabled and mailboxes disabled, Dovecot runs in auth-only mode.
6. **Workload tuning is optional with defaults.** Postfix replicas (2), queue
   storage (1Gi), Rspamd replicas (2), Valkey persistence (off), mail storage
   (10Gi), PDB minAvailable (1). Defaults match the HA requirements; single
   replicas are allowed for testing.
7. **DKIM rotation state lives in the `cnms.rpp.gmbh/rotation` annotation**
   (per the state rule: non-recomputable state must survive a restore without
   status). The spec only carries policy (interval, overlap).
8. **Status shape.** `observedGeneration` plus `metav1.Condition` slices only.
   One `Ready` condition per resource. `MailDomain.status.records` lists every
   required DNS record with its verification state; no per-message or
   unbounded data in status.
9. **Print columns.** `Ready` and `Age` on all three kinds; plus `Domain` on
   `MailDomain` and `Type` on `MailDirectory`.

## Notes and assumptions

- **CRD CEL semantics.** Optional fields that are not present in the object
  cannot be compared with `==`/`!= null` (CEL fails with `no such key`); use
  `has(self.field)`. Scalar fields compared against `null` additionally need
  `+nullable`; object fields are natively nullable. All CEL rules in this API
  follow that.
- **`spec.domains` is a plain atomic list** with a CEL uniqueness rule, not a
  set list: set lists of object items require explicit atomic map types and a
  plain list plus `self.all(x, self.filter(y, y == x).size() == 1)` is
  simpler.
- **`DirectoryType`** is a named string type with constants
  (`DirectoryTypeLDAP`, `DirectoryTypeSQL`); the `oidc` type is added with its
  phase.
- **Samples** in `config/samples` are validated against the generated CRDs in
  envtest (schema, defaults, CEL), plus negative tests for the key CEL rules.
- **API reference** is generated from the Go types with
  `crd-ref-docs@v0.3.0` (`make api-ref`) into `docs/reference/api.md`. The
  upstream repository is archived at v0.3.0; it is a build-time tool, pinned
  and Renovate-managed like the other Makefile tool versions.
