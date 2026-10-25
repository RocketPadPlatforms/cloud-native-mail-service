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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/dovecot"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/postfix"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/rspamd"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/valkey"
)

// MailServer status conditions (see the Phase 2 plan and ADR 0005).
const (
	// ConditionDomainsAuthorized is true when every domain in
	// spec.domains passes the domain authorization check (ADR 0003).
	ConditionDomainsAuthorized = "DomainsAuthorized"
	// ConditionCertificateReady is true when the TLS certificate
	// Secret carries tls.crt and tls.key.
	ConditionCertificateReady = "CertificateReady"
	// ConditionWorkloadsReady is true when every managed workload has
	// all its replicas ready.
	ConditionWorkloadsReady = "WorkloadsReady"
	// ConditionObservability reports the Prometheus Operator
	// integration state (ADR 0005).
	ConditionObservability = "Observability"
	// conditionReady rolls up all other conditions.
	conditionReady = "Ready"
)

// Ready condition reasons set before the data plane renders.
const (
	// ReasonDirectoryNotFound is set when the referenced MailDirectory
	// does not exist.
	ReasonDirectoryNotFound = "DirectoryNotFound"
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
)

// requeueInterval is how often a not-ready MailServer is re-checked
// (certificates are issued, workloads come up, CRDs are installed).
const requeueInterval = 30 * time.Second

// crdCacheTTL is how long CRD-availability results are cached, so
// integrations installed later are picked up without a restart
// (principle 10).
const crdCacheTTL = 5 * time.Minute

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

	// Recorder emits Kubernetes Events on Ready transitions
	// (nil in tests).
	Recorder recorder.EventRecorder

	crds crdCache
}

// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomains,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildirectories,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildomaingrants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile moves the cluster state of one MailServer toward the
// desired state (Phase 2): authorization, rendering, apply and
// readiness reporting. It is idempotent and fail closed: a bad input
// never replaces a working data plane.
func (r *MailServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	server := &cnmsv1alpha1.MailServer{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	prevReady := meta.FindStatusCondition(server.Status.Conditions, conditionReady)
	conds := &server.Status.Conditions
	generation := server.Generation
	// block records a fail-closed Ready condition and finishes.
	block := func(reason, message string) (ctrl.Result, error) {
		setCondition(conds, generation, conditionReady, metav1.ConditionFalse, reason, message)
		return r.finish(ctx, server, prevReady)
	}

	// Directory: fail closed when the referenced MailDirectory is
	// missing (recipient validation and auth are backed by it).
	directory := &cnmsv1alpha1.MailDirectory{}
	err := r.Get(ctx, types.NamespacedName{
		Name: server.Spec.DirectoryRef.Name, Namespace: server.Namespace,
	}, directory)
	if errors.IsNotFound(err) {
		return block(ReasonDirectoryNotFound, fmt.Sprintf(
			"MailDirectory %q not found in namespace %q",
			server.Spec.DirectoryRef.Name, server.Namespace))
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// Outbound modes not implemented yet (fail closed).
	switch {
	case server.Spec.Outbound.Relay != nil:
		return block(ReasonRelayNotAvailable, "relay outbound arrives in Phase 6")
	case server.Spec.Outbound.Direct != nil && server.Spec.Outbound.Direct.IpPoolClaimRef != nil:
		return block(ReasonIpPoolClaimNotAvailable, "ipPoolClaimRef arrives in Phase 7")
	}

	// Domain authorization (ADR 0003), re-evaluated on every reconcile.
	grants, domains, err := r.listAuthorizationInput(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	authorized, authReason, authMessage := (&authorizationEvaluator{
		server: server,
		grants: grantRefs(grants),
		claims: domainClaims(domains),
		mode:   r.DomainAuthorization,
	}).Evaluate()
	allAuthorized := authReason == ReasonAuthorized || authReason == ReasonAuthorizationDisabled
	setCondition(conds, generation, ConditionDomainsAuthorized,
		conditionStatus(allAuthorized), authReason, authMessage)
	if !allAuthorized {
		return block(authReason, authMessage)
	}

	// Static test users (fail closed: invalid users must not render).
	users, userReason, userMessage, usersOK := r.staticUsers(ctx, server)
	if !usersOK {
		return block(userReason, userMessage)
	}

	// Render and apply the data plane.
	certCRDs, err := r.crdsAvailable(ctx, crdCertificate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyDataPlane(ctx, server, authorized, users, certCRDs); err != nil {
		log.FromContext(ctx).Error(err, "Could not apply data plane")
		return block(ReasonApplyFailed, err.Error())
	}

	// Readiness of the rendered objects.
	certStatus, certReason, certMessage := r.certificateReady(ctx, server)
	setCondition(conds, generation, ConditionCertificateReady, certStatus, certReason, certMessage)
	workloadsOK, workloadsMessage := r.workloadsReady(ctx, server)
	setCondition(conds, generation, ConditionWorkloadsReady,
		conditionStatus(workloadsOK),
		conditionReason(workloadsOK, "AllWorkloadsReady", "WorkloadsNotReady"), workloadsMessage)
	obsStatus, obsReason, obsMessage := r.reconcileObservability(ctx, server)
	setCondition(conds, generation, ConditionObservability, obsStatus, obsReason, obsMessage)

	reason, message := readyReason(*conds, []string{
		ConditionDomainsAuthorized, ConditionCertificateReady,
		ConditionWorkloadsReady, ConditionObservability,
	})
	setCondition(conds, generation, conditionReady,
		conditionStatus(reason == "Ready"), reason, message)
	return r.finish(ctx, server, prevReady)
}

// finish persists the status, emits a Ready transition event and
// schedules the re-check of a not-ready server.
func (r *MailServerReconciler) finish(ctx context.Context, server *cnmsv1alpha1.MailServer, prevReady *metav1.Condition) (ctrl.Result, error) {
	server.Status.ObservedGeneration = server.Generation
	if err := r.Status().Update(ctx, server); err != nil {
		return ctrl.Result{}, err
	}
	r.emitReadyEvent(server, prevReady)
	ready := meta.FindStatusCondition(server.Status.Conditions, conditionReady)
	if ready.Status == metav1.ConditionFalse {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	return ctrl.Result{}, nil
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
			ready.Reason, "BecameReady", ready.Message)
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeWarning,
		ready.Reason, "BecameNotReady", ready.Message)
}

// listAuthorizationInput lists the grants and claims the evaluator
// needs (ADR 0003: a pure function of the current objects).
func (r *MailServerReconciler) listAuthorizationInput(ctx context.Context) (*cnmsv1alpha1.MailDomainGrantList, *cnmsv1alpha1.MailDomainList, error) {
	grants := &cnmsv1alpha1.MailDomainGrantList{}
	if err := r.List(ctx, grants); err != nil {
		return nil, nil, err
	}
	domains := &cnmsv1alpha1.MailDomainList{}
	if err := r.List(ctx, domains); err != nil {
		return nil, nil, err
	}
	return grants, domains, nil
}

// staticUsers loads and validates the Phase 2 test users from
// spec.submission.insecureStaticUsers. It returns ok=false with a
// Ready-condition reason when the gate must block rendering.
func (r *MailServerReconciler) staticUsers(ctx context.Context, server *cnmsv1alpha1.MailServer) ([]render.StaticUser, string, string, bool) {
	ref := server.Spec.Submission
	if ref == nil || ref.InsecureStaticUsers == nil {
		return nil, "", "", true
	}
	if !r.EnableInsecureTestFeatures {
		return nil, ReasonInsecureStaticUsersDisabled,
			"spec.submission.insecureStaticUsers is set but the operator was started without --enable-insecure-test-features", false
	}
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
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

// applyDataPlane renders every component and creates or updates the
// data plane objects of the MailServer.
func (r *MailServerReconciler) applyDataPlane(ctx context.Context, server *cnmsv1alpha1.MailServer, authorized []string, users []render.StaticUser, certCRDs bool) error {
	req := &render.RenderRequest{
		Server:            server,
		AuthorizedDomains: authorized,
		StaticUsers:       users,
	}
	renders := make(map[render.Component]*render.RenderResult, len(render.AllComponents()))
	for _, renderer := range []render.EngineRenderer{
		postfix.New(), rspamd.New(), dovecot.New(), valkey.New(),
	} {
		result, err := renderer.Render(req)
		if err != nil {
			return fmt.Errorf("render %s: %w", renderer.Component(), err)
		}
		renders[renderer.Component()] = result
	}
	objs, err := manifests.Build(req, renders)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		// Without the cert-manager CRDs the Certificate is not
		// created; a pre-created Secret still satisfies TLS.
		if !certCRDs && isUnstructuredKind(obj, certificateGVK.Kind) {
			continue
		}
		if err := applyObject(ctx, r.Client, r.Scheme, server, obj); err != nil {
			return fmt.Errorf("apply %T %s: %w", obj, obj.GetName(), err)
		}
	}
	return nil
}

// certificateReady reports whether the TLS certificate Secret carries
// tls.crt and tls.key (the Secret is the source of truth for the
// in-place-reloaded TLS mounts).
func (r *MailServerReconciler) certificateReady(ctx context.Context, server *cnmsv1alpha1.MailServer) (metav1.ConditionStatus, string, string) {
	name := manifests.TLSSecretName(server)
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, secret)
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

// workloadsReady reports whether all four data plane workloads have
// all their replicas ready.
func (r *MailServerReconciler) workloadsReady(ctx context.Context, server *cnmsv1alpha1.MailServer) (bool, string) {
	var notReady []string
	var getErr error
	check := func(name string, obj client.Object, ready func(o client.Object) (int32, int32)) {
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, obj); err != nil {
			if errors.IsNotFound(err) {
				notReady = append(notReady, name+" (missing)")
				return
			}
			getErr = err
			return
		}
		got, want := ready(obj)
		if got < want {
			notReady = append(notReady, fmt.Sprintf("%s (%d/%d)", name, got, want))
		}
	}
	sts := &appsv1.StatefulSet{}
	dep := &appsv1.Deployment{}
	replicas := func(o client.Object) (int32, int32) {
		s := o.(*appsv1.StatefulSet)
		return s.Status.ReadyReplicas, *s.Spec.Replicas
	}
	depReplicas := func(o client.Object) (int32, int32) {
		d := o.(*appsv1.Deployment)
		return d.Status.ReadyReplicas, *d.Spec.Replicas
	}
	for _, c := range []render.Component{render.ComponentPostfix, render.ComponentDovecot} {
		check(render.ServiceName(server.Name, c), sts, replicas)
	}
	for _, c := range []render.Component{render.ComponentRspamd, render.ComponentValkey} {
		check(render.ServiceName(server.Name, c), dep, depReplicas)
	}
	if getErr != nil {
		return false, getErr.Error()
	}
	if len(notReady) == 0 {
		return true, "all workloads are ready"
	}
	return false, strings.Join(notReady, ", ") + " not ready"
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
		if err := r.List(ctx, got,
			client.InNamespace(server.Namespace),
			client.MatchingLabels{manifests.InstanceLabelKey: server.Name},
		); err != nil {
			return metav1.ConditionFalse, "ListFailed", err.Error()
		}
		for i := range got.Items {
			if slices.ContainsFunc(want, func(w client.Object) bool {
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
		if err := applyObject(ctx, r.Client, r.Scheme, server, obj); err != nil {
			return metav1.ConditionFalse, ReasonApplyFailed, err.Error()
		}
	}
	if len(want) > 0 {
		return metav1.ConditionTrue, "MonitorsCreated",
			fmt.Sprintf("%d ServiceMonitors are managed", len(want))
	}
	return metav1.ConditionTrue, "Disabled", "no ServiceMonitors are created (prometheusOperator=disabled)"
}

// crdsAvailable reports whether the CRD with the given plural name is
// installed, cached for crdCacheTTL so integrations installed later
// are picked up without a restart (principle 10).
func (r *MailServerReconciler) crdsAvailable(ctx context.Context, name string) (bool, error) {
	r.crds.mu.Lock()
	defer r.crds.mu.Unlock()
	if time.Since(r.crds.checkedAt) < crdCacheTTL {
		return r.crds.available[name], nil
	}
	avail := make(map[string]bool, 2)
	for _, n := range []string{crdCertificate, crdServiceMonitor} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		err := r.Get(ctx, types.NamespacedName{Name: n}, crd)
		if errors.IsNotFound(err) {
			avail[n] = false
			continue
		}
		if err != nil {
			return false, err
		}
		avail[n] = true
	}
	r.crds.available = avail
	r.crds.checkedAt = time.Now()
	return avail[name], nil
}

// crdCache caches the results of crdsAvailable.
type crdCache struct {
	mu        sync.Mutex
	checkedAt time.Time
	available map[string]bool
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
		Watches(&cnmsv1alpha1.MailDomain{}, handler.EnqueueRequestsFromMapFunc(r.mapSameNamespaceServers)).
		Watches(&cnmsv1alpha1.MailDirectory{}, handler.EnqueueRequestsFromMapFunc(r.mapSameNamespaceServers)).
		Watches(&cnmsv1alpha1.MailDomainGrant{}, handler.EnqueueRequestsFromMapFunc(r.mapGrantServers)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSameNamespaceServers)).
		Complete(r)
}

// crdInstalled reports whether the CRD with the given name is installed
// (direct API read; the manager cache has not started yet at setup
// time). On an error other than NotFound the watch is skipped so that
// a transient API failure can never crash the operator (principle 10).
func crdInstalled(mgr ctrl.Manager, name string) bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := mgr.GetAPIReader().Get(context.Background(), client.ObjectKey{Name: name}, crd)
	if err == nil {
		return true
	}
	if !errors.IsNotFound(err) {
		log.Log.Info("Could not check the CRD at startup, skipping the optional watch",
			"crd", name, "error", err.Error())
	}
	return false
}

// mapSameNamespaceServers enqueues every MailServer of the object's
// namespace (a domain, directory or secret change may change the
// render or the authorization).
func (r *MailServerReconciler) mapSameNamespaceServers(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.serversInNamespace(ctx, obj.GetNamespace())
}

// mapGrantServers enqueues the MailServers of the namespace a grant
// points at (on delete events the handler passes the old object,
// whose spec still carries the namespace).
func (r *MailServerReconciler) mapGrantServers(ctx context.Context, obj client.Object) []reconcile.Request {
	grant, ok := obj.(*cnmsv1alpha1.MailDomainGrant)
	if !ok {
		return nil
	}
	return r.serversInNamespace(ctx, grant.Spec.Namespace)
}

func (r *MailServerReconciler) serversInNamespace(ctx context.Context, ns string) []reconcile.Request {
	list := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, list, client.InNamespace(ns)); err != nil {
		log.FromContext(ctx).Error(err, "Could not list MailServers", "namespace", ns)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Name: list.Items[i].Name, Namespace: list.Items[i].Namespace,
		}})
	}
	return reqs
}

