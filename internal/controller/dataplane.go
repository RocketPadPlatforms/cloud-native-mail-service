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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/dovecot"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/postfix"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/rspamd"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/valkey"
)

// ConditionConfigValidated reports the validation gate (ADR 0009):
// True when the configuration of every workload was validated,
// Unknown while the validation Jobs run, False when a Job failed.
const ConditionConfigValidated = "ConfigValidated"

// ConfigValidated condition reasons.
const (
	// ReasonConfigValidated means the new configuration passed.
	ReasonConfigValidated = "Validated"
	// ReasonValidatingConfig means a validation Job is still running.
	ReasonValidatingConfig = "ValidatingConfig"
	// ReasonConfigRejected means a validation Job failed; the workloads
	// keep running their previous configuration.
	ReasonConfigRejected = "ConfigRejected"
)

// validationState is the outcome of the validation gate.
type validationState int

const (
	validationPassed validationState = iota
	validationRunning
	validationRejected
)

// gate is the result of applying the data plane.
type gate struct {
	state   validationState
	message string
	// want maps a workload name to its desired config hash; set only
	// when the workloads were applied.
	want map[string]string
	// plane is the desired object set (for the stale cleanup).
	plane *manifests.DataPlane
}

// renderDataPlane renders every component and builds the staged
// object set.
func renderDataPlane(server *cnmsv1alpha1.MailServer, authorized []string, users []render.StaticUser) (*manifests.DataPlane, error) {
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
			return nil, fmt.Errorf("render %s: %w", renderer.Component(), err)
		}
		renders[renderer.Component()] = result
	}
	return manifests.Build(req, renders)
}

// applyDataPlane creates the immutable configuration and the objects
// that do not depend on it, runs the validation gate, and applies the
// workloads only when every validation Job for a changed
// configuration succeeded. A rejected or running validation leaves
// the existing workloads untouched (fail closed, ADR 0009).
func (r *MailServerReconciler) applyDataPlane(ctx context.Context, server *cnmsv1alpha1.MailServer, authorized []string, users []render.StaticUser, certCRDs bool) (*gate, error) {
	plane, err := renderDataPlane(server, authorized, users)
	if err != nil {
		return nil, err
	}
	// Immutable objects are created, never updated; the pods of the
	// previous revision keep theirs.
	for _, obj := range plane.Configs {
		if err := createOwned(ctx, r.Client, r.APIReader, r.Scheme, server, obj); err != nil {
			return nil, fmt.Errorf("create %T %s: %w", obj, obj.GetName(), err)
		}
	}
	for _, obj := range plane.Base {
		// Without the cert-manager CRDs the Certificate is not
		// created; a pre-created Secret still satisfies TLS.
		if !certCRDs && isUnstructuredKind(obj, certificateGVK.Kind) {
			continue
		}
		if err := applyObject(ctx, r.Client, r.Scheme, server, obj); err != nil {
			return nil, fmt.Errorf("apply %T %s: %w", obj, obj.GetName(), err)
		}
	}

	g := &gate{plane: plane}
	var running, rejected []string
	for _, v := range plane.Validations {
		// A workload that already runs this configuration passed the
		// gate before; do not validate (or re-validate after the Job
		// TTL) it again.
		current, err := r.runningHash(ctx, server, v.Workload, v.Component)
		if err != nil {
			return nil, err
		}
		if current == v.Hash {
			continue
		}
		job, err := r.ensureJob(ctx, server, v.Job)
		if err != nil {
			return nil, fmt.Errorf("create Job %s: %w", v.Job.Name, err)
		}
		switch passed, failure := jobOutcome(job); {
		case failure != "":
			rejected = append(rejected, fmt.Sprintf(
				"the %s configuration was rejected by Job %s: %s", v.Component, job.Name, failure))
		case !passed:
			running = append(running, string(v.Component))
		}
	}
	switch {
	case len(rejected) > 0:
		g.state = validationRejected
		g.message = strings.Join(rejected, "; ") + "; the previous configuration keeps running"
		return g, nil
	case len(running) > 0:
		g.state = validationRunning
		g.message = "validating the " + strings.Join(running, ", ") + " configuration"
		return g, nil
	}

	for _, obj := range plane.Workloads {
		if err := applyObject(ctx, r.Client, r.Scheme, server, obj); err != nil {
			return nil, fmt.Errorf("apply %T %s: %w", obj, obj.GetName(), err)
		}
	}
	g.want = workloadHashes(plane.Workloads)
	g.message = "the configuration of all components was validated"
	return g, nil
}

// runningHash returns the config hash of the pod template of an
// existing workload, or "" when the workload does not exist yet.
func (r *MailServerReconciler) runningHash(ctx context.Context, server *cnmsv1alpha1.MailServer, name string, c render.Component) (string, error) {
	key := types.NamespacedName{Name: name, Namespace: server.Namespace}
	var template *corev1.PodTemplateSpec
	switch c {
	case render.ComponentPostfix, render.ComponentDovecot:
		sts := &appsv1.StatefulSet{}
		if err := r.Get(ctx, key, sts); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		template = &sts.Spec.Template
	default:
		dep := &appsv1.Deployment{}
		if err := r.Get(ctx, key, dep); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		template = &dep.Spec.Template
	}
	return template.Annotations[manifests.ConfigHashAnnotation], nil
}

