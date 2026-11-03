# Block direct SMTP egress from other pods

:::note
API version `v1alpha1`. This is an optional, cluster-admin step for
clusters that use Cilium. It is not installed by the operator.
:::

A pod that opens a connection to port 25 of a mail server on the internet
leaves the node with the node's address. If a tenant workload does that,
the spam or the blocklist entry lands on the address the CNMS servers
share. CNMS therefore keeps its own mail flow on the Postfix pods only
and asks you to deny port 25 to the internet for every other pod.

## Why the operator does not generate this

A Kubernetes `NetworkPolicy` can only allow traffic. To forbid one port
for pods that already have other policies, the rule must be a *deny*, and
the standard API has none. The earlier approach (a per-namespace allow of
everything except port 25) re-opened egress that a tenant's own default
deny had closed, because `NetworkPolicy` objects add up, and a tenant
could exempt a pod by copying a label. The operator no longer renders
such a policy; the deny is a static manifest that an admin owns
([ADR 0006](../decisions/0006-network-policy-and-security-floor.md)).

## What the manifest does

`config/network-policy/smtp-egress-deny` contains a
`CiliumClusterwideNetworkPolicy` with `egressDeny` for TCP 25 to the
`world` entity (outside the cluster). Deny rules win over every allow
rule, including the `<server>-egress-smtp` policy the operator creates
for the Postfix pods, so the Postfix pods must not be selected by the
deny. The policy has two rules whose selectors are ORed:

1. every pod in a namespace that is **not** in your list of MailServer
   namespaces;
2. every pod in a listed namespace that is **not** a Postfix pod
   (`app.kubernetes.io/component` other than `postfix`).

Only Postfix pods in a listed namespace stay free to send. The deny keeps
`enableDefaultDeny` off for both directions, so it takes nothing but port
25 away and does not put any pod into default deny. Traffic to other pods
inside the cluster is not affected.

## Install

1. Copy `config/network-policy/smtp-egress-deny` into your GitOps
   repository.
2. Replace the placeholder namespaces `tenant-a` and `tenant-b` in
   **both** rules with every namespace that runs a `MailServer`. A
   namespace missing from the list loses outbound mail: its Postfix pods
   are denied like any other pod.
3. Apply it: `kubectl apply -k <your copy>`.

Keep the list in sync when a tenant gets or loses a `MailServer`, for
example by generating it from your tenant inventory.

## Verify

From a pod in a namespace that is not listed, and from a non-Postfix pod
in a listed namespace:

```sh
kubectl run smtp-probe --rm -it --restart=Never --image=<image with nc> \
  -n <namespace> -- nc -vz -w 5 mx.example.org 25
```

The connection must time out (Cilium drops denied packets silently). The
same command with port 587 must still connect. In a listed namespace,
check that the Postfix pod still delivers: `kubectl logs` of the pod
shows `status=sent` lines, and `cilium-dbg monitor --type drop` on its
node shows no drops for it. Use a test host you control, not a real
mail server.

## Limitations

- **Cilium only.** Other CNIs need their own deny. The vendor-neutral
  `ClusterNetworkPolicy` of the Kubernetes network-policy API
  (`v1alpha2`, alpha at the time of writing, replacing
  `AdminNetworkPolicy`) can express the same deny, but support in CNIs
  is still partial; revisit it when it is stable in your CNI.
- **The exemption is a label.** Inside a listed namespace, any pod that
  carries the Postfix label can send on port 25, and a tenant who may
  create pods there can set it. A tenant can neither add its namespace to
  the list (the policy is cluster-scoped) nor spoof the label outside a
  listed namespace, so the exposure is limited to namespaces that run a
  MailServer. Restrict who may create pods with that label in those
  namespaces (an admission policy, for example) if that matters for you.
  A ServiceAccount selector would not help: tenants can create
  ServiceAccounts with any name in their own namespace.
- **Host networking.** Pods with `hostNetwork: true` are not endpoints
  of the policy and are not covered. Forbid them with Pod Security
  admission (the `baseline` level does).
- **Sending address.** The exempt Postfix pods send from the node
  address until the egress-gateway work (Phase 7) gives each server its
  own address. The deny does not change that.

See also the [install and operate guide](02-install-and-operate.md).
