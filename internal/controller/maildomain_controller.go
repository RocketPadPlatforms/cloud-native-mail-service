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
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

// MailDomainReconciler reconciles a MailDomain object: it authorizes
// the domain (ADR 0003), keeps its DKIM keys (ADR 0011), and reports
// and publishes its DNS records (ADR 0012).
type MailDomainReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// DomainAuthorization is the validated --domain-authorization
	// flag (allowlist or unrestricted, ADR 0003).
	DomainAuthorization domainAuthorizationMode

	// APIReader reads without the cache (mgr.GetAPIReader()): the
	// Secrets of DKIM keys, which are never cached.
	APIReader client.Reader

	// Recorder emits Kubernetes Events on important state changes
	// (nil in tests).
	Recorder recorder.EventRecorder

	// Resolver looks the records up (--dns-resolver). Without one no
	// record is verified.
	Resolver dnsrecords.Resolver

	// MTASTSHosts returns the mx hosts of the MTA-STS policy of the
	// domain: the host names of the MailServers that serve it
	// (ADR 0012). Nil means none, so there is no policy.
	MTASTSHosts func(ctx context.Context, md *cnmsv1alpha1.MailDomain) ([]string, error)

	// Now is the clock; nil means time.Now. Tests advance a fake one.
	Now func() time.Time

	dns  dnsCache
	crds crdCache
}

// The controller never writes the spec of a MailDomain and uses no finalizer. It patches
// one annotation, the rotation state (ADR 0011). The finalizers permission is for the
// controller references it sets (blockOwnerDeletion) where the
// OwnerReferencesPermissionEnforcement plugin is on.
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains/status,verbs=patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains/finalizers,verbs=update
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants,verbs=get;list;watch
// Key Secrets are immutable: created, deleted when a retired key leaves DNS, never updated,
// and kept when the MailDomain goes away. They are never cached or watched.
// The DNSEndpoint is an optional integration (principle 10): the CRD is looked up by name.
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;create;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// now returns the current time in UTC, truncated to the second: the
// API serializes times with second precision, so a finer value would
// never compare equal to what is read back and every reconcile would
// write the status.
func (r *MailDomainReconciler) now() time.Time {
	t := time.Now()
	if r.Now != nil {
		t = r.Now()
	}
	return t.UTC().Truncate(time.Second)
}

