# ADR 0007: Merging operator-owned annotations and skipping no-op writes

- **Status:** Accepted
- **Date:** 2026-10-25

## Context

During Phase 2 development the generated Deployments (Rspamd, Valkey)
churned continuously: `metadata.generation` advanced ~5 times per second
and the kube deployment controller kept reacting, so the `MailServer`
`Ready` condition never settled. StatefulSets did not show the
behaviour.

Root cause, established by replaying the operator's captured update
requests against a kind API server (v1.37):

- The kube **deployment controller maintains the
  `deployment.kubernetes.io/revision` annotation on the Deployment
  object itself** (it is copied to the ReplicaSets; the StatefulSet
  controller keeps no such annotation on its object, which is why the
  StatefulSets were stable).
- The operator replaced the whole metadata annotation set of every
  managed object with its own (config-hash only), so each update
  **deleted the controller's revision annotation**.
- For Deployments, a change of annotations (like a spec change) bumps
  `metadata.generation` — the registry strategy compares spec *and*
  annotations in `PrepareForUpdate`. Each bump re-synced the deployment
  controller, which re-added the annotation (another main-resource
  write, another bump), and the operator's watch re-triggered it. A
  self-sustaining loop at the generation level.

A second, related behaviour: the operator's updates carried the
builder spec (without the API-server defaults such as
`progressDeadlineSeconds`, `strategy`, probe defaults). The API server
re-applies defaulting on every write, so the stored object always
differs from the builder spec in exactly those defaulted fields, and a
naive "spec changed?" check would write on every reconcile.

## Decision

1. **Merge instead of replace metadata maps.** `mergeOwnedFields`
   overlays the controller-owned labels and annotations onto the
   fetched object and preserves keys owned by other actors (the
   deployment controller's revision annotation, any user-set
   annotations). Owned keys always win.
2. **Skip no-op writes.** `mergeOwnedFields` reports whether an owned
   field actually changed (labels, annotations, and the spec via
   `equality.Semantic.DeepEqual`); `applyObject` skips the update when
   nothing changed, so a reconcile does not re-trigger itself through
   its own watch event.
3. **Rely on API-server write deduplication for the defaulting gap.**
   When the spec comparison does differ only in API-defaulted fields
   (stored object has `progressDeadlineSeconds: 600`, the builder spec
   does not), the operator still issues the update, but the API server
   applies defaulting and observes an identical object, so the write is
   a no-op: it returns 200 without advancing `resourceVersion` and
   without firing a watch event. Verified empirically against
   Kubernetes 1.37 (repeated byte-identical PUTs do not advance the
   resource version). No rollout is triggered, and the loop settles:
   nothing re-queues the operator.

   The builder does **not** set the defaulted fields explicitly; doing
   so would couple the manifests to the API server's defaults, which
   can change between Kubernetes versions. The current behaviour
   (one deduplicated no-op write per external change to the managed
   object) is cheap and version-robust.

## Consequences

- Deployments are stable: `generation` and `resourceVersion` are flat
  in steady state; `Ready` settles. Verified on kind: after deploying
  the fixed operator, `mail-valkey` kept `generation` and
  `resourceVersion` constant for 20+ seconds with the data plane
  running, and the revision annotation survived.
- Annotations set by other tools on managed objects (e.g. by GitOps or
  other controllers) are preserved by the operator.
- If the builder stops setting a label/annotation it used to set, the
  stale key is kept (merge, not sync). Accepted trade-off; the
  operator's key set is stable in v1.
- A unit test (`TestMergeOwnedFieldsDeployment`) pins the behaviour:
  a foreign annotation survives a reconcile, and a reconcile whose
  owned fields already match reports no change.
