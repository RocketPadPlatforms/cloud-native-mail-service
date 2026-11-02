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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// WorkloadsReady condition reasons.
const (
	// ReasonAllWorkloadsReady is set when every workload finished its
	// rollout and all replicas are ready.
	ReasonAllWorkloadsReady = "AllWorkloadsReady"
	// ReasonProgressing is set while a rollout or the first start is
	// still converging.
	ReasonProgressing = "Progressing"
	// ReasonRolloutStalled is set when a rollout is not converging:
	// a Deployment exceeded its progress deadline, or the workloads
	// stayed not ready for longer than rolloutDeadline.
	ReasonRolloutStalled = "RolloutStalled"
)

// rolloutDeadline is how long the workloads may stay not ready before
// the rollout is reported as stalled. It matches the default
// progressDeadlineSeconds of a Deployment (600s); StatefulSets have no
// deadline of their own.
const rolloutDeadline = 10 * time.Minute

// rolloutStatus is the result of the workload convergence check.
type rolloutStatus struct {
	// ready is true when every workload runs its desired template with
	// all replicas ready.
	ready bool
	// stalled is true when the rollout does not converge.
	stalled bool
	message string
}

// reason is the WorkloadsReady condition reason.
func (s rolloutStatus) reason() string {
	switch {
	case s.ready:
		return ReasonAllWorkloadsReady
	case s.stalled:
		return ReasonRolloutStalled
	}
	return ReasonProgressing
}

// workloadsReady reports whether all four data plane workloads
// finished their rollout: the controller observed the latest spec,
// every replica runs the desired template (want maps the workload
// name to its config hash) and is ready. prev is the previous
// WorkloadsReady condition; it dates the start of a not-ready period
// for the stall detection.
func (r *MailServerReconciler) workloadsReady(ctx context.Context, server *cnmsv1alpha1.MailServer, want map[string]string, prev *metav1.Condition) rolloutStatus {
	var problems []string
	var stalled bool
	note := func(name, msg string) { problems = append(problems, fmt.Sprintf("%s (%s)", name, msg)) }
	get := func(name string, obj client.Object) bool {
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: server.Namespace}, obj)
		switch {
		case err == nil:
			return true
		case errors.IsNotFound(err):
			note(name, "missing")
		default:
			note(name, err.Error())
		}
		return false
	}
	for _, c := range []render.Component{render.ComponentPostfix, render.ComponentDovecot} {
		name := render.ServiceName(server.Name, c)
		sts := &appsv1.StatefulSet{}
		if get(name, sts) {
			if msg := statefulSetProgress(sts, want[name]); msg != "" {
				note(name, msg)
			}
		}
	}
	for _, c := range []render.Component{render.ComponentRspamd, render.ComponentValkey} {
		name := render.ServiceName(server.Name, c)
		dep := &appsv1.Deployment{}
		if get(name, dep) {
			msg, deadline := deploymentProgress(dep, want[name])
			if msg != "" {
				note(name, msg)
			}
			stalled = stalled || deadline
		}
	}
	if len(problems) == 0 {
		return rolloutStatus{ready: true, message: "all workloads are ready"}
	}
	if prev != nil && prev.Status == metav1.ConditionFalse &&
		time.Since(prev.LastTransitionTime.Time) > rolloutDeadline {
		stalled = true
	}
	return rolloutStatus{stalled: stalled, message: strings.Join(problems, ", ")}
}

// statefulSetProgress returns "" when the StatefulSet finished rolling
// out wantHash, else a short description of what is pending.
func statefulSetProgress(s *appsv1.StatefulSet, wantHash string) string {
	replicas := int32(1)
	if s.Spec.Replicas != nil {
		replicas = *s.Spec.Replicas
	}
	switch {
	case wantHash != "" && s.Spec.Template.Annotations[manifests.ConfigHashAnnotation] != wantHash:
		return "waiting for the new template"
	case s.Status.ObservedGeneration != s.Generation:
		return "spec not observed yet"
	case s.Status.UpdatedReplicas != replicas:
		return fmt.Sprintf("updated %d/%d", s.Status.UpdatedReplicas, replicas)
	case s.Status.ReadyReplicas < replicas:
		return fmt.Sprintf("ready %d/%d", s.Status.ReadyReplicas, replicas)
	case s.Status.UpdateRevision == "" || s.Status.CurrentRevision != s.Status.UpdateRevision:
		return "revision rollout in progress"
	}
	return ""
}

// deploymentProgress returns "" when the Deployment finished rolling
// out wantHash, else a description; deadline reports an exceeded
// progress deadline.
func deploymentProgress(d *appsv1.Deployment, wantHash string) (msg string, deadline bool) {
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
			return "progress deadline exceeded", true
		}
	}
	switch {
	case wantHash != "" && d.Spec.Template.Annotations[manifests.ConfigHashAnnotation] != wantHash:
		return "waiting for the new template", false
	case d.Status.ObservedGeneration != d.Generation:
		return "spec not observed yet", false
	case d.Status.UpdatedReplicas != replicas:
		return fmt.Sprintf("updated %d/%d", d.Status.UpdatedReplicas, replicas), false
	case d.Status.Replicas != replicas:
		return fmt.Sprintf("%d replicas instead of %d", d.Status.Replicas, replicas), false
	case d.Status.ReadyReplicas < replicas || d.Status.UnavailableReplicas > 0:
		return fmt.Sprintf("ready %d/%d", d.Status.ReadyReplicas, replicas), false
	}
	return "", false
}
