/*
Copyright 2026 Rocket Pad Platforms GmbH.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// MailServer status conditions (see the Phase 2 plan and ADR 0005).
const (
	// ConditionDomainsAuthorized is true when every MailDomain that
	// references the server passes the domain authorization check (ADR
	// 0003); it is also true, with reason NoDomains, when none does.
	ConditionDomainsAuthorized = "DomainsAuthorized"
	// ConditionHostnameAuthorized is true when the hostname of the
	// server is granted to its namespace and no other MailServer uses
	// it (ADR 0014). While it is false the server presents the
	// in-cluster name of its Postfix Service and keeps serving its
	// domains; Ready is false with the reason of this condition.
	ConditionHostnameAuthorized = "HostnameAuthorized"
	// ConditionDomainsListed is false when status.domains lists only
	// part of the MailDomains that reference the server. It is a
	// warning only: it never feeds into Ready.
	ConditionDomainsListed = "DomainsListed"
	// ConditionCertificateReady is true when the TLS certificate
	// Secret carries tls.crt and tls.key.
	ConditionCertificateReady = "CertificateReady"
	// ConditionWorkloadsReady is true when every managed workload has
	// all its replicas ready.
	ConditionWorkloadsReady = "WorkloadsReady"
	// ConditionObservability reports the Prometheus Operator
	// integration state (ADR 0005).
	ConditionObservability = "Observability"
	// ConditionClientIPPreserved is false when the inbound Service
	// hides client addresses from Postfix. It is a warning only: it
	// never feeds into Ready.
	ConditionClientIPPreserved = "ClientIPPreserved"
	// ConditionDedicatedSendingAddress is false when direct outbound
	// mail leaves from the node's address, which is shared with other
	// workloads (ADR 0002). It is a warning only: it never feeds into
	// Ready.
	ConditionDedicatedSendingAddress = "DedicatedSendingAddress"
	// ConditionNodeDrainable is false when the Postfix disruption
	// budget allows no voluntary disruption, so a node drain waits
	// until the budget or the replica count is changed. It is a
	// warning only: it never feeds into Ready.
	ConditionNodeDrainable = "NodeDrainable"
	// conditionReady rolls up all other conditions.
	conditionReady = "Ready"
)

// ReasonReady is the reason of a Ready condition that is True.
const ReasonReady = "Ready"

// Ready condition reasons set before the data plane renders.
const (
	// ReasonDirectoryNotFound is set when the referenced MailDirectory
	// does not exist.
	ReasonDirectoryNotFound = "DirectoryNotFound"
	// ReasonDirectoryInvalid is set when the referenced MailDirectory
	// cannot be rendered: a Secret is missing or incomplete, or the
	// filter is not valid. The previous revision keeps running.
	ReasonDirectoryInvalid = "DirectoryInvalid"
	// ReasonNoUserSource is set when the server has neither a
	// directoryRef nor static test users.
	ReasonNoUserSource = "NoUserSource"
	// ReasonUserSourceConflict is set when the server configures more
	// than one user source (ADR 0015, section 3). The previous
	// revision keeps running.
	ReasonUserSourceConflict = "UserSourceConflict"
	// ReasonRelayNotAvailable is set for relay outbound (Phase 6).
	ReasonRelayNotAvailable = "RelayNotAvailable"
	// ReasonIpPoolClaimNotAvailable is set for a direct outbound with
	// an ipPoolClaimRef (Phase 7).
	ReasonIpPoolClaimNotAvailable = "IpPoolClaimNotAvailable"
	// ReasonInsecureStaticUsersDisabled is set when
	// spec.submission.insecureStaticUsers is present but the operator
	// was started without --enable-insecure-test-features.
	ReasonInsecureStaticUsersDisabled = "InsecureStaticUsersDisabled"
	// ReasonStaticUsersInvalid is set when the users Secret is
	// missing or does not parse.
	ReasonStaticUsersInvalid = "StaticUsersInvalid"
	// ReasonApplyFailed is set when applying the data plane fails.
	ReasonApplyFailed = "ApplyFailed"
	// ReasonClusterIssuerNotAllowed is set when tls.issuerRef is a
	// ClusterIssuer and the operator runs without
	// --allow-cluster-issuers.
	ReasonClusterIssuerNotAllowed = "ClusterIssuerNotAllowed"
	// ReasonOwnershipConflict is set when an object the data plane
	// needs already exists and is controlled by someone else.
	ReasonOwnershipConflict = "OwnershipConflict"
)

// clusterIssuerKind is the cert-manager kind of a shared issuer.
const clusterIssuerKind = "ClusterIssuer"

// requeueInterval is how often a not-ready MailServer is re-checked
// (certificates are issued, workloads come up, CRDs are installed).
const requeueInterval = 30 * time.Second

// readyRequeueInterval is how often a ready MailServer is
// re-evaluated as a safety net behind the watches.
const readyRequeueInterval = 10 * time.Minute

// CRD names the operator discovers for optional integrations.
const (
	crdCertificate    = "certificates.cert-manager.io"
	crdServiceMonitor = "servicemonitors.monitoring.coreos.com"
)

// GVKs of the optional-integration objects (unstructured).
var (
	certificateGVK    = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	serviceMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"}
)

// MailServerReconciler reconciles a MailServer object: it authorizes
// the domains (ADR 0003), renders the engine configuration and
// manages the data plane (internal/manifests), and reports readiness
// through status conditions.
type MailServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// DomainAuthorization is the validated --domain-authorization
	// flag (allowlist or unrestricted, ADR 0003).
	DomainAuthorization domainAuthorizationMode

	// EnableInsecureTestFeatures accepts
	// spec.submission.insecureStaticUsers (Phase 2 test users).
	EnableInsecureTestFeatures bool

	// AllowClusterIssuers is the --allow-cluster-issuers flag: without
	// it a MailServer may only reference a namespaced Issuer, because
	// a shared ClusterIssuer would let any tenant obtain certificates
	// from it (ADR 0003).
	AllowClusterIssuers bool

	// APIReader reads without the cache (mgr.GetAPIReader()). It reads
	// what the cache must not hold or may lag behind: the tenant
	// Secrets (TLS, static users), the CRDs, the objects of someone
	// else with the name of a data-plane object (the cache only holds
	// the operator's own, CacheOptions), and the pod templates the
	// stale-object cleanup checks.
	APIReader client.Reader

	// Recorder emits Kubernetes Events on Ready transitions
	// (nil in tests).
	Recorder recorder.EventRecorder

	// Resolver looks the DNS records of the hostname up
	// (--dns-resolver). Without one no record is verified.
	Resolver dnsrecords.Resolver

	// Limits are the limits of ADR 0015, section 4.7; a zero field is the
	// value of the ADR. The specs set smaller ones.
	Limits addresses.Limits

	// Now is the clock of the DNS lookup schedule; nil means time.Now.
	// Tests advance a fake one.
	Now func() time.Time

	crds crdCache
	dns  dnsCache
}

// now returns the current time in UTC, truncated to the second like the
// times in the status.
func (r *MailServerReconciler) now() time.Time {
	t := time.Now()
	if r.Now != nil {
		t = r.Now()
	}
	return t.UTC().Truncate(time.Second)
}

// The controller never writes a MailServer itself, only its status. It uses no finalizer,
// but the controller references it sets (blockOwnerDeletion) need update on the
// finalizers subresource where the OwnerReferencesPermissionEnforcement plugin is on.
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers/status,verbs=patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailusers,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildirectories,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// ConfigMaps and Secrets of the data plane are immutable: created and deleted, never updated,
// except the live users Secret of Dovecot (ADR 0015, section 4.7), which is updated in place.
// Secrets are never watched or cached: the TLS and users Secrets are read by name, and
// the operator's own are listed (by label) for the cleanup of superseded ones and for the
// credential Secrets of the MailUsers.
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;create;update;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile moves the cluster state of one MailServer toward the
// desired state (Phase 2): authorization, rendering, apply and
// readiness reporting. It is idempotent and fail closed: a bad input
// never replaces a working data plane.
func (r *MailServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	server := &cnmsv1alpha1.MailServer{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		if errors.IsNotFound(err) {
			r.dns.forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	orig := server.DeepCopy()
	// A copy: setCondition updates the condition in place, and the Ready
	// transition Event compares against the state before.
	prevReady := meta.FindStatusCondition(server.Status.Conditions, conditionReady).DeepCopy()
	conds := &server.Status.Conditions
	generation := server.Generation
	// dnsDelay is when the records are looked up again; the re-check
	// of the server is never later.
	var dnsDelay time.Duration
	done := func() (ctrl.Result, error) {
		res, err := r.finish(ctx, server, orig, prevReady)
		if dnsDelay > 0 && res.RequeueAfter > 0 {
			res.RequeueAfter = min(res.RequeueAfter, dnsDelay)
		}
		return res, err
	}
	// block records a fail-closed Ready condition and finishes.
	block := func(reason, message string) (ctrl.Result, error) {
		setCondition(conds, generation, conditionReady, metav1.ConditionFalse, reason, message)
		return done()
	}
	// fail reports a failed apply and finishes. A lost race on the live users
	// Secret is no failure (another writer, for instance a second replica): the
	// pass is repeated after a second and Ready is not touched (ADR 0015,
	// section 4.7).
	fail := func(err error) (ctrl.Result, error) {
		if stderrors.Is(err, errLiveUsersRaced) {
			return ctrl.Result{RequeueAfter: liveUsersRetry}, nil
		}
		return block(r.reportApplyFailure(ctx, server, prevReady, err), err.Error())
	}

	// Domain authorization (ADR 0003) is evaluated first and on every
	// reconcile: no tenant-controlled check below may keep a revoked
	// domain from reaching the data plane.
	auth, domains, ev, err := r.authorize(ctx, server)
	if err != nil {
		return ctrl.Result{}, err
	}
	authReason, authMessage := auth.condition()
	// The records of the hostname are reported whatever else blocks the
	// render; they never feed into Ready.
	if dnsDelay, err = r.reconcileRecords(ctx, server, auth); err != nil {
		return ctrl.Result{}, err
	}

	// Anything that keeps the normal render from being applied: an
	// input the tenant controls is missing or invalid, or no domain is
	// authorized. A running data plane must then not keep serving a
	// revoked domain, so it gets the fail-closed render instead (no
	// domains, no users; fixed content that depends on no tenant
	// input beyond the server itself).
	blocked, src, err := r.preflight(ctx, server, len(auth.domains) > 0, domains, ev, auth.domains)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked == nil && len(auth.domains) == 0 {
		blocked = &blockedBy{authReason, authMessage}
	}
	setDomainsStatus(server, auth, blocked)
	r.setAddressesCondition(server, src.verdict)
	renderDomains, renderSrc := auth.domains, src
	failClosed := false
	if blocked != nil {
		need, exists, err := r.needsFailClosed(ctx, server, auth.domains)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !need {
			// The running pods stay as they are, but the Certificate
			// must not keep names that are no longer authorized (a
			// renamed or revoked hostname, or the domains an older
			// version put on it). A hostname change never fails the
			// domains closed (ADR 0014).
			reason, message, err := r.holdRunning(ctx, server, auth.hostname, blocked, src, exists, prevReady)
			if err != nil {
				return fail(err)
			}
			return block(reason, message)
		}
		// A domain is revoked as well (needsFailClosed): the fail-closed render
		// replaces the running one, and no limit applies to it (ADR 0015, section
		// 4.7), so the Ready message below is the one of the block without the
		// suffix of limitMessage: renderSrc is reset.
		failClosed, renderDomains, renderSrc = true, nil, userSource{}
	}

	// The signing keys of the domains that are rendered (ADR 0011). A
	// domain without a usable key is still served; its outbound mail
	// fails closed in the guard, so the rollout does not wait for it.
	keys, err := r.reconcileDKIMKeys(ctx, server, domains.Items, renderDomains)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Render and apply the data plane.
	certCRDs, err := r.crdsAvailable(ctx, crdCertificate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if failClosed && r.clusterIssuerRefused(server) {
		// Nothing may be requested from the refused issuer, and the
		// existing Certificate would keep renewing the revoked names.
		certCRDs = false
		if err := r.deleteCertificate(ctx, server); err != nil {
			return ctrl.Result{}, err
		}
	}
	prevValidated := meta.FindStatusCondition(*conds, ConditionConfigValidated)
	prevValidated = prevValidated.DeepCopy()
	g, rolloutWant, err := r.applyGuarded(ctx, server, renderDomains, auth.hostname, renderSrc, keys.keys, certCRDs, failClosed)
	if err != nil {
		return fail(err)
	}
	if blocked, err = r.recordGate(ctx, server, g, renderSrc, blocked, prevReady, prevValidated); err != nil {
		return fail(err)
	}

	// Readiness of the rendered objects.
	certStatus, certReason, certMessage := r.certificateReady(ctx, server)
	setCondition(conds, generation, ConditionCertificateReady, certStatus, certReason, certMessage)
	// A spec change starts a new rollout: the stall clock restarts, so
	// the not-ready time of an earlier rollout does not count.
	// shortcut: only a generation change restarts it, not an image bump
	// by an operator upgrade; track the config hash if that matters.
	prevRollout := meta.FindStatusCondition(*conds, ConditionWorkloadsReady)
	newRollout := prevRollout != nil && prevRollout.ObservedGeneration != generation
	if newRollout {
		prevRollout = nil
	}
	rollout := r.workloadsReady(ctx, server, rolloutWant, prevRollout)
	setCondition(conds, generation, ConditionWorkloadsReady,
		conditionStatus(rollout.ready), rollout.reason(), rollout.message)
	if newRollout && !rollout.ready {
		meta.FindStatusCondition(*conds, ConditionWorkloadsReady).LastTransitionTime = metav1.Now()
	}
	r.setAuxiliaryConditions(ctx, server)
	if g.want != nil && rollout.ready {
		// This pass applied the workloads and their rollout finished:
		// nothing runs the superseded configuration anymore. While a
		// validation runs or was rejected the workloads were not
		// applied, so a "ready" result describes an older revision.
		if err := r.cleanupStale(ctx, server, g.plane, g.want); err != nil {
			log.FromContext(ctx).Error(err, "Could not remove stale configuration objects")
		}
	}

	// DomainsAuthorized comes after the rollout conditions: a revoked
	// domain is still served until the new configuration is validated
	// and rolled out, so "partially authorized" must not hide that.
	reason, message := readyReason(*conds, []string{
		ConditionCertificateReady, ConditionConfigValidated,
		ConditionWorkloadsReady, ConditionDomainsAuthorized, ConditionHostnameAuthorized, ConditionObservability,
	})
	if blocked != nil {
		// The fail-closed render is applied, but the server is not
		// ready for what its spec asks for.
		reason, message = blocked.reason, blocked.message
	}
	setCondition(conds, generation, conditionReady,
		conditionStatus(reason == ReasonReady), reason, message)
	res, err := done()
	return keys.requeue(res), err
}

// authorize evaluates the hostname and the domains of the server (ADR
// 0003, 0014), records the verdicts in the DomainsAuthorized and
// HostnameAuthorized conditions and returns them with the MailDomains it
// read. The hostname is judged on its own: a failure leaves the domains
// alone.
func (r *MailServerReconciler) authorize(ctx context.Context, server *cnmsv1alpha1.MailServer) (authorization, *cnmsv1alpha1.MailDomainList, *authorizationEvaluator, error) {
	grants, domains, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return authorization{}, nil, nil, err
	}
	servers := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, servers); err != nil {
		return authorization{}, nil, nil, err
	}
	ev := &authorizationEvaluator{
		server:  server,
		grants:  grantRefs(grants),
		claims:  domainClaims(domains),
		servers: serverClaims(servers),
		mode:    r.DomainAuthorization,
	}
	auth := ev.Evaluate()
	conds, generation := &server.Status.Conditions, server.Generation

	reason, message := auth.condition()
	prev := meta.FindStatusCondition(*conds, ConditionDomainsAuthorized).DeepCopy()
	setCondition(conds, generation, ConditionDomainsAuthorized, conditionStatus(auth.all()), reason, message)
	r.emitAuthorizationEvent(server, auth, prev, reason, message)

	hostStatus, hostReason, hostMessage := auth.hostnameCondition()
	prev = meta.FindStatusCondition(*conds, ConditionHostnameAuthorized).DeepCopy()
	setCondition(conds, generation, ConditionHostnameAuthorized, hostStatus, hostReason, hostMessage)
	r.emitHostnameEvent(server, prev, hostStatus, hostReason, hostMessage)
	return auth, domains, ev, nil
}

// reportApplyFailure logs a failed apply and returns the reason of the
// Ready condition. A transition to not-ready already emits an Event; a
// conflict that appears while the server was not ready would not.
func (r *MailServerReconciler) reportApplyFailure(ctx context.Context, server *cnmsv1alpha1.MailServer, prevReady *metav1.Condition, err error) string {
	log.FromContext(ctx).Error(err, "Could not apply data plane")
	reason := applyFailureReason(err)
	if reason == ReasonOwnershipConflict && r.Recorder != nil && prevReady != nil &&
		prevReady.Status == metav1.ConditionFalse &&
		(prevReady.Reason != reason || prevReady.Message != err.Error()) {
		r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, reason, "ApplyDataPlane", "%s", eventMessage(err.Error()))
	}
	return reason
}

// setAuxiliaryConditions sets the conditions that are not about the
// rollout: the observability integration and the warnings.
func (r *MailServerReconciler) setAuxiliaryConditions(ctx context.Context, server *cnmsv1alpha1.MailServer) {
	conds, generation := &server.Status.Conditions, server.Generation
	obsStatus, obsReason, obsMessage := r.reconcileObservability(ctx, server)
	setCondition(conds, generation, ConditionObservability, obsStatus, obsReason, obsMessage)
	ipStatus, ipReason, ipMessage := clientIPPreserved(server)
	setCondition(conds, generation, ConditionClientIPPreserved, ipStatus, ipReason, ipMessage)
	sendStatus, sendReason, sendMessage := dedicatedSendingAddress(server)
	setCondition(conds, generation, ConditionDedicatedSendingAddress, sendStatus, sendReason, sendMessage)
	drainStatus, drainReason, drainMessage := nodeDrainable(server)
	setCondition(conds, generation, ConditionNodeDrainable, drainStatus, drainReason, drainMessage)
}

// applyGuarded applies the render for domains and users. If the
// workloads were not replaced (the new render is still validating or
// was rejected) while they serve a domain that is no longer
// authorized, it applies the fail-closed render instead of waiting. That
// fallback does not write the live users Secret: the users of the domains that
// are still authorized keep their logins (ADR 0015, section 4.6).
// The returned map holds the revision the rollout check waits for.
func (r *MailServerReconciler) applyGuarded(ctx context.Context, server *cnmsv1alpha1.MailServer, domains []string, hostname string, src userSource, keys []render.DomainKeys, certCRDs, failClosed bool) (*gate, map[string]string, error) {
	g, err := r.applyDataPlane(ctx, server, domains, hostname, src, keys, certCRDs, true)
	if le, ok := stderrors.AsType[*addresses.LimitError](err); ok {
		// The revision is too large: it is not applied, and a revoked domain
		// still gets the fail-closed render below.
		g, err = &gate{limit: le, message: le.Message}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if failClosed || g.want != nil {
		return g, g.want, nil
	}
	need, _, err := r.needsFailClosed(ctx, server, domains)
	if err != nil || !need {
		return g, nil, err
	}
	closed, err := r.applyDataPlane(ctx, server, nil, hostname, userSource{}, nil, certCRDs, false)
	if err != nil {
		return nil, nil, err
	}
	return g, closed.want, nil
}

// blockedBy is the reason the normal render cannot be applied.
type blockedBy struct{ reason, message string }

// preflight checks the tenant-controlled inputs of the render. A
// non-nil blockedBy means the normal render must not be applied. The
// static users are only loaded when needed (a fail-closed render has
// none).
func (r *MailServerReconciler) preflight(ctx context.Context, server *cnmsv1alpha1.MailServer, needUsers bool, domains *cnmsv1alpha1.MailDomainList, ev *authorizationEvaluator, authorized []string) (*blockedBy, userSource, error) {
	var src userSource
	staticUsers := server.Spec.Submission != nil && server.Spec.Submission.InsecureStaticUsers != nil
	mailUsers, err := r.serverMailUsers(ctx, server)
	if err != nil {
		return nil, src, err
	}
	// A server has one user source: never pick one silently.
	if sources := userSourceNames(server, len(mailUsers) > 0); len(sources) > 1 {
		return &blockedBy{ReasonUserSourceConflict, fmt.Sprintf(
			"the server configures more than one user source (%s); keep exactly one",
			strings.Join(sources, ", "))}, src, nil
	}
	// Fail closed when no user source is configured, or the
	// referenced MailDirectory is missing or invalid (recipient
	// validation and auth are backed by it).
	if ref := server.Spec.DirectoryRef; ref != nil {
		dir := &cnmsv1alpha1.MailDirectory{}
		err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: server.Namespace}, dir)
		if errors.IsNotFound(err) {
			return &blockedBy{ReasonDirectoryNotFound, fmt.Sprintf(
				"MailDirectory %q not found in namespace %q", ref.Name, server.Namespace)}, src, nil
		}
		if err != nil {
			return nil, src, err
		}
		// The directory is read with its Secrets (uncached) only for a
		// normal render.
		if needUsers {
			target, bad, err := directoryResolver{r.APIReader}.resolve(ctx, dir)
			if err != nil {
				return nil, src, err
			}
			if bad != nil {
				return &blockedBy{ReasonDirectoryInvalid, fmt.Sprintf(
					"MailDirectory %q is not valid: %s", ref.Name, bad.message)}, src, nil
			}
			src.directory = &render.DirectorySource{LDAP: target.ldap, SQL: target.sql}
		}
	} else if !staticUsers && len(mailUsers) == 0 {
		// No directory, no static users and no MailUser (ADR 0015, section
		// 3, point 7). shortcut: the service accounts of 3c will count too.
		return &blockedBy{ReasonNoUserSource,
			"spec.directoryRef is not set and no other user source exists; set directoryRef or create a MailUser"}, src, nil
	}
	// Outbound modes not implemented yet (fail closed).
	switch {
	case server.Spec.Outbound.Relay != nil:
		return &blockedBy{ReasonRelayNotAvailable, "relay outbound arrives in Phase 6"}, src, nil
	case server.Spec.Outbound.Direct != nil && server.Spec.Outbound.Direct.IPPoolClaimRef != nil:
		return &blockedBy{ReasonIpPoolClaimNotAvailable, "ipPoolClaimRef arrives in Phase 7"}, src, nil
	}
	// A shared ClusterIssuer needs the operator's opt-in (ADR 0003).
	if r.clusterIssuerRefused(server) {
		return &blockedBy{ReasonClusterIssuerNotAllowed, fmt.Sprintf(
			"tls.issuerRef %q is a ClusterIssuer, which the operator does not allow (start it with --allow-cluster-issuers, or use a namespaced Issuer)",
			server.Spec.TLS.IssuerRef.Name)}, src, nil
	}
	if !needUsers {
		return nil, src, nil
	}
	// Static test users (fail closed: invalid users must not render).
	users, reason, message, ok := r.staticUsers(ctx, server)
	if !ok {
		return &blockedBy{reason, message}, userSource{}, nil
	}
	if len(users) > 0 {
		src.input = render.AccountsInput(users, authorized)
		src.accounts, src.conflicted = render.DecideAccounts(users, authorized)
	}
	if len(mailUsers) > 0 {
		if src, err = r.mailUserSource(ctx, server, mailUsers, domains, ev); err != nil {
			return nil, src, err
		}
	}
	src.entries = accountEntries(src.accounts)
	// The counts of ADR 0015, section 4.7: over a cap the new revision is not
	// applied. The running one keeps serving; the live files follow the
	// revocation-only rule (keepRunning).
	if err := addresses.CheckLimitsWith(src.input, r.Limits); err != nil {
		if le, ok := stderrors.AsType[*addresses.LimitError](err); ok {
			src.limited = true
			return &blockedBy{le.Reason, le.Message}, src, nil
		}
		return nil, src, err
	}
	return nil, src, nil
}

// userSourceNames lists the user sources of the server, for the
// UserSourceConflict check: the ones the spec configures, and the MailUsers
// that name it (ADR 0015, section 3, point 7).
func userSourceNames(server *cnmsv1alpha1.MailServer, mailUsers bool) []string {
	var names []string
	if server.Spec.DirectoryRef != nil {
		names = append(names, "directoryRef")
	}
	if server.Spec.Submission != nil && server.Spec.Submission.InsecureStaticUsers != nil {
		names = append(names, "submission.insecureStaticUsers")
	}
	if mailUsers {
		names = append(names, "MailUsers")
	}
	return names
}

// userSource is where the accounts of a render come from: the static
// test users, a directory, or neither (the fail-closed render).
type userSource struct {
	accounts  []render.Account
	directory *render.DirectorySource
	// conflicted are the addresses that lost a conflict between the
	// accounts (addresses.Result.Conflicted).
	conflicted []string
	// verdict is the decision for the MailUsers of the server; nil for
	// any other user source.
	verdict *addresses.Result
	// input is the input of the address decision, for the limits of ADR 0015,
	// section 4.7. entries are the accounts as the engines carry them (the
	// desired state that Settle compares with the live files). limited is
	// set when a limit blocks the revision.
	input   addresses.Input
	entries []addresses.Entry
	limited bool
}

// needsFailClosed reports whether the running data plane must be
// replaced by the fail-closed render: it exists and either no domain
// is authorized (re-applying is idempotent) or it serves a domain that
// is no longer authorized. Only domains count: a renamed or revoked
// hostname never fails the domains closed (ADR 0014). exists reports
// whether a data plane was ever applied.
func (r *MailServerReconciler) needsFailClosed(ctx context.Context, server *cnmsv1alpha1.MailServer, authorized []string) (need, exists bool, err error) {
	served, exists, err := r.servedDomains(ctx, server)
	if err != nil {
		return false, false, err
	}
	if len(authorized) == 0 {
		return exists, exists, nil
	}
	return slices.ContainsFunc(served, func(d string) bool { return !slices.Contains(authorized, d) }), exists, nil
}

// keepRunning is what a blocked render still does to the running data
// plane: the Certificate loses names that are no longer authorized, and,
// when the last MailUser is gone (no user source) or a limit blocks the
// revision, a removed or disabled account loses its login while the Postfix
// revision stays (the revocation-only rule of ADR 0015, section 4.7). It returns
// the number of changes that wait.
func (r *MailServerReconciler) keepRunning(ctx context.Context, server *cnmsv1alpha1.MailServer, hostname string, blocked *blockedBy, src userSource) (held int, err error) {
	if err := r.reapplyCertificate(ctx, server, hostname); err != nil {
		return 0, err
	}
	switch {
	case blocked.reason == ReasonNoUserSource:
		return 0, r.revokeLogins(ctx, server)
	case src.limited:
		return r.settleLiveUsers(ctx, server, src.entries)
	}
	return 0, nil
}

// limitMessage is the message of Ready for a limit block: the message of the
// limit (counts or an object name), and what the block does. No address, no
// secret.
func limitMessage(message string, held int, running bool) string {
	if !running {
		return message + "; nothing is applied until it is fixed"
	}
	return fmt.Sprintf("%s; the running revision keeps serving, %d account changes wait, and the removal, the disabling and the new password of a login still apply", message, held)
}

// reapplyCertificate applies the Certificate alone, with the authorized
// hostname (empty: the in-cluster name), while the rest of the render
// is blocked. cert-manager then stops renewing names that are no
// longer authorized. It does nothing without the cert-manager CRDs. For
// a refused ClusterIssuer it deletes the Certificate the server owns
// instead: one made while the issuer was allowed would keep renewing
// names the namespace no longer owns.
func (r *MailServerReconciler) reapplyCertificate(ctx context.Context, server *cnmsv1alpha1.MailServer, hostname string) error {
	if r.clusterIssuerRefused(server) {
		return r.deleteCertificate(ctx, server)
	}
	installed, err := r.crdsAvailable(ctx, crdCertificate)
	if err != nil || !installed {
		return err
	}
	cert := manifests.Certificate(&render.RenderRequest{Server: server, Hostname: hostname})
	if err := applyObject(ctx, r.Client, r.APIReader, r.Scheme, server, cert); err != nil {
		return fmt.Errorf("apply %T %s: %w", cert, cert.GetName(), err)
	}
	return nil
}

// servedDomains returns the domains the existing workloads of the
// server serve, read from the served-domains annotation that every
// workload carries since the revision was applied (it is set together
// with the pod template, so it names the revision being rolled out; an
// older pod of a rollout in progress is not tracked). exists reports
// whether any workload or the Certificate exists, that is, whether a
// data plane was ever applied. The Certificate lists no domains (ADR
// 0014; one stored by an older version is replaced by the next
// reconcile), so only its presence counts.
func (r *MailServerReconciler) servedDomains(ctx context.Context, server *cnmsv1alpha1.MailServer) (served []string, exists bool, err error) {
	seen := map[string]bool{}
	for _, c := range render.AllComponents() {
		key := types.NamespacedName{Name: render.ServiceName(server.Name, c), Namespace: server.Namespace}
		var obj client.Object = &appsv1.Deployment{}
		if c == render.ComponentPostfix || c == render.ComponentDovecot {
			obj = &appsv1.StatefulSet{}
		}
		if err := r.Get(ctx, key, obj); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return nil, false, err
		}
		exists = true
		for d := range strings.SplitSeq(obj.GetAnnotations()[manifests.ServedDomainsAnnotation], ",") {
			if d != "" {
				seen[d] = true
			}
		}
	}
	// The Certificate is part of the data plane too, even before any
	// workload exists.
	cert, err := r.ownedCertificate(ctx, server)
	if err != nil {
		return nil, false, err
	}
	return slices.Sorted(maps.Keys(seen)), exists || cert != nil, nil
}

// clusterIssuerRefused reports whether the server references a shared
// ClusterIssuer the operator was not started to allow (ADR 0003).
func (r *MailServerReconciler) clusterIssuerRefused(server *cnmsv1alpha1.MailServer) bool {
	return server.Spec.TLS.IssuerRef.Kind == clusterIssuerKind && !r.AllowClusterIssuers
}

// deleteCertificate removes the Certificate of the server if it owns
// one.
func (r *MailServerReconciler) deleteCertificate(ctx context.Context, server *cnmsv1alpha1.MailServer) error {
	cert, err := r.ownedCertificate(ctx, server)
	if err != nil || cert == nil {
		return err
	}
	return client.IgnoreNotFound(r.Delete(ctx, cert))
}

// ownedCertificate returns the Certificate of the server, or nil when
// there is none, the cert-manager CRDs are not installed, or the
// object is not controlled by the server.
func (r *MailServerReconciler) ownedCertificate(ctx context.Context, server *cnmsv1alpha1.MailServer) (*unstructured.Unstructured, error) {
	installed, err := r.crdsAvailable(ctx, crdCertificate)
	if err != nil || !installed {
		return nil, err
	}
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	err = r.Get(ctx, types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, cert)
	if errors.IsNotFound(err) || (err == nil && !metav1.IsControlledBy(cert, server)) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cert, nil
}

// emitAuthorizationEvent records a Warning Event when domains became
// (or are still, with a new message) unauthorized.
func (r *MailServerReconciler) emitAuthorizationEvent(server *cnmsv1alpha1.MailServer, auth authorization, prev *metav1.Condition, reason, message string) {
	if r.Recorder == nil || auth.all() {
		return
	}
	if prev != nil && prev.Status == metav1.ConditionFalse && prev.Message == message {
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, reason, "AuthorizeDomains", "%s", eventMessage(message))
}

// emitHostnameEvent records a Warning Event when the hostname became
// (or is still, with a new message) unauthorized.
func (r *MailServerReconciler) emitHostnameEvent(server *cnmsv1alpha1.MailServer, prev *metav1.Condition, status metav1.ConditionStatus, reason, message string) {
	if r.Recorder == nil || status == metav1.ConditionTrue {
		return
	}
	if prev != nil && prev.Status == metav1.ConditionFalse && prev.Message == message {
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, reason, "AuthorizeHostname", "%s", eventMessage(message))
}

// clientIPPreserved reports whether Postfix sees the real client
// address. A LoadBalancer Service with externalTrafficPolicy Cluster
// source-NATs clients to a node address, so the per-client anvil rate
// limits (ADR 0006) then count every sender together and throttle
// all of them at once. The rate limits stay on; this is a warning.
func clientIPPreserved(server *cnmsv1alpha1.MailServer) (metav1.ConditionStatus, string, string) {
	in := server.Spec.Inbound
	if in != nil && in.ExternalTrafficPolicy != nil && *in.ExternalTrafficPolicy == "Cluster" &&
		(in.ServiceType == nil || *in.ServiceType == string(corev1.ServiceTypeLoadBalancer)) {
		return metav1.ConditionFalse, "ClusterTrafficPolicy",
			"inbound.externalTrafficPolicy is Cluster: client addresses are source-NATed, so the per-client rate limits apply to all senders together; use Local to preserve them"
	}
	return metav1.ConditionTrue, "Preserved",
		"client addresses reach Postfix unchanged for postscreen and rate limits"
}

// dedicatedSendingAddress reports whether the outbound mail has its own
// address: direct delivery without an IP pool claim leaves from the
// node address, so its reputation is shared with everything else on
// the node.
func dedicatedSendingAddress(server *cnmsv1alpha1.MailServer) (metav1.ConditionStatus, string, string) {
	if d := server.Spec.Outbound.Direct; d != nil && d.IPPoolClaimRef == nil {
		return metav1.ConditionFalse, "NodeSourceAddress",
			"direct outbound without ipPoolClaimRef sends from the node address, which other workloads share, so the sender reputation is shared; set outbound.direct.ipPoolClaimRef or use a relay"
	}
	return metav1.ConditionTrue, "Dedicated", "outbound mail has its own sending address or goes through a relay"
}

// nodeDrainable reports whether the Postfix disruption budget lets a
// node drain evict a pod: the budget needs at least one replica more
// than it keeps available.
func nodeDrainable(server *cnmsv1alpha1.MailServer) (metav1.ConditionStatus, string, string) {
	replicas := manifests.PostfixReplicas(server)
	minAvailable := manifests.PostfixMinAvailable(server)
	// An invalid percentage cannot get past the API; treat it as drainable.
	keep, err := intstr.GetScaledValueFromIntOrPercent(&minAvailable, int(replicas), true)
	if err == nil && keep >= int(replicas) {
		return metav1.ConditionFalse, "BudgetBlocksDrain", fmt.Sprintf(
			"postfixMinAvailable %s keeps all %d Postfix replicas available, so evicting a pod is never allowed and a node drain waits; add replicas or lower postfixMinAvailable",
			minAvailable.String(), replicas)
	}
	return metav1.ConditionTrue, "Drainable", "the Postfix disruption budget allows evicting one pod at a time"
}

// setValidationCondition records the validation gate state and emits
// a Warning Event when a configuration is newly rejected.
func (r *MailServerReconciler) setValidationCondition(server *cnmsv1alpha1.MailServer, g *gate, prev *metav1.Condition) {
	status, reason := metav1.ConditionTrue, ReasonConfigValidated
	switch g.state {
	case validationRunning:
		status, reason = metav1.ConditionUnknown, ReasonValidatingConfig
	case validationRejected:
		status, reason = metav1.ConditionFalse, ReasonConfigRejected
	case validationPassed:
	}
	setCondition(&server.Status.Conditions, server.Generation, ConditionConfigValidated, status, reason, g.message)
	if status == metav1.ConditionFalse && r.Recorder != nil &&
		(prev == nil || prev.Status != status || prev.Message != g.message) {
		r.Recorder.Eventf(server, nil, corev1.EventTypeWarning,
			ReasonConfigRejected, "ValidateConfig", "%s", eventMessage(g.message))
	}
}

// finish persists the status as a patch against orig (the object as
// read), so a write of the spec or metadata since then does not make
// it conflict. It emits a Ready transition event and schedules the
// re-check of the server.
func (r *MailServerReconciler) finish(ctx context.Context, server, orig *cnmsv1alpha1.MailServer, prevReady *metav1.Condition) (ctrl.Result, error) {
	server.Status.ObservedGeneration = server.Generation
	if err := r.Status().Patch(ctx, server, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}
	r.emitReadyEvent(server, prevReady)
	ready := meta.FindStatusCondition(server.Status.Conditions, conditionReady)
	if ready.Status == metav1.ConditionFalse {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	// A ready server is re-checked too: authorization depends on
	// objects the watches might miss, and a revocation must not wait
	// for the cache resync.
	return ctrl.Result{RequeueAfter: readyRequeueInterval}, nil
}

// emitReadyEvent records an Event when the Ready condition changes.
func (r *MailServerReconciler) emitReadyEvent(server *cnmsv1alpha1.MailServer, prev *metav1.Condition) {
	if r.Recorder == nil {
		return
	}
	ready := meta.FindStatusCondition(server.Status.Conditions, conditionReady)
	if prev != nil && prev.Status == ready.Status {
		return
	}
	if ready.Status == metav1.ConditionTrue {
		r.Recorder.Eventf(server, nil, corev1.EventTypeNormal,
			ready.Reason, "BecameReady", "%s", eventMessage(ready.Message))
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeWarning,
		ready.Reason, "BecameNotReady", "%s", eventMessage(ready.Message))
}

// maxEventMessage is the longest Event message the operator emits:
// messages embed text from outside (validator output, object names),
// and the API server limits the note of an Event to 1 kB.
const maxEventMessage = 1024

// eventMessage truncates msg to maxEventMessage bytes, ending in "..."
// when it cuts, without splitting a UTF-8 character.
func eventMessage(msg string) string {
	if len(msg) <= maxEventMessage {
		return msg
	}
	return strings.ToValidUTF8(msg[:maxEventMessage-3], "") + "..."
}

// listAuthorizationInput lists the grants and claims the evaluator
// needs (ADR 0003: a pure function of the current objects).
func listAuthorizationInput(ctx context.Context, c client.Reader) (*cnmsv1alpha1.MailDomainGrantList, *cnmsv1alpha1.MailDomainList, error) {
	grants := &cnmsv1alpha1.MailDomainGrantList{}
	if err := c.List(ctx, grants); err != nil {
		return nil, nil, err
	}
	domains := &cnmsv1alpha1.MailDomainList{}
	if err := c.List(ctx, domains); err != nil {
		return nil, nil, err
	}
	return grants, domains, nil
}

// staticUsers loads and validates the Phase 2 test users from
// spec.submission.insecureStaticUsers. It returns ok=false with a
// Ready-condition reason when the gate must block rendering.
func (r *MailServerReconciler) staticUsers(ctx context.Context, server *cnmsv1alpha1.MailServer) ([]render.Account, string, string, bool) {
	ref := server.Spec.Submission
	if ref == nil || ref.InsecureStaticUsers == nil {
		return nil, "", "", true
	}
	if !r.EnableInsecureTestFeatures {
		return nil, ReasonInsecureStaticUsersDisabled,
			"spec.submission.insecureStaticUsers is set but the operator was started without --enable-insecure-test-features", false
	}
	secret := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{
		Name: ref.InsecureStaticUsers.SecretName, Namespace: server.Namespace,
	}, secret)
	if errors.IsNotFound(err) {
		return nil, ReasonStaticUsersInvalid,
			fmt.Sprintf("insecureStaticUsers Secret %q not found", ref.InsecureStaticUsers.SecretName), false
	}
	if err != nil {
		return nil, ReasonStaticUsersInvalid, err.Error(), false
	}
	data, ok := secret.Data["users.yaml"]
	if !ok {
		return nil, ReasonStaticUsersInvalid,
			fmt.Sprintf("insecureStaticUsers Secret %q is missing the users.yaml key", ref.InsecureStaticUsers.SecretName), false
	}
	users, err := render.ParseStaticUsers(data)
	if err != nil {
		return nil, ReasonStaticUsersInvalid, err.Error(), false
	}
	return users, "", "", true
}

// workloadHashes maps each workload name to the config hash of its
// desired pod template.
func workloadHashes(objs []client.Object) map[string]string {
	want := map[string]string{}
	for _, obj := range objs {
		switch w := obj.(type) {
		case *appsv1.StatefulSet:
			want[w.Name] = w.Spec.Template.Annotations[manifests.ConfigHashAnnotation]
		case *appsv1.Deployment:
			want[w.Name] = w.Spec.Template.Annotations[manifests.ConfigHashAnnotation]
		}
	}
	return want
}

// certificateReady reports whether the TLS certificate Secret carries
// tls.crt and tls.key (the Secret is the source of truth for the
// in-place-reloaded TLS mounts). The Secret is not watched: a missing
// one is picked up by the re-check of a not-ready server, and a
// renewal is picked up by the reload sidecars, not by a reconcile.
func (r *MailServerReconciler) certificateReady(ctx context.Context, server *cnmsv1alpha1.MailServer) (metav1.ConditionStatus, string, string) {
	name := manifests.TLSSecretName(server)
	secret := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, secret)
	if errors.IsNotFound(err) {
		return metav1.ConditionFalse, "SecretNotFound",
			fmt.Sprintf("the TLS certificate Secret %q does not exist yet", name)
	}
	if err != nil {
		return metav1.ConditionFalse, "SecretReadError", err.Error()
	}
	for _, key := range []string{manifests.TLSCertFile, manifests.TLSKeyFile} {
		if _, ok := secret.Data[key]; !ok {
			return metav1.ConditionFalse, "SecretIncomplete",
				fmt.Sprintf("the TLS certificate Secret %q is missing %s", name, key)
		}
	}
	return metav1.ConditionTrue, "Issued", "the TLS certificate is available"
}

// reconcileObservability manages the Prometheus Operator
// ServiceMonitors per spec.observability.prometheusOperator
// (ADR 0005) and returns the ConditionObservability state.
func (r *MailServerReconciler) reconcileObservability(ctx context.Context, server *cnmsv1alpha1.MailServer) (metav1.ConditionStatus, string, string) {
	avail, err := r.crdsAvailable(ctx, crdServiceMonitor)
	if err != nil {
		return metav1.ConditionFalse, "CRDCheckFailed", err.Error()
	}
	setting := "auto"
	if o := server.Spec.Observability; o != nil && o.PrometheusOperator != nil {
		setting = *o.PrometheusOperator
	}
	var want []client.Object
	switch setting {
	case "enabled":
		if !avail {
			return metav1.ConditionFalse, "CRDsRequired",
				"prometheusOperator=enabled but the ServiceMonitor CRDs are not installed"
		}
		want = manifests.ServiceMonitors(server)
	case "disabled":
		// No monitors; existing ones are cleaned up below.
	default: // auto
		if !avail {
			return metav1.ConditionTrue, "CRDsUnavailable",
				"the ServiceMonitor CRDs are not installed; metrics stay exposed on the component endpoints"
		}
		want = manifests.ServiceMonitors(server)
	}
	if avail {
		got := &unstructured.UnstructuredList{}
		got.SetAPIVersion("monitoring.coreos.com/v1")
		got.SetKind("ServiceMonitorList")
		// Only monitors this server created are candidates: the
		// instance label alone also matches a Helm release or
		// another tool that uses the same name.
		if err := r.List(ctx, got,
			client.InNamespace(server.Namespace),
			client.MatchingLabels{
				manifests.InstanceLabelKey: server.Name,
				manifests.ManagedByLabel:   manifests.ManagedByValue,
				manifests.ServerLabel:      server.Name,
			},
		); err != nil {
			return metav1.ConditionFalse, "ListFailed", err.Error()
		}
		for i := range got.Items {
			if !metav1.IsControlledBy(&got.Items[i], server) || slices.ContainsFunc(want, func(w client.Object) bool {
				return w.GetName() == got.Items[i].GetName()
			}) {
				continue
			}
			if err := r.Delete(ctx, &got.Items[i]); err != nil && !errors.IsNotFound(err) {
				return metav1.ConditionFalse, "DeleteFailed", err.Error()
			}
		}
	}
	for _, obj := range want {
		if err := applyObject(ctx, r.Client, r.APIReader, r.Scheme, server, obj); err != nil {
			return metav1.ConditionFalse, applyFailureReason(err), err.Error()
		}
	}
	if len(want) > 0 {
		return metav1.ConditionTrue, "MonitorsCreated",
			fmt.Sprintf("%d ServiceMonitors are managed", len(want))
	}
	return metav1.ConditionTrue, "Disabled", "no ServiceMonitors are created (prometheusOperator=disabled)"
}

// crdsAvailable reports whether the CRD with the given plural name is
// installed (cached, see crdCache).
func (r *MailServerReconciler) crdsAvailable(ctx context.Context, name string) (bool, error) {
	return r.crds.installed(ctx, r.APIReader, name)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MailServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK)
	monitor := &unstructured.Unstructured{}
	monitor.SetGroupVersionKind(serviceMonitorGVK)
	b := ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailServer{}).
		Named("mailserver").
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&networkingv1.NetworkPolicy{})
	// Optional integrations (principle 10): watch the objects of a CRD
	// only when the CRD is installed; a watch on a GVK whose CRD is
	// missing fails the cache sync and the manager exits. The watch is
	// a trigger optimization only; when the CRD is installed later the
	// reconciler picks the objects up at the next CRD re-check.
	if crdInstalled(mgr, crdCertificate) {
		b = b.Owns(certificate)
	}
	if crdInstalled(mgr, crdServiceMonitor) {
		b = b.Owns(monitor)
	}
	return b.
		Watches(&cnmsv1alpha1.MailDomain{}, handler.EnqueueRequestsFromMapFunc(r.mapDomainServers)).
		Watches(&cnmsv1alpha1.MailDirectory{}, handler.EnqueueRequestsFromMapFunc(r.mapDirectoryServers)).
		// The accounts of the server. No generation predicate: a status write,
		// such as a new credentialRevision (a changed password), must reach the
		// server, which cannot see the Secrets change (ADR 0015, section 7).
		// The request waits mailUserDebounce so that a burst is one reconcile.
		Watches(&cnmsv1alpha1.MailUser{}, debounced(r.mapUserServer, mailUserDebounce)).
		Watches(&cnmsv1alpha1.MailDomainGrant{}, handler.EnqueueRequestsFromMapFunc(r.mapGrantServers)).
		// The hostname is unique across namespaces: a server that takes
		// it, renames or goes away blocks or frees the other claimants.
		// Status writes do not change it.
		Watches(&cnmsv1alpha1.MailServer{}, handler.EnqueueRequestsFromMapFunc(r.mapPeerServers),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// mapPeerServers enqueues the MailServers that use the same hostname as
// obj, in every namespace. For a renamed server the handler also maps
// the old object, so the claimants of the old name are freed.
func (r *MailServerReconciler) mapPeerServers(ctx context.Context, obj client.Object) []reconcile.Request {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok || s.Spec.Hostname == "" {
		return nil
	}
	peers := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, peers, client.MatchingFields{mailServerHostnameIndex: s.Spec.Hostname}); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailServers", "hostname", s.Spec.Hostname)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(peers.Items))
	for i := range peers.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&peers.Items[i])})
	}
	return reqs
}

// mapDirectoryServers enqueues the MailServers of the directory's
// namespace that reference it by name (a directory change may change
// the render; ADR 0015, section 7).
func (r *MailServerReconciler) mapDirectoryServers(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{mailServerDirectoryIndex: obj.GetName()}); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailServers", "directory", obj.GetName())
		return nil
	}
	return serverRequests(list)
}

// mapDomainServers enqueues the MailServer the MailDomain references
// and the servers referenced by every MailDomain claiming the same
// domain: the uniqueness check is cluster wide, so a new, changed or
// deleted claim can block or free the other claimants. For a changed
// spec.serverRef or spec.domain the handler also maps the old object,
// so a domain that moves reaches both of its servers (the old one
// revokes it); a deleted domain reaches the server it was attached to.
func (r *MailServerReconciler) mapDomainServers(ctx context.Context, obj client.Object) []reconcile.Request {
	d, ok := obj.(*cnmsv1alpha1.MailDomain)
	if !ok {
		return nil
	}
	servers := map[types.NamespacedName]bool{}
	add := func(md *cnmsv1alpha1.MailDomain) {
		if md.Spec.ServerRef.Name != "" {
			servers[types.NamespacedName{Namespace: md.Namespace, Name: md.Spec.ServerRef.Name}] = true
		}
	}
	add(d)
	peers := &cnmsv1alpha1.MailDomainList{}
	if err := r.List(ctx, peers, client.MatchingFields{mailDomainDomainIndex: d.Spec.Domain}); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailDomains", "domain", d.Spec.Domain)
	}
	for i := range peers.Items {
		add(&peers.Items[i])
	}
	// A MailDomain of the hostname itself carries its SPF record (ADR
	// 0014): the servers of the namespace with that hostname report
	// records accordingly.
	hosts := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, hosts, client.InNamespace(d.Namespace), client.MatchingFields{mailServerHostnameIndex: d.Spec.Domain}); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailServers", "hostname", d.Spec.Domain)
	}
	for i := range hosts.Items {
		servers[client.ObjectKeyFromObject(&hosts.Items[i])] = true
	}
	reqs := make([]reconcile.Request, 0, len(servers))
	for _, key := range slices.SortedFunc(maps.Keys(servers), func(a, b types.NamespacedName) int {
		return strings.Compare(a.String(), b.String())
	}) {
		reqs = append(reqs, reconcile.Request{NamespacedName: key})
	}
	return reqs
}

// mapGrantServers enqueues every MailServer: a grant decides who owns
// a domain for all namespaces (the owner of a subdomain carve-out, the
// namespace before and after a change), so any server may be
// affected. A reconcile is cheap and the object count is small.
func (r *MailServerReconciler) mapGrantServers(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.serversInNamespace(ctx, metav1.NamespaceAll)
}

func (r *MailServerReconciler) serversInNamespace(ctx context.Context, ns string) []reconcile.Request {
	list := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, list, client.InNamespace(ns)); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailServers", "namespace", ns)
		return nil
	}
	return serverRequests(list)
}

// serverRequests turns a list of MailServers into reconcile requests.
func serverRequests(list *cnmsv1alpha1.MailServerList) []reconcile.Request {
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Name: list.Items[i].Name, Namespace: list.Items[i].Namespace,
		}})
	}
	return reqs
}

// ownershipConflictError reports that an object a controller needs
// already exists and is not controlled by its owner (a MailServer or a
// MailDomain): it belongs to someone else (a tenant's own Service of the
// same name, an object of another server) and must not be overwritten.
type ownershipConflictError struct {
	kind, name, owner string
}

func (e *ownershipConflictError) Error() string {
	return fmt.Sprintf("%s %q already exists and is not controlled by this %s; rename or delete it", e.kind, e.name, e.owner)
}

// applyFailureReason is the condition reason for a failed apply.
func applyFailureReason(err error) string {
	if _, ok := stderrors.AsType[*ownershipConflictError](err); ok {
		return ReasonOwnershipConflict
	}
	return ReasonApplyFailed
}

// kindName returns a readable kind for error messages.
func kindName(obj client.Object) string {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u.GetKind()
	}
	return reflect.TypeOf(obj).Elem().Name()
}

// applyObject creates obj if it does not exist and updates the
// controller-owned fields if it does. Every object of the data plane
// set is fully owned by the MailServer (controller owner reference),
// so replacing the spec is safe. An existing object that is not
// controlled by the MailServer is never overwritten (an
// ownershipConflictError is returned); the controller reference is
// set on create only. The cache only holds the operator's own
// (labelled) objects, so a NotFound from it is confirmed with reader
// (not the cache): an object of someone else with the name, or one of
// ours that lost its label, is found there.
func applyObject(ctx context.Context, c client.Client, reader client.Reader, scheme *runtime.Scheme, owner, desired client.Object) error {
	existing := desired.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		existing = emptyLike(desired)
		err = reader.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	}
	if errors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
			return err
		}
		return c.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(existing, owner) {
		return &ownershipConflictError{kind: kindName(desired), name: desired.GetName(), owner: kindName(owner)}
	}
	// spec.volumeClaimTemplates is immutable on update (k8s 1.37).
	// The API makes the storage sizes immutable (ADR 0010), so the
	// template set only changes by name: the Dovecot mail volume
	// appears or disappears when mailboxes are toggled. That needs a
	// recreate; the Postfix template never changes its name, so its
	// StatefulSet is never deleted here. The per-replica PVCs are not
	// deleted with the StatefulSet (default deletePolicy Keep) and
	// the new StatefulSet reuses them, so mailbox data survives the
	// toggle. Dovecot has a single replica, so a background delete
	// (which also removes the pod) bypasses no disruption budget.
	if d, ok := desired.(*appsv1.StatefulSet); ok {
		if e, ok := existing.(*appsv1.StatefulSet); ok {
			if !volumeClaimTemplateNamesEqual(e.Spec.VolumeClaimTemplates, d.Spec.VolumeClaimTemplates) {
				return recreateStatefulSet(ctx, c, scheme, owner, e, d)
			}
			// Keep the existing templates so a size difference
			// (a server created before the sizes became immutable,
			// or an absent block set later) does not fail the update.
			d.Spec.VolumeClaimTemplates = e.Spec.VolumeClaimTemplates
		}
	}
	// Skip the write when the owned fields already match, so a
	// reconcile does not re-trigger itself through a watch event.
	if !mergeOwnedFields(existing, desired) {
		return nil
	}
	return c.Update(ctx, existing)
}

// emptyLike returns an empty object of the type (and kind, for
// unstructured) of obj, so a read does not merge into its content.
func emptyLike(obj client.Object) client.Object {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		e := &unstructured.Unstructured{}
		e.SetGroupVersionKind(u.GroupVersionKind())
		return e
	}
	return reflect.New(reflect.TypeOf(obj).Elem()).Interface().(client.Object)
}

// recreateStatefulSet replaces a StatefulSet whose volume claim
// template names changed. The delete uses background propagation and
// leaves the PVCs in place. A Create that meets the object still
// being deleted (AlreadyExists) is not an error: the StatefulSet
// watch triggers the next reconcile, which creates it.
func recreateStatefulSet(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, existing, desired *appsv1.StatefulSet) error {
	if err := c.Delete(ctx, existing, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !errors.IsNotFound(err) {
		return err
	}
	if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
		return err
	}
	if err := c.Create(ctx, desired); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// volumeClaimTemplateNamesEqual reports whether two template lists
// carry the same claim names in order. Sizes are not compared: they
// are immutable in the API and an existing StatefulSet keeps its own.
func volumeClaimTemplateNamesEqual(a, b []corev1.PersistentVolumeClaim) bool {
	return slices.EqualFunc(a, b, func(x, y corev1.PersistentVolumeClaim) bool {
		return x.Name == y.Name
	})
}

// mergeOwnedFields copies the controller-owned fields (labels,
// annotations, spec) from desired into the fetched existing object,
// preserving the server-managed status and metadata owned by other
// actors (for example deployment.kubernetes.io/revision, maintained
// by the kube deployment controller). Dropping foreign annotations
// would change the object on every write and, for Deployments, bump
// the generation each time. It reports whether anything changed.
//
// shortcut: whole specs are compared, and the fetched spec carries
// server defaults the rendered one lacks, so every reconcile rewrites
// every object (about a dozen PUTs); compare the config hash annotation
// and the owned fields instead when the API load matters.
func mergeOwnedFields(existing, desired client.Object) bool {
	var changed bool
	// mergeStrings overlays the owned keys onto the existing map,
	// keeping keys that only the existing object has.
	mergeStrings := func(e *map[string]string, owned map[string]string) {
		if len(owned) == 0 {
			return
		}
		if *e == nil {
			*e = make(map[string]string, len(owned))
		}
		for k, v := range owned {
			if (*e)[k] != v {
				(*e)[k] = v
				changed = true
			}
		}
	}
	// sameValue reports whether an owned value field already matches.
	sameValue := func(current, owned any) bool {
		return equality.Semantic.DeepEqual(current, owned)
	}
	switch d := desired.(type) {
	case *corev1.ConfigMap:
		e := existing.(*corev1.ConfigMap)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Data, d.Data) {
			e.Data = d.Data
			changed = true
		}
	case *corev1.Service:
		e := existing.(*corev1.Service)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Spec, d.Spec) {
			e.Spec = d.Spec
			changed = true
		}
	case *corev1.PersistentVolumeClaim:
		e := existing.(*corev1.PersistentVolumeClaim)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Spec.Resources, d.Spec.Resources) { // the only resizable field
			e.Spec.Resources = d.Spec.Resources
			changed = true
		}
	case *appsv1.StatefulSet:
		e := existing.(*appsv1.StatefulSet)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Spec, d.Spec) {
			e.Spec = d.Spec
			changed = true
		}
	case *appsv1.Deployment:
		e := existing.(*appsv1.Deployment)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Spec, d.Spec) {
			e.Spec = d.Spec
			changed = true
		}
	case *policyv1.PodDisruptionBudget:
		e := existing.(*policyv1.PodDisruptionBudget)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Spec, d.Spec) {
			e.Spec = d.Spec
			changed = true
		}
	case *networkingv1.NetworkPolicy:
		e := existing.(*networkingv1.NetworkPolicy)
		mergeStrings(&e.Labels, d.Labels)
		mergeStrings(&e.Annotations, d.Annotations)
		if !sameValue(e.Spec, d.Spec) {
			e.Spec = d.Spec
			changed = true
		}
	default:
		// Unstructured objects (Certificate, ServiceMonitor).
		u := desired.(*unstructured.Unstructured)
		e := existing.(*unstructured.Unstructured)
		// GetLabels returns a copy, so merge explicitly and re-set.
		if owned := u.GetLabels(); len(owned) > 0 {
			cur := e.GetLabels()
			if len(cur) == 0 {
				e.SetLabels(owned)
				changed = true
			} else {
				merged := make(map[string]string, len(cur)+len(owned))
				maps.Copy(merged, cur)
				labelsChanged := false
				for k, v := range owned {
					if merged[k] != v {
						merged[k] = v
						labelsChanged = true
					}
				}
				if labelsChanged {
					e.SetLabels(merged)
					changed = true
				}
			}
		}
		spec, found, _ := unstructured.NestedMap(u.Object, "spec")
		if found && !sameValue(e.Object["spec"], spec) {
			_ = unstructured.SetNestedMap(e.Object, spec, "spec")
			changed = true
		}
	}
	return changed
}

// isUnstructuredKind reports whether obj is an unstructured object of
// the given kind.
func isUnstructuredKind(obj client.Object, kind string) bool {
	u, ok := obj.(*unstructured.Unstructured)
	return ok && u.GetKind() == kind
}

// setCondition stamps a condition on the current generation and time;
// meta.SetStatusCondition keeps LastTransitionTime stable while the
// status is unchanged.
func setCondition(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// conditionStatus maps a bool to a condition status.
func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// readyReason reports the reason and message of the first condition
// (in the given order) that is not True; all True yields "Ready".
func readyReason(conds []metav1.Condition, order []string) (string, string) {
	for _, t := range order {
		c := meta.FindStatusCondition(conds, t)
		if c != nil && c.Status != metav1.ConditionTrue {
			return c.Reason, c.Message
		}
	}
	return ReasonReady, "all managed workloads are running with validated configuration"
}
