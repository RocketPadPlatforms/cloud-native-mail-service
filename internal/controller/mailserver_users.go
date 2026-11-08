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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render/dovecot"
)

// mailUserDebounce is how long a change of a MailUser waits before the server
// is reconciled (ADR 0015, section 4.7): the delaying queue keeps the earliest
// time of a key, so the changes of a burst (ten users created together) end in
// one reconcile and one revision of the Postfix maps, not ten.
const mailUserDebounce = 3 * time.Second

// serverMailUsers lists the MailUsers that name the server. The list is read
// from the cache without copies and is only read: the field selector of the
// index is not used because the reconciler filters (ADR 0014), and a list of
// one namespace is cheap.
func (r *MailServerReconciler) serverMailUsers(ctx context.Context, server *cnmsv1alpha1.MailServer) ([]cnmsv1alpha1.MailUser, error) {
	all := &cnmsv1alpha1.MailUserList{}
	if err := r.List(ctx, all, client.InNamespace(server.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	var out []cnmsv1alpha1.MailUser
	for i := range all.Items {
		if all.Items[i].Spec.ServerRef.Name == server.Name {
			out = append(out, all.Items[i])
		}
	}
	return out, nil
}

// mailUserSource decides the addresses of the MailUsers of the server and
// turns the verdict into the accounts of the render. The input of the
// decision is built by serverAddressInput, the function the MailUser
// controller builds its status from, so the rows that a user reports are the
// rows that are rendered (ADR 0015, section 3, point 6).
func (r *MailServerReconciler) mailUserSource(ctx context.Context, server *cnmsv1alpha1.MailServer, users []cnmsv1alpha1.MailUser, domains *cnmsv1alpha1.MailDomainList, ev *authorizationEvaluator) (userSource, error) {
	hashes, err := r.credentialHashes(ctx, server, users)
	if err != nil {
		return userSource{}, err
	}
	in, _ := serverAddressInput(server.Name, users, namespaceDomains(domains, server.Namespace), ev)
	res := addresses.Decide(in)
	return userSource{
		accounts:   render.NewAccounts(res, in.Users, func(name string) string { return hashes[name] }),
		conflicted: res.Conflicted,
		verdict:    &res,
	}, nil
}

// credentialHashes reads the password hashes of the users with one list of
// the credential Secrets of the server, selected by label (ADR 0015, section
// 6), not one get per user. The result is by account name. A hash is taken
// only from a Secret that the MailUser controls and only if it has the shape
// the operator writes: the Secret is in a namespace of the tenant, and
// whatever is in it ends in a file that Dovecot parses. A user without such a
// hash has no login; its mail is delivered all the same.
func (r *MailServerReconciler) credentialHashes(ctx context.Context, server *cnmsv1alpha1.MailServer, users []cnmsv1alpha1.MailUser) (map[string]string, error) {
	sel := labels.NewSelector()
	for _, req := range []struct {
		key string
		op  selection.Operator
		val []string
	}{
		{manifests.ServerLabel, selection.Equals, []string{server.Name}},
		{manifests.ManagedByLabel, selection.Equals, []string{manifests.ManagedByValue}},
		// The data plane's own Secrets carry the instance label too.
		{manifests.InstanceLabelKey, selection.DoesNotExist, nil},
	} {
		requirement, err := labels.NewRequirement(req.key, req.op, req.val)
		if err != nil {
			return nil, err
		}
		sel = sel.Add(*requirement)
	}
	list := &corev1.SecretList{}
	if err := r.APIReader.List(ctx, list, client.InNamespace(server.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, err
	}
	byName := make(map[string]*corev1.Secret, len(list.Items))
	for i := range list.Items {
		byName[list.Items[i].Name] = &list.Items[i]
	}
	hashes := make(map[string]string, len(users))
	for i := range users {
		s := byName[credentialSecretName(users[i].Name)]
		if s == nil || !metav1.IsControlledBy(s, &users[i]) {
			continue
		}
		if hash := string(s.Data[keyPasswordHash]); validStoredHash(hash) {
			hashes[users[i].Name] = hash
		}
	}
	return hashes, nil
}

// validStoredHash reports whether a hash from a credential Secret has the
// form the MailUser controller writes (parseHash accepts nothing else).
func validStoredHash(hash string) bool {
	_, _, ok := parseHash(hash)
	return ok
}

// errLiveUsersRaced says that the live users Secret changed between the read
// and the write of the operator. It is not a failure.
var errLiveUsersRaced = stderrors.New("the live users Secret changed while it was written")

// liveUsersRetry is the delay before the reconcile reads the Secret again.
const liveUsersRetry = time.Second

// applyLiveUsers writes the live users Secret of Dovecot in place (ADR 0015,
// section 4.7). The Secret has a stable name and is not part of the config
// hash: the kubelet projects a changed Secret into the pod, so a user is
// added, changed or removed with no revision, no validation Job and no
// restart. The update carries the resourceVersion that was read; a conflict
// (or a create that finds the Secret) is errLiveUsersRaced, which the
// reconciler answers with a short requeue and no change of Ready. A Secret
// of that name that the server does not control is never overwritten. With
// createMissing unset a missing Secret stays missing.
func (r *MailServerReconciler) applyLiveUsers(ctx context.Context, server *cnmsv1alpha1.MailServer, desired *corev1.Secret, createMissing bool) error {
	existing := &corev1.Secret{}
	err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		if !createMissing {
			return nil
		}
		if err := controllerutil.SetControllerReference(server, desired, r.Scheme); err != nil {
			return err
		}
		err := r.Create(ctx, desired)
		if errors.IsAlreadyExists(err) {
			return errLiveUsersRaced
		}
		return err
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(existing, server) {
		return &ownershipConflictError{kind: "Secret", name: desired.Name, owner: ownerMailServer}
	}
	labelsChanged := false
	for k, v := range desired.Labels {
		if existing.Labels[k] != v {
			if existing.Labels == nil {
				existing.Labels = map[string]string{}
			}
			existing.Labels[k], labelsChanged = v, true
		}
	}
	if !labelsChanged && equality.Semantic.DeepEqual(existing.Data, desired.Data) {
		return nil
	}
	existing.Data = desired.Data
	err = r.Update(ctx, existing)
	if errors.IsConflict(err) {
		return errLiveUsersRaced
	}
	return err
}

// revokeLogins empties the live users Secret of a server that has no user
// source any more (its last MailUser was deleted) while the running
// revision is kept: the previous revision keeps running (ADR 0009), but a
// removed account must not keep its login (ADR 0015, section 4.6). The
// Postfix maps stay as they are until the server has a source again.
func (r *MailServerReconciler) revokeLogins(ctx context.Context, server *cnmsv1alpha1.MailServer) error {
	res, err := dovecot.New().Render(&render.RenderRequest{Server: server})
	if err != nil {
		return err
	}
	return r.applyLiveUsers(ctx, server, manifests.LiveUsersSecret(server, res.LiveData), false)
}

// setAddressesCondition sets the AddressesServed warning of the server (ADR
// 0015, section 8): it never feeds into Ready. It exists while the server is
// served by MailUsers, and is removed otherwise. An Event is emitted when
// some address stops being served.
func (r *MailServerReconciler) setAddressesCondition(server *cnmsv1alpha1.MailServer, v *addresses.Result) {
	conds := &server.Status.Conditions
	if v == nil {
		meta.RemoveStatusCondition(conds, ConditionAddressesServed)
		return
	}
	prev := meta.FindStatusCondition(*conds, ConditionAddressesServed).DeepCopy()
	var parts []string
	for _, u := range v.Users {
		for _, row := range u.Rows {
			if !row.Served {
				parts = append(parts, row.Address+" ("+row.Reason+")")
			}
		}
	}
	if len(parts) == 0 {
		setCondition(conds, server.Generation, ConditionAddressesServed, metav1.ConditionTrue, ReasonAllServed,
			fmt.Sprintf("every address of the %d accounts is served", len(v.Users)))
		return
	}
	if len(parts) > 10 {
		parts = append(parts[:10], fmt.Sprintf("and %d more", len(parts)-10))
	}
	message := "not served: " + strings.Join(parts, ", ")
	setCondition(conds, server.Generation, ConditionAddressesServed, metav1.ConditionFalse, ReasonPartiallyServed, message)
	if r.Recorder != nil && (prev == nil || prev.Status != metav1.ConditionFalse || prev.Message != message) {
		r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, ReasonPartiallyServed, "ServeAddresses", "%s", eventMessage(message))
	}
}

// mapUserServer enqueues the MailServer that a MailUser names. For an update
// the handler maps the old object too, so a user that moves to another
// server reaches the one it left.
func (r *MailServerReconciler) mapUserServer(_ context.Context, obj client.Object) []reconcile.Request {
	u, ok := obj.(*cnmsv1alpha1.MailUser)
	if !ok || u.Spec.ServerRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: u.Namespace, Name: u.Spec.ServerRef.Name}}}
}

// debounced is an event handler that maps like fn and enqueues each request
// after delay. The delaying queue keeps one entry per key with the earliest
// time, so a burst of events is one reconcile, delay after the first.
func debounced(fn handler.MapFunc, delay time.Duration) handler.EventHandler {
	enqueue := func(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		for _, req := range fn(ctx, obj) {
			q.AddAfter(req, delay)
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.ObjectOld, q)
			enqueue(ctx, e.ObjectNew, q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
	}
}
