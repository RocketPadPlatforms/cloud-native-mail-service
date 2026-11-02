# ADR 0009: Config validation gate with immutable configuration

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

The rendered engine configuration must never replace a working one
unless the engine accepts it (project plan, "Rendering and rollout").
The first implementation validated in an init container of every
workload. That has three flaws:

- The pod template (and so the rollout) is already changed when the
  check runs. The new pods fail in `Init:Error`, but the StatefulSet
  or Deployment has already moved on; for a StatefulSet a pod that
  restarts later runs the new template.
- The ConfigMap was updated in place, so pods of the old revision
  that restart mid-rollout read the new configuration.
- `postfix check` only warns (exit 0) about unknown `main.cf`
  parameters; the warning for those appears in the `postconf -n`
  output on stderr ("unused parameter"). The gate passed them.

Verified against the pinned engines (Rspamd 4.2.3, Dovecot 2.4.5,
Postfix 3.11.7, Valkey 9.1.2):

- `rspamadm configtest` exits 1 on syntax errors and 0 on an unknown
  worker attribute (only logged). `--strict` also counts the deferred
  Valkey name resolution logs as errors, so it is not used.
- `doveconf -n` exits 89 for an unknown setting. It exits 0 without
  the certificate files and without the users file, so the Dovecot Job
  mounts only the ConfigMap.
- Valkey has no config-check flag; starting the server with the file
  and pinging it exits non-zero for a bad directive. This is a real
  syntax check, so the gate keeps it.
- Dovecot reads `ssl_server` certificate files when it loads its
  configuration; a failed reload keeps the old configuration.

## Decision

1. **Immutable, content-named configuration.** Each component's
   rendered files go into a ConfigMap named
   `<server>-<component>-<hash8>`, `immutable: true`; the hash is the
   SHA-256 of the rendered files. The Dovecot users file (password
   hashes) lives in an immutable Secret with the same naming, mounted
   at `/etc/dovecot-users` with mode 0440 (a file cannot be mounted
   into the read-only ConfigMap directory). The objects are owned by
   the `MailServer` and are created, never updated; `AlreadyExists`
   counts as success. Pod templates reference the hashed names, and
   carry the config hash as an annotation. That hash covers the
   ConfigMap files, the Secret files and the component's engine image,
   so an image bump is validated again with the new image.
2. **Validation Job before rollout.** For every component whose new
   hash differs from the hash on the running workload, the controller
   creates a Job `<server>-<component>-v-<hash8>` (owned by the
   `MailServer`, `backoffLimit: 0`, `ttlSecondsAfterFinished`, an
   `activeDeadlineSeconds`) that runs the component image against the
   new ConfigMap (the Job mounts no Secret):
   - Postfix: `postfix check`, then `postconf -n`; any `warning:` on
     stderr fails the Job. The queue directory is an `emptyDir`.
   - Rspamd: `rspamadm configtest -c /usr/share/rspamd/config/rspamd.conf`.
   - Dovecot: `doveconf -n`.
   - Valkey: start the server with the file, ping it, shut it down.
3. **All or nothing.** The workloads of a `MailServer` are applied only
   when every needed Job succeeded, also at first creation, because
   the components reference each other (a Postfix change may need the
   matching Dovecot change). Until then the existing workloads are not
   touched. A component that already runs the new hash is not
   validated again, so a Job removed by its TTL does not cause a
   re-validation, and reverting the spec to the running configuration
   needs none.
4. **Status.** `ConfigValidated` is `Unknown` (`ValidatingConfig`)
   while Jobs run, `True` (`Validated`) when the configuration is
   validated or already running, and `False` (`ConfigRejected`) when a
   Job failed. `Ready` takes the reason and the message of the first
   condition that is not `True`, so a rejected configuration shows as
   `Ready=False, reason=ConfigRejected` with the Job name and its
   failure reason; a Warning Event is emitted on a new rejection. The
   validator's output is in the Job's pod log.
5. **Cleanup after the rollout.** Once all workloads finished their
   rollout (observed generation, updated and ready replicas, equal
   StatefulSet revisions), the controller deletes the ConfigMaps,
   Secrets and Jobs of the `MailServer` that no workload pod template
   references and that are not part of the desired object set. Nothing
   is deleted while a rollout runs: pods of the previous revision can
   still be recreated from the old template. The cleanup runs only in a
   pass that applied the workloads (not while a validation runs or
   after a rejection), and it reads the pod templates uncached and
   requires them to carry the hash that pass applied. A rejected configuration's
   objects are in the desired set, so they stay (the failed Job is not
   re-run in a loop).
6. **Ready means rolled out.** `WorkloadsReady` additionally requires
   the desired template, the observed generation, all replicas updated
   and ready, and for StatefulSets equal current and update revisions.
   A Deployment past its progress deadline, or workloads that stay not
   ready for ten minutes, report `RolloutStalled`. A spec change
   restarts that ten-minute clock.
7. **RBAC.** The operator needs `create` and `delete` on Jobs
   (`batch`) and Secrets; ConfigMaps no longer need `update` or
   `patch`.

## Consequences

- A bad configuration is stopped before the pod template changes, and
  the old revision keeps a ConfigMap that cannot change under it.
- A new configuration takes the time of the slowest validation Job
  (seconds, plus the first image pull on a node) before the rollout
  starts.
- A failed Job stays until its TTL (one hour) removes it; the
  `MailServer` stays `ConfigRejected` for that configuration. To retry
  after a transient failure (for example an image pull problem), delete
  the Job.
- The gate checks what the engines check at config load. It does not
  catch a service that fails at runtime (for example a `master.cf`
  entry for a daemon that does not exist) or a certificate that cannot
  be loaded: a missing certificate file at startup is fatal for
  Dovecot, which is unchanged by this ADR.
- The operator reads the Job condition only; surfacing the validator
  output in the condition would need `pods/log` access (backlog).
- The validation of the Postfix queue (`postfix check`) runs against an
  empty `emptyDir`, so it cannot find problems with an existing queue.
- Job names add 19 characters to the `MailServer` name
  (`<server>-<component>-v-<hash8>`), so the API rejects `MailServer`
  names longer than 44 characters to keep every generated name within
  the 63-character label limit.
