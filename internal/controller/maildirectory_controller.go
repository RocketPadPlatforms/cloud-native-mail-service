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
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
)

// MailDirectory condition types (ADR 0015, section 5). Ready says the
// configuration is valid; Reachable says a probe worked. Reachable is a
// warning: nothing else reads it, so an outage of the directory (or a
// network that lets the operator pod see less than the data plane) never
// takes a rollout away from a MailServer.
const (
	// ConditionReachable is true when the last probe connected, bound
	// and found at least one account with the composed filter.
	ConditionReachable = "Reachable"
)

// MailDirectory condition reasons. The outcomes of a probe are the
// reasons of Reachable; the Ready reasons for a bad configuration come
// first.
const (
	// ReasonSecretNotFound is set when a referenced Secret is missing.
	// It is the reason MailDomain already uses for its key Secrets.
	ReasonSecretNotFound = ReasonKeySecretNotFound
	// ReasonSecretIncomplete is set when a Secret lacks the key that is
	// read from it, or the CA bundle holds no certificate.
	ReasonSecretIncomplete = "SecretIncomplete"
	// ReasonInvalidFilter is set when the active filter, or the filter
	// composed with it, is not a valid LDAP filter.
	ReasonInvalidFilter = string(directory.InvalidFilter)
	// ReasonReachable and the following are the probe outcomes.
	ReasonReachable         = string(directory.Reachable)
	ReasonUnreachable       = string(directory.Unreachable)
	ReasonBindFailed        = string(directory.BindFailed)
	ReasonTLSFailed         = string(directory.TLSFailed)
	ReasonNoMatchingEntries = string(directory.NoMatchingEntries)
	ReasonRelationMissing   = string(directory.RelationMissing)
)

const (
	// directoryProbeInterval is how often a directory is probed again
	// and, behind that, the safety net for a configuration that is
	// valid; a spec change probes at once.
	directoryProbeInterval = 5 * time.Minute
	// directorySecretRetry is how often a directory that waits for a
	// Secret looks again: Secrets are not watched (ADR 0009).
	directorySecretRetry = time.Minute
	// directoryProbeTimeout bounds one whole probe.
	directoryProbeTimeout = 30 * time.Second
)

// MailDirectoryReconciler reconciles a MailDirectory object: it checks
// the configuration and probes the directory. It renders nothing; the
// MailServer controller reads the directory.
type MailDirectoryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the Secrets without the cache (mgr.GetAPIReader()).
	APIReader client.Reader
	// Prober connects to the directory; tests pass a fake.
	Prober directory.Prober
	// Recorder emits Events when the state changes (nil in tests).
	Recorder recorder.EventRecorder
}

// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildirectories,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=maildirectories/status,verbs=patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile validates the configuration of the directory and, when it is
// valid, probes it (ADR 0015, section 5). The error texts of the drivers
// can carry a credential, so only a reason and the host are logged and
// only fixed messages reach the status and the Events.
func (r *MailDirectoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	dir := &cnmsv1alpha1.MailDirectory{}
	if err := r.Get(ctx, req.NamespacedName, dir); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := dir.DeepCopy()
	prevReady := meta.FindStatusCondition(orig.Status.Conditions, conditionReady)
	prevReachable := meta.FindStatusCondition(orig.Status.Conditions, ConditionReachable)

	target, bad, err := r.resolve(ctx, dir)
	if err != nil {
		return ctrl.Result{}, err
	}
	var result ctrl.Result
	if bad != nil {
		setCondition(&dir.Status.Conditions, dir.Generation, conditionReady, metav1.ConditionFalse, bad.reason, bad.message)
		setCondition(&dir.Status.Conditions, dir.Generation, ConditionReachable, metav1.ConditionUnknown, bad.reason,
			"the directory is not probed while the configuration is invalid")
		if bad.reason != ReasonInvalidFilter {
			result.RequeueAfter = directorySecretRetry
		}
		log.Info("Directory configuration is invalid", "reason", bad.reason)
	} else {
		setCondition(&dir.Status.Conditions, dir.Generation, conditionReady, metav1.ConditionTrue, ReasonReady,
			"the configuration is valid")
		probeCtx, cancel := context.WithTimeout(ctx, directoryProbeTimeout)
		outcome := target.probe(probeCtx, r.Prober)
		cancel()
		setCondition(&dir.Status.Conditions, dir.Generation, ConditionReachable,
			conditionStatus(outcome == directory.Reachable), string(outcome), probeMessage(dir.Spec.Type, outcome))
		result.RequeueAfter = directoryProbeInterval
		// The periodic probe of an unchanged directory is debug noise.
		probeLog := log.V(1)
		if changed(prevReachable, meta.FindStatusCondition(dir.Status.Conditions, ConditionReachable)) {
			probeLog = log
		}
		probeLog.Info("Probed directory", "host", target.host(), "outcome", string(outcome))
	}
	dir.Status.ObservedGeneration = dir.Generation

	r.emitEvents(dir, prevReady, prevReachable)
	if !equality.Semantic.DeepEqual(orig.Status, dir.Status) {
		if err := r.Status().Patch(ctx, dir, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// badConfig is why a directory is not Ready.
type badConfig struct{ reason, message string }

// probeTarget is the resolved configuration of one directory.
type probeTarget struct {
	ldap *directory.LDAPTarget
	sql  *directory.SQLTarget
}

func (t probeTarget) host() string {
	if t.ldap != nil {
		return t.ldap.Host
	}
	return t.sql.Host
}

func (t probeTarget) probe(ctx context.Context, p directory.Prober) directory.Outcome {
	if t.ldap != nil {
		return p.ProbeLDAP(ctx, *t.ldap)
	}
	return p.ProbeSQL(ctx, *t.sql)
}

// resolve checks the configuration without any network access to the
// directory and reads the Secrets. A non-nil badConfig means not Ready;
// an error is a failure to read from the API server and is retried.
func (r *MailDirectoryReconciler) resolve(ctx context.Context, dir *cnmsv1alpha1.MailDirectory) (probeTarget, *badConfig, error) {
	switch {
	case dir.Spec.Type == cnmsv1alpha1.DirectoryTypeLDAP && dir.Spec.LDAP != nil:
		return r.resolveLDAP(ctx, dir.Namespace, dir.Spec.LDAP)
	case dir.Spec.Type == cnmsv1alpha1.DirectoryTypeSQL && dir.Spec.SQL != nil:
		return r.resolveSQL(ctx, dir.Namespace, dir.Spec.SQL)
	}
	// The CRD refuses this (exactly one of ldap or sql, matching type).
	return probeTarget{}, nil, errInvalidDirectorySpec
}

// errInvalidDirectorySpec cannot happen with the CRD installed.
var errInvalidDirectorySpec = errors.New("the settings of the MailDirectory do not match its type")

func (r *MailDirectoryReconciler) resolveLDAP(ctx context.Context, ns string, l *cnmsv1alpha1.LDAP) (probeTarget, *badConfig, error) {
	t := &directory.LDAPTarget{
		Host: l.Host, BaseDN: l.BaseDN, TLSMode: directory.LDAPStartTLS,
		Attributes: directory.Attributes{Login: directory.DefaultLogin, Mail: directory.DefaultMail},
	}
	if l.Port != nil {
		t.Port = int(*l.Port)
	}
	if l.TLS != nil && l.TLS.Mode != nil {
		t.TLSMode = *l.TLS.Mode
	}
	if a := l.Attributes; a != nil {
		if a.Login != nil {
			t.Attributes.Login = *a.Login
		}
		if a.Mail != nil {
			t.Attributes.Mail = *a.Mail
		}
		if a.Aliases != nil {
			t.Attributes.Aliases = *a.Aliases
		}
	}
	if l.ActiveFilter != nil {
		t.ActiveFilter = *l.ActiveFilter
	}
	if _, err := directory.ProbeFilter(t.Attributes, t.ActiveFilter); err != nil {
		return probeTarget{}, &badConfig{ReasonInvalidFilter, "the active filter is not a valid LDAP filter"}, nil
	}
	if l.BindDN != nil && *l.BindDN != "" && l.BindSecretRef != nil {
		pw, bad, err := r.secretKey(ctx, ns, l.BindSecretRef.Name, "password")
		if bad != nil || err != nil {
			return probeTarget{}, bad, err
		}
		t.BindDN, t.BindPassword = *l.BindDN, string(pw)
	}
	if l.TLS != nil && l.TLS.CASecretRef != nil {
		ca, bad, err := r.caBundle(ctx, ns, l.TLS.CASecretRef.Name)
		if bad != nil || err != nil {
			return probeTarget{}, bad, err
		}
		t.CA = ca
	}
	return probeTarget{ldap: t}, nil, nil
}

func (r *MailDirectoryReconciler) resolveSQL(ctx context.Context, ns string, s *cnmsv1alpha1.SQL) (probeTarget, *badConfig, error) {
	t := &directory.SQLTarget{
		Host: s.Host, Port: 5432, TLSMode: directory.SQLVerifyFull, Database: s.Database, Username: s.Username,
		Users: directory.Relation{Schema: "public", Name: "cnms_users"},
	}
	if s.Port != nil {
		t.Port = int(*s.Port)
	}
	if s.TLS != nil && s.TLS.Mode != nil {
		t.TLSMode = *s.TLS.Mode
	}
	if s.Users != nil {
		t.Users = relation(*s.Users)
	}
	if s.Aliases != nil {
		a := relation(*s.Aliases)
		t.Aliases = &a
	}
	pw, bad, err := r.secretKey(ctx, ns, s.PasswordSecretRef.Name, "password")
	if bad != nil || err != nil {
		return probeTarget{}, bad, err
	}
	t.Password = string(pw)
	if s.TLS != nil && s.TLS.CASecretRef != nil {
		ca, bad, err := r.caBundle(ctx, ns, s.TLS.CASecretRef.Name)
		if bad != nil || err != nil {
			return probeTarget{}, bad, err
		}
		t.CA = ca
	}
	return probeTarget{sql: t}, nil, nil
}

// relation converts an API relation; an unset schema is "public".
func relation(r cnmsv1alpha1.SQLRelation) directory.Relation {
	out := directory.Relation{Schema: "public", Name: r.Name}
	if r.Schema != nil {
		out.Schema = *r.Schema
	}
	return out
}

// secretKey reads one key of a Secret of the namespace. The message
// names the Secret and the key, never a value.
func (r *MailDirectoryReconciler) secretKey(ctx context.Context, ns, name, key string) ([]byte, *badConfig, error) {
	s := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &badConfig{ReasonSecretNotFound, "Secret " + name + " not found"}, nil
		}
		return nil, nil, err
	}
	if len(s.Data[key]) == 0 {
		return nil, &badConfig{ReasonSecretIncomplete, "Secret " + name + " has no key " + key}, nil
	}
	return s.Data[key], nil, nil
}

// caBundle reads the key "ca.crt" and checks that it holds a certificate.
func (r *MailDirectoryReconciler) caBundle(ctx context.Context, ns, name string) ([]byte, *badConfig, error) {
	ca, bad, err := r.secretKey(ctx, ns, name, "ca.crt")
	if bad != nil || err != nil {
		return nil, bad, err
	}
	if !directory.ValidCA(ca) {
		return nil, &badConfig{ReasonSecretIncomplete, "key ca.crt of Secret " + name + " holds no certificate"}, nil
	}
	return ca, nil, nil
}

// probeMessage is the fixed text of a probe outcome: it never carries a
// driver error, a bind DN or a connection string.
func probeMessage(typ cnmsv1alpha1.DirectoryType, o directory.Outcome) string {
	switch o {
	case directory.Reachable:
		return "the directory answered and the lookup found accounts"
	case directory.Unreachable:
		return "the directory did not answer"
	case directory.BindFailed:
		return "the directory rejected the credentials or the permissions of the lookup"
	case directory.TLSFailed:
		return "the TLS handshake or the verification of the server certificate failed"
	case directory.RelationMissing:
		return "a schema, a relation or a column of the directory does not exist"
	case directory.NoMatchingEntries:
		if typ == cnmsv1alpha1.DirectoryTypeSQL {
			return "the users relation holds no account that passes the login guard"
		}
		return "the lookup with the composed filter found no account"
	}
	return "the probe failed"
}

// changed reports whether a condition has a new status or reason.
func changed(prev, cur *metav1.Condition) bool {
	return prev == nil || prev.Status != cur.Status || prev.Reason != cur.Reason
}

// emitEvents records an Event when Ready or Reachable change for the
// worse, and when Reachable comes back.
func (r *MailDirectoryReconciler) emitEvents(dir *cnmsv1alpha1.MailDirectory, prevReady, prevReachable *metav1.Condition) {
	if r.Recorder == nil {
		return
	}
	ready := meta.FindStatusCondition(dir.Status.Conditions, conditionReady)
	reachable := meta.FindStatusCondition(dir.Status.Conditions, ConditionReachable)
	if ready.Status == metav1.ConditionFalse && changed(prevReady, ready) {
		r.Recorder.Eventf(dir, nil, corev1.EventTypeWarning, ready.Reason, "Validate", "%s", eventMessage(ready.Message))
	}
	switch {
	case reachable.Status == metav1.ConditionFalse && changed(prevReachable, reachable):
		r.Recorder.Eventf(dir, nil, corev1.EventTypeWarning, reachable.Reason, "Probe", "%s", eventMessage(reachable.Message))
	case reachable.Status == metav1.ConditionTrue && prevReachable != nil && prevReachable.Status == metav1.ConditionFalse:
		r.Recorder.Eventf(dir, nil, corev1.EventTypeNormal, reachable.Reason, "Probe", "%s", eventMessage(reachable.Message))
	}
}

// SetupWithManager sets up the controller with the Manager. Only a spec
// change (a new generation) reacts at once; the periodic requeue runs the
// probe again, and status writes do not trigger a probe.
func (r *MailDirectoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailDirectory{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("maildirectory").
		// A probe can take up to directoryProbeTimeout; with one worker a few
		// slow or dead directories would delay the probes of all others.
		WithOptions(runtimecontroller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}