// Reconcile moves one MailDomain toward its desired state. It is
// idempotent and fail closed: an unauthorized domain gets no keys, no
// records and no signing. Everything it persists is re-derivable
// except the key Secrets and the rotation annotation (ADR 0011).
func (r *MailDomainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	md := &cnmsv1alpha1.MailDomain{}
	if err := r.Get(ctx, req.NamespacedName, md); err != nil {
		if errors.IsNotFound(err) {
			r.dns.forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !md.DeletionTimestamp.IsZero() {
		// No finalizer: the DNSEndpoint is garbage collected through its
		// owner reference, the key Secrets are kept (ADR 0011).
		r.dns.forget(req.NamespacedName)
		return ctrl.Result{}, nil
	}
	before := md.DeepCopy()

	grants, claims, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return ctrl.Result{}, err
	}
	reason, message, authorized := r.authorize(md, grants, claims)
	prevAuth := meta.FindStatusCondition(md.Status.Conditions, ConditionAuthorized).DeepCopy()
	r.setCondition(md, ConditionAuthorized, conditionStatus(authorized), reason, message)
	r.emitAuthorizationEvent(md, prevAuth, authorized, reason, message)

	var requeue time.Duration
	if authorized {
		var err error
		if requeue, err = r.reconcileAuthorized(ctx, md); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		requeue = r.reconcileUnauthorized(ctx, md)
	}
	r.setReady(md)
	return r.finish(ctx, md, before, requeue)
}

// reconcileAuthorized handles an authorized domain and returns the
// delay of the next reconcile.
func (r *MailDomainReconciler) reconcileAuthorized(ctx context.Context, md *cnmsv1alpha1.MailDomain) (time.Duration, error) {
	now := r.now()
	st := r.dns.get(client.ObjectKeyFromObject(md))
	var set *keySet
	var existing []generatedKey
	ownKeys := md.Spec.DKIM != nil && len(md.Spec.DKIM.Keys) > 0
	if ownKeys {
		var err error
		if set, err = r.reconcileOwnKeys(ctx, md); err != nil {
			return 0, err
		}
	} else {
		var err error
		if existing, err = r.loadGeneratedKeys(ctx, md); err != nil {
			return 0, err
		}
	}

	// Verify the records of the keys that exist before the rotation
	// decides on them: a pending key only becomes active once its
	// record has been seen (ADR 0011).
	var current []keyEntry
	if ownKeys {
		current = set.all()
	} else {
		for _, g := range existing {
			if g.key != nil {
				current = append(current, keyEntry{selector: g.info.Selector, key: g.key})
			}
		}
	}
	in, err := r.composeInput(ctx, md, current)
	if err != nil {
		return 0, err
	}
	known, err := dnsrecords.Compose(in)
	if err != nil {
		return 0, err
	}
	ran := st.due(now, known.Records)
	if ran {
		st.lookup(ctx, r.Resolver, known.Records, md.Status.Records, now)
	}

	if !ownKeys {
		if set, err = r.reconcileGeneratedKeys(ctx, md, existing, st.dkimStates(md.Spec.Domain, existing)); err != nil {
			return 0, err
		}
	}
	r.reportKeys(md, set)

	if in, err = r.composeInput(ctx, md, set.all()); err != nil {
		return 0, err
	}
	composed, err := dnsrecords.Compose(in)
	if err != nil {
		return 0, err
	}
	r.reportRecords(md, composed, st)
	endpointRetry := r.reconcileDNSEndpoint(ctx, md, composed.Records)

	// Schedule the next lookup round. A key that was just created has
	// no result yet and is looked up at once.
	allVerified := len(set.pending) == 0 || set.ownKeys
	for _, rec := range composed.Records {
		if res, ok := st.results[recordKey(rec)]; !ok || res.unknown || res.verified == nil || !*res.verified {
			allVerified = false
		}
	}
	switch {
	case st.missing(composed.Records):
		st.nextDue = now
	case ran:
		st.schedule(now, allVerified)
	}
	return soonest(readyRequeueInterval, set.requeue, endpointRetry, max(st.nextDue.Sub(now), time.Second)), nil
}

// soonest returns the smallest positive delay.
func soonest(delays ...time.Duration) time.Duration {
	var best time.Duration
	for _, d := range delays {
		if d > 0 && (best == 0 || d < best) {
			best = d
		}
	}
	return best
}

// reportKeys writes the key state to the status and sets DKIMKeysValid
// and DKIMRotation.
func (r *MailDomainReconciler) reportKeys(md *cnmsv1alpha1.MailDomain, set *keySet) {
	status := &cnmsv1alpha1.DKIMStatus{
		Active:   keyStatuses(set.active),
		Pending:  keyStatuses(set.pending),
		Retiring: keyStatuses(set.retiring),
	}
	if !set.nextRotation.IsZero() {
		status.NextRotation = new(metav1.NewTime(set.nextRotation.UTC().Truncate(time.Second)))
	}
	md.Status.DKIM = status

	if len(set.problems) == 0 {
		r.setCondition(md, ConditionDKIMKeysValid, metav1.ConditionTrue, ReasonKeysValid, "every DKIM key is present and usable")
	} else {
		r.setCondition(md, ConditionDKIMKeysValid, metav1.ConditionFalse, set.problemReason, strings.Join(set.problems, "; "))
	}
	switch {
	case set.ownKeys:
		r.setCondition(md, ConditionDKIMRotation, metav1.ConditionTrue, ReasonUserManaged,
			"the keys and their signing flags are set in spec.dkim.keys")
	case len(set.pending) > 0:
		sels := make([]string, len(set.pending))
		for i, e := range set.pending {
			sels[i] = e.selector
		}
		r.setCondition(md, ConditionDKIMRotation, metav1.ConditionFalse, ReasonAwaitingDNS,
			fmt.Sprintf("the record of %s is not verified yet; the current key keeps signing", strings.Join(sels, ", ")))
	default:
		r.setCondition(md, ConditionDKIMRotation, metav1.ConditionTrue, ReasonNoPendingKey, "no key waits for its DNS record")
	}
}

// keyStatuses converts keys to their status form, sorted by selector.
func keyStatuses(entries []keyEntry) []cnmsv1alpha1.DKIMKeyStatus {
	out := make([]cnmsv1alpha1.DKIMKeyStatus, 0, len(entries))
	for _, e := range entries {
		ks := cnmsv1alpha1.DKIMKeyStatus{Selector: e.selector, Algorithm: string(e.key.Algorithm)}
		if !e.created.IsZero() {
			ks.KeyCreated = new(metav1.NewTime(e.created.UTC().Truncate(time.Second)))
		}
		if !e.until.IsZero() {
			ks.Until = new(metav1.NewTime(e.until.UTC().Truncate(time.Second)))
		}
		out = append(out, ks)
	}
	slices.SortFunc(out, func(a, b cnmsv1alpha1.DKIMKeyStatus) int { return strings.Compare(a.Selector, b.Selector) })
	return out
}

// authorize decides whether the MailDomain may use its domain: the
// domain must not be a public suffix (ADR 0012) and passes the same
// grant and uniqueness check as in the MailServer controller (ADR
// 0003).
func (r *MailDomainReconciler) authorize(md *cnmsv1alpha1.MailDomain, grants *cnmsv1alpha1.MailDomainGrantList, claims *cnmsv1alpha1.MailDomainList) (reason, message string, ok bool) {
	domain := md.Spec.Domain
	ev := &authorizationEvaluator{grants: grantRefs(grants), claims: domainClaims(claims), mode: r.DomainAuthorization}
	if f := ev.authorize(md.Namespace, domain); f != nil {
		return f.reason, f.message, false
	}
	if r.DomainAuthorization == AuthorizationUnrestricted {
		return ReasonAuthorizationDisabled, "domain authorization is disabled (--domain-authorization=unrestricted)", true
	}
	return ReasonAuthorized, "the domain is granted for this namespace", true
}

// reconcileUnauthorized fails the domain closed: no records are
// required, so the status lists none, and the conditions that describe
// the keys and records are dropped. It returns the delay of the next
// check.
func (r *MailDomainReconciler) reconcileUnauthorized(ctx context.Context, md *cnmsv1alpha1.MailDomain) time.Duration {
	r.dns.forget(client.ObjectKeyFromObject(md))
	md.Status.Records, md.Status.DKIM, md.Status.MTAStS = nil, nil, nil
	for _, t := range []string{
		ConditionDKIMKeysValid, ConditionDKIMRotation, ConditionRecordsVerified,
		ConditionDNSPublished, ConditionSPFReady, ConditionMTASTSReady, ConditionReportAuthorization,
	} {
		meta.RemoveStatusCondition(&md.Status.Conditions, t)
	}
	return soonest(readyRequeueInterval, r.revokeDNSEndpoint(ctx, md))
}

// emitAuthorizationEvent records an Event when the authorization
// verdict changes.
func (r *MailDomainReconciler) emitAuthorizationEvent(md *cnmsv1alpha1.MailDomain, prev *metav1.Condition, authorized bool, reason, message string) {
	if r.Recorder == nil || authorized || (prev != nil && prev.Status == metav1.ConditionFalse && prev.Reason == reason) {
		return
	}
	r.Recorder.Eventf(md, nil, corev1.EventTypeWarning, reason, "Authorize", "%s", eventMessage(message))
}

// setCondition stamps a condition on the current generation. The
// transition time comes from the reconciler's clock; meta keeps the
// old one while the status is unchanged.
func (r *MailDomainReconciler) setCondition(md *cnmsv1alpha1.MailDomain, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&md.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: md.Generation,
		LastTransitionTime: metav1.NewTime(r.now()),
	})
}

