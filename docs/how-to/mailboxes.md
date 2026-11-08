# Use mailboxes (IMAP)

:::note
API version `v1alpha1`. The decisions and the evidence behind them: [ADR
0016](../decisions/0016-mailbox-format-and-storage.md).
:::

With mailboxes enabled, Dovecot stores the mail that Postfix delivers over LMTP
and serves it over IMAP. Accounts come from the user source of the server, a
[directory](directories.md) or [`MailUser`s](users-and-aliases.md); mailboxes
add no resource of their own.

## Enable mailboxes

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailServer
metadata:
  name: mail
spec:
  mailboxes:
    enabled: true
    storage: 20Gi      # default 10Gi
```

- `spec.mailboxes.storage` is the size of the one ReadWriteOnce volume of
  Dovecot. It is **immutable** once set ([ADR
  0010](../decisions/0010-storage-immutability-and-scale-down.md)). To grow the
  volume, edit the PersistentVolumeClaim `mail-<server>-dovecot-0` with `kubectl
  edit pvc` on a StorageClass that allows expansion; Kubernetes never shrinks a
  volume (see [failure modes](../concepts/failure-modes.md#postfix-replicas)).
- Switching `mailboxes.enabled` on or off recreates the Dovecot StatefulSet; the
  volume stays.
- Without mailboxes, and with submission on, Dovecot runs in auth-only mode: no
  IMAP, no LMTP, no mail volume
  ([install and operate](02-install-and-operate.mdx)).

## Where the mail lives

The mail volume is mounted at `/srv/mail`. Each account has a home keyed by its
**login name** (the object name of a `MailUser`): `/srv/mail/<login>`. The format
is Maildir with the Maildir++ layout, which the operator sets explicitly:

```text
/srv/mail/alice/mail/cur, new, tmp      INBOX
/srv/mail/alice/mail/.Sent/cur, ...     the folder "Sent"
/srv/mail/alice/mail/dovecot-uidlist    control and index files, in the folders
```

The indexes and control files (`dovecot-uidlist`, `dovecot-keywords`,
`dovecot-uidvalidity*`, `dovecot.index*`) are part of the volume and of its
backup: a lost `dovecot-uidlist` gives the messages new UIDs and clients download
them again. NFS-backed volumes are not supported.

## Connect with an IMAP client

- **Ports:** 993 (implicit TLS) and 143 (plaintext until `STARTTLS`; `AUTH` is
  only offered after it) on the Service `<server>-dovecot`. The Service is of
  type `ClusterIP`: nothing outside the cluster can reach it until you expose it.
- **Name:** the certificate covers `spec.hostname` of the `MailServer` and
  nothing else, so a client must connect to that name.
- **Login:** the account name (`alice`) or the primary address
  (`alice@a.example`), in any case. An alias is not a login ([users and
  aliases](users-and-aliases.md)).

To try it, forward the port and make the hostname resolve to the local address
(an `/etc/hosts` entry for `127.0.0.1` will do):

```sh
kubectl port-forward -n <ns> service/<server>-dovecot 9993:993
openssl s_client -connect <hostname>:9993
```

To reach the server from outside, write a Service of your own: the operator
creates no Service that is reachable from outside for IMAP. This one selects the
Dovecot pod by the labels the operator sets on it; the container ports are named
`imap` and `imaps`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mail-imap
  namespace: <ns>
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/name: cloud-native-mail-service
    cnms.rpp.gmbh/mailserver: <server>      # the name of the MailServer
    app.kubernetes.io/component: dovecot
  ports:
    - {name: imaps, port: 993, targetPort: imaps}
    - {name: imap, port: 143, targetPort: imap}
```

Do not copy the CNMS labels (`cnms.rpp.gmbh/managed-by`,
`app.kubernetes.io/managed-by`) onto your Service. The ingress NetworkPolicy
`<server>-ingress` of the server already allows the IMAP container ports (31143
and 31993) from any source, so the Service needs no policy of its own. Point the
DNS name `spec.hostname` at the address of the Service. That the Service works
with your load balancer is yours to check; this page has not run it.

## Quota

`spec.mailboxes.quota` is accepted by the API (a quantity greater than zero), but
**it is not enforced**: no renderer reads it, Dovecot has no quota configured, and
no limit applies to a mailbox today. Size the volume for the sum of your mailboxes.

## Delete an account

:::warning
Deleting a `MailUser`, or removing a user from a directory, **does not delete the
mailbox**. It stays on the volume at `/srv/mail/<login>`, and **an account that is
created later with the same name gets that mailbox, with all the old mail**. If a
different person receives the name, that person can read the previous person's
mail. Purge the mailbox before the name is reused: [Purge a
mailbox](../operations/index.md#purge-a-mailbox). The operator never deletes mail
by itself, and there is no API to do it.
:::

## Backups

The Dovecot volume carries `k8up.io/backup: "true"` while `spec.backup.enabled` is
on (the default); see [backup annotations](02-install-and-operate.mdx#backup-annotations-k8up).
The schedule and the repository are yours. A file-level copy of a live Maildir is
tolerated by the format but is not a point in time ([ADR
0016](../decisions/0016-mailbox-format-and-storage.md)).

## When the node of Dovecot fails

IMAP is down until the pod runs elsewhere, and a pod on an unreachable node is not
replaced by itself. Postfix keeps the mail it cannot deliver in its queue. See
[Dovecot failover](../operations/index.md#dovecot-failover).
