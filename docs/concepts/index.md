# Concepts

Background on how CNMS works:

- [Resource model](resource-model.mdx) (current v1alpha1 resources)
- [Domain authorization](domain-authorization.md) (`MailDomainGrant`, fail-closed)
- [Architecture](architecture.md) (control plane, data plane, rendering and rollout)
- [Control plane vs data plane](control-vs-data-plane.md) (why mail flows when the operator is down)
- [Reputation isolation](reputation-isolation.md) (one mail system, one reputation)
- [Failure modes](failure-modes.md) (what happens when a component goes down)
- [DKIM signing and rotation](dkim-signing.md) (keys, the never-unsigned guard, rotation)
- IP pools and claims (arrives with the IP management phase)

See the [roadmap](../roadmap.md).