// setReady rolls the conditions up (ADR 0012): the first condition, in
// the order below, that is not True gives the reason of Ready.
func (r *MailDomainReconciler) setReady(md *cnmsv1alpha1.MailDomain) {
	reason, message := readyReason(md.Status.Conditions, []string{
		ConditionAuthorized, ConditionDKIMKeysValid, ConditionDNSPublished, ConditionRecordsVerified,
	})
	status := metav1.ConditionFalse
	if reason == ReasonReady {
		status = metav1.ConditionTrue
		message = "the domain is authorized, its DKIM keys are valid and every required record is verified"
	}
	r.setCondition(md, conditionReady, status, reason, message)
}

// finish persists the status as a patch against before (the object as
// read) unless nothing changed, and returns the delay of the next
// reconcile.
func (r *MailDomainReconciler) finish(ctx context.Context, md, before *cnmsv1alpha1.MailDomain, requeue time.Duration) (ctrl.Result, error) {
	md.Status.ObservedGeneration = md.Generation
	if !equality.Semantic.DeepEqual(before.Status, md.Status) {
		if err := r.Status().Patch(ctx, md, client.MergeFrom(before)); err != nil {
			if errors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// The DNSEndpoint is an optional integration (principle 10): it is
// watched only when its CRD is installed at startup, because a watch on
// a kind whose CRD is missing fails the cache sync and the manager
// exits. A CRD installed later is picked up by the periodic look in
// reconcileDNSEndpoint. The key Secrets of your own keys are not
// watched (Secrets are never cached): they are read again on a timer.
func (r *MailDomainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailDomain{}).
		Named("maildomain")
	if crdInstalled(mgr, crdDNSEndpoint) {
		endpoint := &unstructured.Unstructured{}
		endpoint.SetGroupVersionKind(dnsEndpointGVK)
		b = b.Owns(endpoint)
	}
	return b.
		Watches(&cnmsv1alpha1.MailDomain{}, handler.EnqueueRequestsFromMapFunc(r.mapPeerDomains)).
		Watches(&cnmsv1alpha1.MailDomainGrant{}, handler.EnqueueRequestsFromMapFunc(r.mapGrantDomains)).
		Complete(r)
}

// mapPeerDomains enqueues the MailDomains that claim the same domain as
// obj: the uniqueness check is cluster wide, so a new, changed or
// deleted claim blocks or frees the other claimants. It lists every
// MailDomain instead of using an index, which the MailServer controller
// owns.
func (r *MailDomainReconciler) mapPeerDomains(ctx context.Context, obj client.Object) []reconcile.Request {
	d, ok := obj.(*cnmsv1alpha1.MailDomain)
	if !ok {
		return nil
	}
	return r.matchingDomains(ctx, func(o *cnmsv1alpha1.MailDomain) bool {
		return o.Spec.Domain == d.Spec.Domain
	})
}

// mapGrantDomains enqueues the MailDomains a grant can affect: those
// of the granted namespace (before and after a change, the handler also
// maps the old object) and those whose domain is the granted one or,
// for includeSubdomains, below it, in any namespace: the grant decides
// who owns the domain for everyone.
func (r *MailDomainReconciler) mapGrantDomains(ctx context.Context, obj client.Object) []reconcile.Request {
	g, ok := obj.(*cnmsv1alpha1.MailDomainGrant)
	if !ok {
		return nil
	}
	return r.matchingDomains(ctx, func(o *cnmsv1alpha1.MailDomain) bool {
		return o.Namespace == g.Spec.Namespace || o.Spec.Domain == g.Name || strings.HasSuffix(o.Spec.Domain, "."+g.Name)
	})
}

func (r *MailDomainReconciler) matchingDomains(ctx context.Context, match func(*cnmsv1alpha1.MailDomain) bool) []reconcile.Request {
	list := &cnmsv1alpha1.MailDomainList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailDomains")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if match(&list.Items[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}