// applyObject creates obj if it does not exist and updates the
// controller-owned fields if it does. Every object of the data plane
// set is fully owned by the MailServer (controller owner reference),
// so replacing the spec is safe.
func applyObject(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner, desired client.Object) error {
	existing := desired.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
			return err
		}
		return c.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// spec.volumeClaimTemplates is immutable on update (k8s 1.37),
	// so a changed template set requires recreating the StatefulSet.
	// The per-replica PVCs are not deleted with the StatefulSet
	// (default deletePolicy Keep) and the new StatefulSet reuses
	// them, so mailbox data survives the toggle.
	if d, ok := desired.(*appsv1.StatefulSet); ok {
		if e, ok := existing.(*appsv1.StatefulSet); ok &&
			!volumeClaimTemplatesEqual(e.Spec.VolumeClaimTemplates, d.Spec.VolumeClaimTemplates) {
			if err := c.Delete(ctx, e); err != nil && !errors.IsNotFound(err) {
				return err
			}
			if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
				return err
			}
			return c.Create(ctx, desired)
		}
	}
	mergeOwnedFields(existing, desired)
	return c.Update(ctx, existing)
}

// volumeClaimTemplatesEqual reports whether two template lists are
// equivalent (names and storage requests in order).
func volumeClaimTemplatesEqual(a, b []corev1.PersistentVolumeClaim) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
		if !a[i].Spec.Resources.Requests[corev1.ResourceStorage].
			Equal(b[i].Spec.Resources.Requests[corev1.ResourceStorage]) {
			return false
		}
	}
	return true
}

