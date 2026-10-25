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
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants/status,verbs=get;update;patch
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
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: grant.Spec.Namespace}, ns)
	status, reason, message := metav1.ConditionTrue, "NamespaceExists",
		"the granted namespace exists"
	if errors.IsNotFound(err) {
		status, reason, message = metav1.ConditionFalse, "NamespaceNotFound",
			fmt.Sprintf("namespace %q does not exist", grant.Spec.Namespace)
		if r.Recorder != nil {
			r.Recorder.Eventf(grant, nil, corev1.EventTypeWarning, reason,
				"NamespaceNotFound", message)
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}
	setCondition(&grant.Status.Conditions, grant.Generation,
		ConditionNamespaceExists, status, reason, message)
	grant.Status.ObservedGeneration = grant.Generation
	if err := r.Status().Update(ctx, grant); err != nil {
		return ctrl.Result{}, err
	}
	log.V(1).Info("Reconciled MailDomainGrant",
		"namespace", grant.Spec.Namespace, "exists", status == metav1.ConditionTrue)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MailDomainGrantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailDomainGrant{}).
		Named("maildomaingrant").
		Complete(r)
}
