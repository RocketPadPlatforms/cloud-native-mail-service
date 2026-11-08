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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// MailServiceAccountReconciler reconciles a MailServiceAccount object.
type MailServiceAccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailserviceaccounts,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailserviceaccounts/status,verbs=patch

// Reconcile does nothing yet: the credential, the sender checks and the
// status of a MailServiceAccount arrive with its controller (ADR 0015,
// section 4.9).
func (r *MailServiceAccountReconciler) Reconcile(_ context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MailServiceAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailServiceAccount{}).
		Named("mailserviceaccount").
		Complete(r)
}