// ensureJob creates the validation Job if it does not exist and
// returns the current object. A Job that was just created is
// returned as created (no status yet).
func (r *MailServerReconciler) ensureJob(ctx context.Context, server *cnmsv1alpha1.MailServer, job *batchv1.Job) (*batchv1.Job, error) {
	existing := &batchv1.Job{}
	err := r.Get(ctx, client.ObjectKeyFromObject(job), existing)
	if err == nil {
		// A Job of someone else (a tenant could pre-create one that
		// "passed") must never count as the validation result.
		if !metav1.IsControlledBy(existing, server) {
			return nil, &ownershipConflictError{kind: kindName(job), name: job.Name}
		}
		return existing, nil
	}
	if !errors.IsNotFound(err) {
		return nil, err
	}
	if err := createOwned(ctx, r.Client, r.APIReader, r.Scheme, server, job); err != nil {
		return nil, err
	}
	// Either just created (no status yet) or created concurrently;
	// the Job watch triggers the next reconcile.
	return job, nil
}

// jobOutcome reports whether a Job completed, or the failure message
// of a failed one.
func jobOutcome(job *batchv1.Job) (passed bool, failure string) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, ""
		case batchv1.JobFailed:
			msg := c.Reason
			if c.Message != "" {
				msg += ": " + c.Message
			}
			return false, msg
		}
	}
	return false, ""
}

// createOwned creates obj with a controller reference to owner. An
// existing object controlled by owner is success: the objects it is
// used for are immutable and content-named, so the same name means the
// same content. One that owner does not control (a tenant may create
// objects in the namespace under the same name) is an
// ownershipConflictError: its content is not ours. The existing object
// is read with reader, which must not be the cache: the cache only
// holds the operator's own (labelled) objects, so it cannot see a
// foreign one.
func createOwned(ctx context.Context, c client.Client, reader client.Reader, scheme *runtime.Scheme, owner, obj client.Object) error {
	if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
		return err
	}
	err := c.Create(ctx, obj)
	if err == nil || !errors.IsAlreadyExists(err) {
		return err
	}
	existing := obj.DeepCopyObject().(client.Object)
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		// Gone again: the next pass decides.
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(existing, owner) {
		return &ownershipConflictError{kind: kindName(obj), name: obj.GetName()}
	}
	return nil
}

// cleanupStale deletes the immutable configuration objects and the
// validation Jobs of this MailServer that no workload pod template and
// no desired object references anymore. It must run only in a pass
// that applied the workloads (want holds their config hashes) and
// found the rollout finished: before that, pods of the previous
// revision may still be (re)created from the old template. The
// templates are read uncached and must carry the wanted hash, so a
// lagging cache can never make a referenced object look stale.
func (r *MailServerReconciler) cleanupStale(ctx context.Context, server *cnmsv1alpha1.MailServer, plane *manifests.DataPlane, want map[string]string) error {
	keep := map[string]bool{}
	for _, obj := range plane.Configs {
		keep[obj.GetName()] = true
	}
	for _, v := range plane.Validations {
		keep[v.Job.Name] = true
	}
	for _, c := range render.AllComponents() {
		name := render.ServiceName(server.Name, c)
		key := types.NamespacedName{Name: name, Namespace: server.Namespace}
		var template *corev1.PodTemplateSpec
		switch c {
		case render.ComponentPostfix, render.ComponentDovecot:
			sts := &appsv1.StatefulSet{}
			if err := r.APIReader.Get(ctx, key, sts); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return err
			}
			template = &sts.Spec.Template
		default:
			dep := &appsv1.Deployment{}
			if err := r.APIReader.Get(ctx, key, dep); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return err
			}
			template = &dep.Spec.Template
		}
		if template.Annotations[manifests.ConfigHashAnnotation] != want[name] {
			return nil // not the template this pass applied; retry next pass
		}
		for _, v := range template.Spec.Volumes {
			if v.ConfigMap != nil {
				keep[v.ConfigMap.Name] = true
			}
			if v.Secret != nil {
				keep[v.Secret.SecretName] = true
			}
		}
	}

	owned := []client.ListOption{
		client.InNamespace(server.Namespace),
		client.MatchingLabels{
			manifests.InstanceLabelKey: server.Name,
			manifests.ManagedByLabel:   manifests.ManagedByValue,
		},
	}
	var stale []client.Object
	collect := func(items []client.Object) {
		for _, o := range items {
			if !keep[o.GetName()] && metav1.IsControlledBy(o, server) {
				stale = append(stale, o)
			}
		}
	}
	cms := &corev1.ConfigMapList{}
	if err := r.List(ctx, cms, owned...); err != nil {
		return err
	}
	for i := range cms.Items {
		collect([]client.Object{&cms.Items[i]})
	}
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, owned...); err != nil {
		return err
	}
	for i := range secrets.Items {
		collect([]client.Object{&secrets.Items[i]})
	}
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, owned...); err != nil {
		return err
	}
	for i := range jobs.Items {
		collect([]client.Object{&jobs.Items[i]})
	}
	for _, o := range stale {
		// Background propagation also removes the Job pods.
		if err := r.Delete(ctx, o, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
