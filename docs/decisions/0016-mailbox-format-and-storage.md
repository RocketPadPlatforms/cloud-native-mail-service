# ADR 0016: Mailbox format and storage

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

What exists today (Dovecot 2.4.5, `internal/images/images.go`):

- IMAP, LMTP delivery and the mail volume are rendered. `dovecot.conf`
  sets `mail_driver = maildir` and `mail_path = ~/mail`; the home of an
  account is `/srv/mail/<name>` (`internal/render/dovecot/dovecotconf.go`,
  `internal/render/dovecot/users.go`).
- The Dovecot workload is a StatefulSet with one replica and one RWO
  volume claim `mail` (default 10Gi, immutable size, ADR 0010). The pod
  runs as vmail (uid and gid 1000) with `fsGroup` 1000
  (`internal/manifests/workloads.go`, `internal/manifests/security.go`).
  The volume carries the k8up include annotation when backups are on
  (`internal/manifests/manifests.go`).
- ADR 0015 left two points to this phase: what happens to the mailbox of a
  deleted `MailUser` (section 4.6), and the route of mail that Dovecot
  itself generates (section 4.4, "a Phase 4 decision").

`AGENTS.md` asks this ADR to choose the mailbox format with crash-consistent
snapshots in mind (section 4), to document the realistic failover time of
an RWO volume (section 4), and to choose a format that is safe for a
file-level backup of a live volume (section 8). Principle 8 applies: the
Dovecot 2.4.5 documentation was read for the formats (the
[Maildir](https://doc.dovecot.org/2.4.5/core/config/mailbox_formats/maildir.html),
[dbox](https://doc.dovecot.org/2.4.5/core/config/mailbox_formats/dbox.html)
and [quota](https://doc.dovecot.org/2.4.5/core/plugins/quota.html) pages,
2026-11-08) and the settings below were run on the pinned image (see
Verification).

## Decision

1. **Format: Maildir, with the Maildir++ layout rendered explicitly.** We
   keep `mail_driver = maildir`, `mail_path = ~/mail`, and add
   `mailbox_list_layout = maildir++` next to them, so that a change of the
   upstream default cannot move folders on an existing volume. Indexes and
   control files (`dovecot-uidlist`, `dovecot-keywords`,
   `dovecot-uidvalidity*`, `dovecot.index*`) stay where Dovecot puts them,
   inside the mailbox directories, and **are part of the backup**:
   `dovecot-uidlist` cannot be rebuilt without clients getting new UIDs,
   and losing `dovecot-keywords` loses keyword names (upstream, Maildir
   page, "Control Files"). fsync and locking keep their defaults: one
   replica and a block-storage RWO volume. NFS-backed volumes are not
   supported (the docs say so) and Dovecot's NFS settings are not
   rendered. The quota driver of the quota ADR (not yet written) must not
   be the filesystem (`fs`) quota: upstream warns that Dovecot cannot cope
   with failing writes to its control files, and `mail_control_path` would
   have to move them (Maildir page, "Control Files"; quota page, "Maildir").
   *Rejected:* `sdbox` and `mdbox`. Upstream's dbox page: dbox keeps message
   flags and keywords only in its index files, which "can't be regenerated
   without data loss", and mdbox also needs its map index; that is the
   problem for a copy of a live volume, and switching later needs a
   migration. Also rejected: the `fs` layout (it moves folders: on the same
   data it lists `.Sent`, not `Sent`, S1). Upstream also flags Maildir as
   best-effort for the Community Edition and "mainly viable for smaller
   installations"; that is the accepted price for a format whose files are
   plain and can be restored one by one.
2. **A file-level backup is a copy, not a point in time.** A restic file
   walk of a live Maildir is tolerated by the format, but it is not
   consistent: a message renamed from `new/` to `cur/` during the walk can be
   copied twice or not at all, and the uidlist, the indexes and the message
   files come from different moments. A CSI `VolumeSnapshot` is the
   crash-consistent option. It is **documented, not implemented**. What the
   restore does with the uidlist, `dovecot-uidvalidity*` and the indexes of
   a restored volume is decided with the backup ADR (not yet written), as
   is how k8up handles an RWO volume in the pinned version (`AGENTS.md`,
   section 8). Upstream warns that a message file restored
   under a name that is still in the uidlist is renamed with a log warning,
   and that deleting the uidlist gives messages new UIDs and clients
   download them again (neither run here).
3. **Failover is a manual step.** A StatefulSet pod on an unreachable node
   is never replaced by the controller
   ([force-delete page](https://kubernetes.io/docs/tasks/run-application/force-delete-stateful-set-pod/);
   [node shutdown page](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/)).
   The runbook action is: confirm that the node is down, then taint it
   `node.kubernetes.io/out-of-service`, or delete the Node object, or
   force-delete the pod. "Down" means shut down or powered off, not
   restarting: a force-detached volume that a workload on the node still
   uses can be corrupted (upstream, node shutdown page). Where something
   deletes Node objects by itself, the time to a new pod is about 11
   minutes (300 s default toleration plus the 6 minutes before the volume
   is force-detached; the node shutdown page names the 6 minutes and an
   optional switch that turns the force detach off).
   **These numbers are derived from Kubernetes defaults, not verified on
   a cluster.** No startup probe and the default grace period stay. If the
   quota change adds a Postfix policy check, its timeout applies while the
   pod is unreachable (decided in the quota ADR, not yet written).
4. **The mailbox is keyed by the login name; there is no automatic purge.**
   A `MailUser` that is deleted leaves `/srv/mail/<name>` (ADR 0015, 4.6). A
   `MailUser` created later **with the same name inherits that mailbox**: a
   different person could read the previous owner's mail. This is warned about
   in the API documentation, the delete how-to and the purge runbook. The purge
   is a runbook step the administrator runs on purpose (a pod that mounts
   the volume and removes the home); Phase 4 adds no purge API and the
   operator never deletes mail. A purge API, or any automation of the purge,
   stays parked without a phase. The "explicit purge operation" of ADR 0015,
   section 4.6, therefore means this runbook procedure.
5. **IMAP is not exposed by the API.** Documentation only: `kubectl
   port-forward`, or a Service the administrator writes that selects the
   Dovecot pod labels (`objectLabels` in `internal/manifests`). Clients
   connect to `spec.hostname`, because the certificate covers it. No new API
   field.
6. **Null-sender mail from Dovecot is not decided in Phase 4.** Phase 4
   renders no Sieve and no `submission_host`, so Dovecot generates no mail.
   The route for null-sender mail that Dovecot would generate (MDN,
   vacation) waits for the phase that adds Sieve. This closes ADR 0015's
   "a Phase 4 decision" as *not decided in Phase 4*; port 587 keeps
   refusing the null sender.
7. **`fsGroupChangePolicy: OnRootMismatch` is parked to Phase 9.** The
   ownership of a restored volume is unknown and no gain was measured.

## Consequences

- Pinning the layout is a no-op for volumes that exist (S1): the change keeps
  behaviour, so no migration is needed. The golden files of the
  mailbox-enabled configurations gain two lines, a comment and the setting.
- `doveconf -n`, the validation gate of ADR 0009, accepted
  `mailbox_list_layout = maildirpp` (S1); the runtime fails on it. The
  value is a constant of the renderer, covered by the golden file and a
  unit test.
- Everything on the mail volume is in the backup, so restored copies may
  disagree with themselves (decision 2); the backup ADR must say what the
  restore does about it.
- A recreated user name inherits the previous mailbox (decision 4). An
  administrator who wants a clean start purges first (S4: a recreated name
  without a purge saw the old messages; after a purge it started empty).
  Mail that Postfix accepted for the removed account before its tables
  rolled out stays deferred in the queue (the `lmtp` service defers a
  permanent error, `soft_bounce`, and the queue lifetime is the Postfix
  default of 5 days), and would be delivered to an account created again in
  that time. The runbook therefore clears the queues of every Postfix
  replica before the name is reused.
- `doveadm kick <user>` ends an open IMAP session (S3), which answers the
  open point of ADR 0015, section 4.7, for the container; running it in the
  pod is not verified.
- The Dovecot image has no `rm` (S2): the purge pod uses another image.
  Constraints for the purge runbook: the volume is RWO and the operator
  keeps the StatefulSet at one replica, so scaling it to 0 would be undone,
  and the purge pod has to run on the node of the Dovecot pod (a second pod;
  the Dovecot pod itself has no `rm`). The Postfix image sets no `USER`, so
  it runs as root unless told otherwise: `rm -rf` of a home worked as
  1000:1000 and as root and failed as 65534:1000, so the purge pod sets
  `runAsUser` and `runAsGroup` to 1000 (or root) (S4, in containers). The
  purge pod on a cluster is not verified, and a namespace that enforces the
  `restricted` Pod Security level needs the fields the runbook adds.
- Phase 9 gets the failover and restore aids: a shorter `tolerationSeconds`
  for the Dovecot pod, the CSI snapshot integration, `fsGroupChangePolicy`
  and the exclusion of index files from the backup (`docs/backlog.md`). A
  purge API, or any automation of the purge, stays parked without a phase:
  the operator never deletes mail.

## Verification

Run on 2026-11-08 against `dovecot/dovecot:2.4.5@sha256:43c14def08a57f25107593cbcf64a0a6fbae84a7b9de7aa038513c9b21e91914`
(the pin of `internal/images/images.go`, which is authoritative if it moves
on). `$I` below is that reference. The harness was **not committed**. For S1
and S3 one throw-away container ran the rendered configuration of
`internal/render/dovecot/testdata/mailboxes.dovecotconf.golden` with
these changes only: a self-signed certificate at `/etc/dovecot/ssl`;
`passdb` and `userdb` files in the form of `users.go` with one account `alice`
(`{PLAIN}` password; `userdb` home `/srv/mail/alice`); `tmpfs` mounts, owned
by uid 1000, on `/srv/mail`, `/var/run/dovecot` and `/tmp`; `--read-only`,
`--cap-drop ALL --cap-add SYS_CHROOT`. The `mailbox_list_layout` line is
the only change to the configuration file itself, and only where S1 says so.
S4 used the same configuration with a Docker volume in place of the `tmpfs`
at `/srv/mail`; its row says how.

| # | Question | How | Result |
|---|---|---|---|
| S1 | What layout does `mail_driver = maildir` use without a setting, how is it spelled, and does pinning change an existing volume? | `doveconf -a`, `doveconf -d` and `doveconf -n` in the container; `doveadm save -u alice -m INBOX` (message on stdin), `doveadm mailbox create -u alice Sent` and `Archive.2026`, then a recursive `sh` walk of `/srv/mail`; then `mailbox_list_layout = maildir++` added after `mail_path` in place, `doveadm reload`, `doveadm mailbox list`, an LMTP delivery with `nc` to port 31024, and the walk again. Variants through `doveadm -c`: values `Maildir++`, `MAILDIR++`, `maildirpp`, `fs` | Without the line: `doveconf -n` shows no layout; `doveconf -a` prints `mailbox_list_layout = fs` (the generic default); `doveconf -d` shows the driver default `maildir { mail_path = %{home}/Maildir, mailbox_list_layout = maildir++ }`. In effect it is Maildir++: INBOX in `mail/{cur,new,tmp}`, folders `mail/.Sent`, `mail/.Archive`, `mail/.Archive.2026`; creating `Archive/2026` fails with "Name must not have '/' characters". With the line: `doveconf -n` shows `mailbox_list_layout = maildir++`; the mailbox list and the set of paths are identical to the run without it (compared with message file names masked), and the LMTP delivery was `250 2.0.0 ... Saved` into the same INBOX. The pin is a **no-op**. Runtime accepts `maildir++`, `Maildir++` and `MAILDIR++` alike (use lowercase, the upstream spelling), `doveconf -n` accepts `maildirpp` and `doveadm` then fails with "Unknown driver name". `fs` on the same data lists `.Sent .Archive.2026 .Archive INBOX`: the folders are not the same. |
| S2 | Does the image have `sh` and `rm`, and which user does it run as? | `docker run --rm --read-only --entrypoint sh $I -c 'command -v rm; echo rc=$?'`; a loop over `/usr/bin`, `/bin` and `/usr/sbin`; `docker image inspect`; the `Uid` line of `/proc/self/status` | `sh` is `/usr/bin/sh` (dash). `rm`: `command -v` rc 127, and no `rm`, `find`, `ls`, `cat`, `id`, `sleep`, `head` in `/usr/bin`, `/bin`, `/usr/sbin`. There are `tar`, `curl`, `nc`, `openssl`. `Config.User` is `vmail`, uid and gid 1000; the entrypoint is `tini`. A purge pod cannot use this image. The CNMS Postfix image, run in S4, has `/usr/bin/rm` and runs as `0:0` (it sets no `USER`). |
| S3 | Does `doveadm kick` end an open IMAP session? | A second container with `openssl s_client -quiet -connect <container ip>:31993` fed `a1 LOGIN`, `a2 SELECT INBOX`, `a3 IDLE` and then held open; server lines logged with a host clock; `doveadm who` and `doveadm kick alice` with `docker exec` (as vmail) in the Dovecot container; the layout line of S1 was in effect | Login and `SELECT INBOX` worked, the server answered `+ idling` and `doveadm who` listed `alice 1 imap`. `doveadm kick alice` printed `1 connections kicked`, exit 0, in 0.08 s; the client received `* BYE Server shutting down.` at +0.07 s and the connection closed (the client container exited); `doveadm who` was empty. One session, one run. |
| S4 | The purge procedure end to end: a second container on the same volume removes `/srv/mail/<login>`, and a recreated name starts empty | The pinned Dovecot image (`dovecot/dovecot:2.4.5@sha256:43c14def08a57f25107593cbcf64a0a6fbae84a7b9de7aa038513c9b21e91914`), read-only, with the mailboxes golden configuration plus `{PLAIN}` passwd-file `passdb` and `userdb` for `alice` and `bob`; a Docker volume at `/srv/mail` whose root was prepared like `fsGroup` (`root:1000`, mode 2775); no `fsGroup` ownership change ran on its contents (a plain Docker volume has no kubelet); the CNMS Postfix image `ghcr.io/rocketpadplatforms/cnms-postfix:3.11.7-1` (digest `sha256:2d43db0661e41824877a5e05ea50d0b14ee5d0549737aeb953eac1dccab4d725`, has `/usr/bin/rm`, sets no `USER` so it runs as `0:0`) as the second container on the same volume. Steps: LMTP to `alice` on port 31024 with `nc`, `doveadm mailbox create` and `doveadm save`; `alice` removed from both files while an `openssl s_client` IDLE session was open, then an IMAP login, `doveadm auth test`, `doveadm user`, LMTP to `alice` and to `bob`; `doveadm kick alice`; `rm -rf /srv/mail/<login>` with `--user 1000:1000` (`alice`), `0:0` (a `cp -a` copy `carol`) and `65534:1000` (a copy `dave`); `alice` added again with a new password; separately `bob` removed from both files without a purge and added again | Homes are `/srv/mail/alice/mail/{cur,new,tmp,.Sent}` plus `dovecot-uidlist` and the index files, owned `1000:1000`, mode 2700. Removed user: the IMAP login gave `NO [AUTHENTICATIONFAILED]`, `doveadm user` said "doesn't exist", LMTP answered `550 5.1.1` for `alice` while `bob` still got `250`, and the IDLE session opened before stayed open. Kick: `1 connections kicked` and `* BYE Server shutting down.` 0.07 s later. `rm -rf` worked as 1000:1000 and as root (rc 0) and failed as 65534:1000 with `Permission denied` (the homes are mode 2700: owner `1000:1000`, the setgid bit inherited from the 2775 parent, no group permission bits); no ownership problems, `/srv/mail` stayed `0:1000` mode 2775. Recreated after the purge: INBOX only, `0 EXISTS`, no `Sent`, and a new LMTP delivery was the only message. Recreated without a purge: `bob` logged in with the new password and saw `* 1 EXISTS`, `Subject: b-first` (the inherit warning, verified). **Not run:** a cluster, the purge pod, the `nodeName` placement, `fsGroup` on a real PVC, the Postfix queue commands, `doveadm user` through `kubectl exec`. |

## Not verified

- The failover numbers of decision 3 on a cluster, the 300 s and the 6
  minutes.
- The CSI `VolumeSnapshot` option: no snapshot was taken.
- Client behaviour after a restore, and the upstream warnings quoted in
  decision 2 (the uidlist rename, new UIDs): decided and run with the
  backup ADR (not yet written).
- A live-copied `sdbox` or `mdbox` volume was not restored here; the
  reason against them is upstream's statement, not a run.
- How k8up handles an RWO volume in the pinned version (the backup ADR, not
  yet written).
- That `doveadm kick` through `kubectl exec` in the Dovecot pod ends an open
  session (S3 used `docker exec` in a plain container; row 41 of ADR 0015
  only showed that `doveadm who` and a `kick` without a session answer).
- The purge on a cluster: the pod of the runbook, its `nodeName` placement,
  the `fsGroup` of a real volume, the Pod Security fields, the Postfix queue
  commands and `doveadm user` through `kubectl exec`. S4 ran in containers.
- A Service of your own that selects the Dovecot pod for IMAP.
