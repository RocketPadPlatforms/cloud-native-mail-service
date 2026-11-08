# Operations

Runbooks and operating procedures:

- One runbook per alert (alert rules land in Phase 9)
- Queue rescue
- [Dovecot failover](#dovecot-failover) (RWO volume behaviour)
- [Purge a mailbox](#purge-a-mailbox)
- [End the sessions of a revoked account](#end-the-sessions-of-a-revoked-account)
- [A server is blocked by a limit](#a-server-is-blocked-by-a-limit)
- Restore drill (mailboxes, DKIM keys)

Added as the corresponding features land. See the [roadmap](../roadmap.md).

## Dovecot failover

Dovecot is one pod on one ReadWriteOnce volume. When the node it runs on fails,
**the pod is not replaced automatically**: a StatefulSet pod on an unreachable
node stays `Terminating` or `Unknown`, because the controller cannot know that
the old pod has stopped
([upstream](https://kubernetes.io/docs/tasks/run-application/force-delete-stateful-set-pod/)),
and the volume stays attached to the old node. Meanwhile IMAP is down, LMTP
delivery fails temporarily and Postfix keeps the mail in its queue; nothing is
lost ([failure modes](../concepts/failure-modes.md#dovecot)).

1. **Confirm that the node is really down**: shut down or powered off, not
   restarting. `kubectl get nodes` shows `NotReady`; check the machine itself.
   A volume that is force-detached while a workload on the node still uses it can
   be corrupted
   ([upstream](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/)).
2. **Release the pod**, with one of:
   - taint the node, which force-deletes its pods and detaches their volumes at
     once: `kubectl taint nodes <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute`.
     Remove the taint when the pod runs elsewhere and the node is back;
   - delete the Node object (`kubectl delete node <node>`), if the node is gone
     for good;
   - force-delete the pod: `kubectl delete pod <server>-dovecot-0 -n <ns>
     --force --grace-period=0`. The volume is still attached to the old node, so
     the new pod waits for the detach (the timeout below).
3. **Check the recovery.** The pod is `Running` and `Ready` on another node
   (`kubectl get pod <server>-dovecot-0 -n <ns> -o wide`), a client can log in, and
   the Postfix queue drains (`kubectl exec -n <ns> <server>-postfix-0 -c postfix
   -- postqueue -p`).

Where something deletes the Node objects of failed nodes by itself (a cloud
controller or a node autoscaler), the pod is replaced without your action after
about **11 minutes**: 300 s until the default toleration of
`node.kubernetes.io/unreachable` evicts the pod, then 6 minutes until Kubernetes
force-detaches a volume whose pod deletion has not finished. These numbers are
**derived from Kubernetes defaults, not verified on a cluster**; a cluster can
change both, and the force detach can be switched off. Without something that
deletes the Node, the pod stays where it is until you act. There is no startup
probe, and the pod keeps the default grace period.

## Purge a mailbox

The mailbox of a deleted account stays on the volume, and an account that gets
the same name later **inherits it**: a different person could read the previous
person's mail ([mailboxes](../how-to/mailboxes.md#delete-an-account)). The
operator never deletes mail, so removing a mailbox is a step you do, with a
one-off pod. **The removal is irreversible: a backup is the only way back.**

**Prerequisite:** the account is gone from **every** user source: the `MailUser`
deleted, the user removed from the directory, and not among the static test
users. Otherwise the account logs in again or the mailbox is created again by the
next delivery.

1. **Delete the account and wait until it has left the Dovecot files and the
   Postfix tables.** The live Dovecot Secret (the `passdb` and `userdb` files)
   reaches the pod after a minute or two (see [users and
   aliases](../how-to/users-and-aliases.md#open-imap-sessions)). Check that
   Dovecot no longer knows the account:
   `kubectl exec -n <ns> <server>-dovecot-0 -c dovecot -- doveadm user <login>`
   must answer `doesn't exist`. The Postfix tables follow with a validated rollout
   of the Postfix pods; wait until it is finished. Until then Postfix may still
   accept mail for the address; Dovecot answers `550 5.1.1 User doesn't exist`,
   and Postfix defers that mail instead of bouncing it ([users and
   aliases](../how-to/users-and-aliases.md)). Purge only after the account is out
   of the Dovecot files and the Postfix tables: while Dovecot still knows it, a
   delivery or a login creates the mailbox again.
2. **End its sessions**: `kubectl exec -n <ns> <server>-dovecot-0 -c dovecot --
   doveadm kick <login>` ([below](#end-the-sessions-of-a-revoked-account)). An open
   session survives the removal from the files.
3. **Remove the directory with a one-off pod that mounts the same volume.** The
   volume is ReadWriteOnce and the operator keeps the Dovecot StatefulSet at one
   replica, so scaling it to zero is undone. The pod must run **on the node of the
   Dovecot pod**: find it with `kubectl get pod <server>-dovecot-0 -n <ns> -o
   jsonpath='{.spec.nodeName}'`. The Dovecot image has no `rm`; use an image that
   has one. The Postfix image the operator already uses does:
   `kubectl get pod <server>-dovecot-0 -n <ns> -o
   jsonpath='{.spec.containers[?(@.name=="cert-reload")].image}'`. The files belong
   to uid and gid 1000, so the pod runs as **1000:1000** (or as root). In a
   container test without any `fsGroup` the home directories had mode `0700` and
   another uid was refused even with group 1000. On a cluster the `fsGroup` of the
   Dovecot pod is expected to add group read and write and the setgid bit to
   existing directories when the volume is mounted; the advice holds either way.

   ```yaml
   apiVersion: v1
   kind: Pod
   metadata:
     name: purge-alice
     namespace: <ns>
   spec:
     restartPolicy: Never
     nodeName: <node of the Dovecot pod>
     securityContext:
       runAsUser: 1000
       runAsGroup: 1000
       runAsNonRoot: true
       seccompProfile: {type: RuntimeDefault}
     containers:
       - name: purge
         image: <image from above>
         command: ["sh", "-c", "rm -rf /srv/mail/alice && ls /srv/mail"]
         securityContext:
           allowPrivilegeEscalation: false
           capabilities: {drop: ["ALL"]}
         volumeMounts:
           - {name: mail, mountPath: /srv/mail}
     volumes:
       - name: mail
         persistentVolumeClaim:
           claimName: mail-<server>-dovecot-0
   ```

   The pod sets the fields that a namespace enforcing the `restricted` Pod
   Security level asks for (`runAsNonRoot`, no privilege escalation, all
   capabilities dropped, a seccomp profile); a pod without them is rejected
   there. The container test ran without these fields, and `rm` by the owner of
   the files needs no capability, so they are expected to work; that is **not
   verified**. Write the login out in the command (`alice` here); do not build it
   from a variable that can be empty. `kubectl wait pod/purge-alice -n <ns>
   --for=jsonpath='{.status.phase}'=Succeeded --timeout=120s` and `kubectl logs
   purge-alice -n <ns>` show the homes that are left.
4. **Delete the pod**: `kubectl delete pod purge-alice -n <ns>`.
5. **Clear the Postfix queues before the name is reused.** Mail that Postfix
   accepted for the account before its tables rolled out stays deferred in the
   queue of the Postfix pod that took it (the `lmtp` service defers a permanent
   LMTP error, and the queue lifetime is the Postfix default of 5 days). If the
   name is created again in that time, that mail is delivered to the new account.
   On **every** Postfix replica (`<server>-postfix-0`, `-1`, ... as many as
   `spec.postfix.replicas`) look for the primary address of the account and remove
   what you find:

   ```sh
   kubectl exec -n <ns> <server>-postfix-<n> -c postfix -- postqueue -p
   kubectl exec -n <ns> <server>-postfix-<n> -c postfix -- postsuper -d <queue id>
   ```

   `postqueue -p` prints a `*` (active) or `!` (on hold) after the queue ID;
   `postsuper -d` needs the ID without it. Mail to an alias of the account is
   queued under the primary address. This step is **not verified on a cluster**.

To check the result, the log of step 3 no longer lists the login. If the account
is created again once no deferred mail for the old account is left in the queues,
its mailbox starts empty (no folders but INBOX, new UIDs).

In a container test with the pinned Dovecot 2.4.5 image and the CNMS Postfix
image on the same volume, removing `/srv/mail/<login>` worked as 1000:1000 and as
root and failed as 65534:1000 (the home directories were mode `0700`); an account
created again after the removal had an empty INBOX, and without the removal it saw
the old messages. **Not verified on a cluster:** the pod above, the `nodeName`
placement, the `fsGroup` of the volume, the Postfix queue commands of step 5, and
`doveadm user` through `kubectl exec`.

## End the sessions of a revoked account

Disabling or deleting a `MailUser`, or changing its password, removes the
login from the live Dovecot files once the kubelet has projected the Secret
(a minute or two, see [users and aliases](../how-to/users-and-aliases.md)).
Dovecot checks the password at login only, so **a session that is open stays
open**, and an IDLE session can last for days. After the revocation:

```sh
kubectl exec -n <ns> <server>-dovecot-0 -c dovecot -- doveadm kick <account name>
```

with the account name (the object name of the `MailUser`). On kind, `doveadm who`
and a `kick` of an account without a session answered through `kubectl exec` in
the pod. In a container test with the pinned image ([ADR
0016](../decisions/0016-mailbox-format-and-storage.md)), `kick` ended an open
IMAP session; that it ends one through `kubectl exec` in a pod is **not
verified**. If the session stays open, restart the Dovecot pod, which ends every
session and interrupts IMAP, LMTP and submission login for a moment (mail waits
in the Postfix queue):

```sh
kubectl rollout restart statefulset/<server>-dovecot -n <ns>
```

## A server is blocked by a limit

`kubectl get mailserver <server> -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'`
shows `Ready=False` with the reason `UserLimitExceeded`, `AddressLimitExceeded`
or `ConfigTooLarge`. The message has the counts, or the object that is too
large and its size. A Warning Event with the same reason is emitted when it
changes. The server is not broken: the revision that runs keeps serving, and a
change that adds something waits (see [limits](../how-to/users-and-aliases.md#limits)).

1. Read the message. With `UserLimitExceeded` the server has more than 1,000
   `MailUser`s; with `AddressLimitExceeded` more than 5,000 addresses (or an
   account with more than 32 aliases); with `ConfigTooLarge` a rendered object
   reached 1,000,000 bytes (the live Dovecot Secret is
   `<server>-dovecot-users`).
2. Remove accounts or aliases (or move accounts to another `MailServer`) until
   the numbers are under the limits. The next reconcile, within a few seconds of
   the change to a `MailUser` and otherwise within 30 seconds, applies the whole
   revision as usual: the held accounts and aliases appear, the validation Job
   runs and the pods roll.
3. While the block lasts, removing or disabling an account takes its login
   away, but not its address: mail for it is still accepted and delivered to the
   retained mailbox. Open IMAP sessions stay open (see above).

There is nothing to restart and nothing to delete: the state of the block is
the live Secret and the objects themselves, so a restart of the operator does
not change it. One thing the block does not do: it never creates the live
Secret `<server>-dovecot-users`. If that Secret is deleted while a limit
blocks, it stays missing until the block clears; a Dovecot pod that is
rescheduled meanwhile stays in `ContainerCreating` and mail waits in the
Postfix queue, so clear the block first (step 2).
