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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// MailDomainGrant condition types (ADR 0003).
const (
	// ConditionNamespaceExists is true when the namespace a grant
	// points at exists. The condition is informational only and never
	// feeds back into authorization.
	ConditionNamespaceExists = "NamespaceExists"
)

// MailDomainGrantReconciler reconciles a MailDomainGrant object.
type MailDomainGrantReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder emits a Warning Event when the grant points at a
	// missing namespace (nil in tests).
	Recorder recorder.EventRecorder
}

// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants/status,verbs=patch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile warns when a grant points at a namespace that does not
// exist (ADR 0003); authorization itself is evaluated by the
// MailServer controller from the current grants.
func (r *MailDomainGrantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	grant := &cnmsv1alpha1.MailDomainGrant{}
	if err := r.Get(ctx, req.NamespacedName, grant); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	orig := grant.DeepCopy()
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: grant.Spec.Namespace}, ns)
	status, reason, message := metav1.ConditionTrue, "NamespaceExists",
		"the granted namespace exists"
	if errors.IsNotFound(err) {
		status, reason, message = metav1.ConditionFalse, "NamespaceNotFound",
			fmt.Sprintf("namespace %q does not exist", grant.Spec.Namespace)
		if r.Recorder != nil {
			r.Recorder.Eventf(grant, nil, corev1.EventTypeWarning, reason,
				"NamespaceNotFound", "%s", eventMessage(message))
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}
	setCondition(&grant.Status.Conditions, grant.Generation,
		ConditionNamespaceExists, status, reason, message)
	grant.Status.ObservedGeneration = grant.Generation
	if err := r.Status().Patch(ctx, grant, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}
	log.V(1).Info("Reconciled MailDomainGrant",
		"namespace", grant.Spec.Namespace, "exists", status == metav1.ConditionTrue)
	return ctrl.Result{}, nil
}

// grantNamespaceIndex is the field index of MailDomainGrants by
// spec.namespace, so a Namespace event finds the grants pointing at it.
const grantNamespaceIndex = "spec.namespace"

// indexGrantByNamespace is the extractor of grantNamespaceIndex.
func indexGrantByNamespace(obj client.Object) []string {
	g, ok := obj.(*cnmsv1alpha1.MailDomainGrant)
	if !ok || g.Spec.Namespace == "" {
		return nil
	}
	return []string{g.Spec.Namespace}
}

// mapNamespaceGrants enqueues the grants that point at the namespace.
func (r *MailDomainGrantReconciler) mapNamespaceGrants(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &cnmsv1alpha1.MailDomainGrantList{}
	if err := r.List(ctx, list, client.MatchingFields{grantNamespaceIndex: obj.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list MailDomainGrants", "namespace", obj.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager. A
// Namespace appearing or disappearing re-evaluates the grants that
// point at it; label and annotation updates are ignored.
func (r *MailDomainGrantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(),
		&cnmsv1alpha1.MailDomainGrant{}, grantNamespaceIndex, indexGrantByNamespace); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailDomainGrant{}).
		Named("maildomaingrant").
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceGrants),
			builder.WithPredicates(predicate.Funcs{
				UpdateFunc: func(event.UpdateEvent) bool { return false },
			})).
		Complete(r)
}