// mergeOwnedFields copies the controller-owned fields (labels,
// annotations, spec) from desired into the fetched existing object,
// preserving the server-managed status.
func mergeOwnedFields(existing, desired client.Object) {
	switch d := desired.(type) {
	case *corev1.ConfigMap:
		e := existing.(*corev1.ConfigMap)
		e.Labels, e.Annotations, e.Data = d.Labels, d.Annotations, d.Data
	case *corev1.Service:
		e := existing.(*corev1.Service)
		e.Labels, e.Annotations, e.Spec = d.Labels, d.Annotations, d.Spec
	case *corev1.PersistentVolumeClaim:
		e := existing.(*corev1.PersistentVolumeClaim)
		e.Labels, e.Annotations = d.Labels, d.Annotations
		e.Spec.Resources = d.Spec.Resources // the only resizable field
	case *appsv1.StatefulSet:
		e := existing.(*appsv1.StatefulSet)
		e.Labels, e.Annotations, e.Spec = d.Labels, d.Annotations, d.Spec
	case *appsv1.Deployment:
		e := existing.(*appsv1.Deployment)
		e.Labels, e.Annotations, e.Spec = d.Labels, d.Annotations, d.Spec
	case *policyv1.PodDisruptionBudget:
		e := existing.(*policyv1.PodDisruptionBudget)
		e.Labels, e.Annotations, e.Spec = d.Labels, d.Annotations, d.Spec
	case *networkingv1.NetworkPolicy:
		e := existing.(*networkingv1.NetworkPolicy)
		e.Labels, e.Annotations, e.Spec = d.Labels, d.Annotations, d.Spec
	default:
		// Unstructured objects (Certificate, ServiceMonitor).
		u := desired.(*unstructured.Unstructured)
		e := existing.(*unstructured.Unstructured)
		e.SetLabels(u.GetLabels())
		spec, found, _ := unstructured.NestedMap(u.Object, "spec")
		if found {
			_ = unstructured.SetNestedMap(e.Object, spec, "spec")
		}
	}
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

// conditionReason picks the reason of a simple true/false condition.
func conditionReason(ok bool, trueReason, falseReason string) string {
	if ok {
		return trueReason
	}
	return falseReason
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
	return "Ready", "all managed workloads are running with validated configuration"
}
