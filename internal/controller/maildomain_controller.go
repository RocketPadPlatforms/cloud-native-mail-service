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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
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

	// Recorder emits Kubernetes Events on important state changes
	// (nil in tests).
	Recorder recorder.EventRecorder

	// Now is the clock; nil means time.Now. Tests advance a fake one.
	Now func() time.Time
}

// The controller never writes the spec of a MailDomain and uses no finalizer. The
// finalizers permission is for the controller references it sets (blockOwnerDeletion)
// where the OwnerReferencesPermissionEnforcement plugin is on.
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains/status,verbs=patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains/finalizers,verbs=update
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants,verbs=get;list;watch
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
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !md.DeletionTimestamp.IsZero() {
		// No finalizer: the DNSEndpoint is garbage collected through its
		// owner reference, the key Secrets are kept (ADR 0011).
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
		requeue = readyRequeueInterval
	} else {
		requeue = r.reconcileUnauthorized(md)
	}
	r.setReady(md)
	return r.finish(ctx, md, before, requeue)
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
func (r *MailDomainReconciler) reconcileUnauthorized(md *cnmsv1alpha1.MailDomain) time.Duration {
	md.Status.Records, md.Status.DKIM, md.Status.MTAStS = nil, nil, nil
	for _, t := range []string{
		ConditionDKIMKeysValid, ConditionDKIMRotation, ConditionRecordsVerified,
		ConditionDNSPublished, ConditionSPFReady, ConditionMTASTSReady,
	} {
		meta.RemoveStatusCondition(&md.Status.Conditions, t)
	}
	return readyRequeueInterval
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
func (r *MailDomainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailDomain{}).
		Named("maildomain").
		Complete(r)
}
