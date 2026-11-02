# ADR 0010: Immutable storage sizes and no Postfix scale-down yet

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

The Postfix and Dovecot workloads are StatefulSets. Their
`volumeClaimTemplates` are immutable after creation, and a template
only applies to PVCs the StatefulSet creates afterwards: editing the
size of a template never resizes an existing volume.

Two gaps followed from that:

1. Changing `spec.postfix.queueStorage` or `spec.mailboxes.storage`
   made the controller delete and recreate the StatefulSet. For
   Postfix that closed port 25 on every replica at once (the delete
   bypasses the PodDisruptionBudget) and resized nothing.
2. Reducing `spec.postfix.replicas` deleted the highest-ordinal pods.
   Their deferred mail stays on the retained queue PVCs, which
   nothing serves or reports. The project plan requires the operator
   to move a replica's queue to the remaining replicas first
   (and the docs promised it), but that move is not implemented.

## Decision

1. **Storage sizes are immutable at admission.** CEL transition rules
   reject any change of `spec.postfix.queueStorage` and
   `spec.mailboxes.storage` once the field exists (the API defaults
   make the field present as soon as its block is). Volumes are
   resized by editing the PersistentVolumeClaims directly, which
   needs a StorageClass with expansion; Kubernetes never shrinks a
   volume.
2. **Decreasing `spec.postfix.replicas` is rejected at admission**
   ("decreasing postfix.replicas is not supported yet; the queue move
   is not implemented"). The rule compares effective values, so an
   unset field counts as the default of 2. Increasing is allowed.
3. **The controller recreates a StatefulSet only when the claim
   template names change.** That happens only when mailboxes are
   toggled (the Dovecot `mail` volume appears or disappears); the
   Postfix template never changes its name. Sizes are not compared
   and an existing StatefulSet keeps its own templates, so a size
   difference can neither delete a StatefulSet nor fail an update.
   The recreate is a background delete, which also removes the single
   Dovecot pod (no budget is bypassed); the PVCs stay and the new
   StatefulSet reuses them. A `Create` that meets the object still
   being deleted (`AlreadyExists`) is not an error: the StatefulSet
   watch triggers the next reconcile.
4. **The Valkey data PVC is a plain PVC** (Valkey is a Deployment),
   so its `valkey.storage` is not immutable: the controller applies a
   larger size to the PVC in place when the StorageClass allows
   expansion, and Kubernetes rejects a smaller one, which shows as
   `ApplyFailed`.

## Consequences

- A routine storage edit or replica decrease can no longer cause an
  inbound outage or strand queued mail; the API answers with a clear
  message instead.
- The queue move (scale-down) and an operator-driven PVC resize are
  parked in the backlog and the hardening phase (Phase 9, together
  with queue-rescue). The rules and the decreasing-replicas message
  are removed when the queue move lands.
- The rules are no migration concern: there is no release yet.
- Tests: envtest checks the CEL rules, and the template-change paths
  of `applyObject` are asserted through a client interceptor
  (envtest runs no garbage collector).
