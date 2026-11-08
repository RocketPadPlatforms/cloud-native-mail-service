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
	"slices"
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
	// The senders of the service accounts join the decision with the next
	// step: the MailServer does not serve them yet.
	in, _ := serverAddressInput(server.Name, users, nil, namespaceDomains(domains, server.Namespace), ev)
	res := addresses.Decide(in)
	return userSource{
		accounts:   render.NewAccounts(res, in.Users, func(name string) string { return hashes[name] }),
		conflicted: res.Conflicted,
		verdict:    &res,
		input:      in,
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
		return &ownershipConflictError{kind: kindSecret, name: desired.Name, owner: ownerMailServer}
	}
	// A write that was computed from a read carries that read's resourceVersion
	// (settleLiveUsers): when the Secret has moved since, the computation is stale
	// and must not overwrite what another writer just applied.
	if desired.ResourceVersion != "" && desired.ResourceVersion != existing.ResourceVersion {
		return errLiveUsersRaced
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

// ReasonUserLimitExceeded, ReasonAddressLimitExceeded and ReasonConfigTooLarge
// are the Ready reasons of a MailServer whose new revision a limit blocks
// (ADR 0015, sections 4.7 and 8). The running revision keeps serving.
const (
	ReasonUserLimitExceeded    = addresses.ReasonUserLimitExceeded
	ReasonAddressLimitExceeded = addresses.ReasonAddressLimitExceeded
	ReasonConfigTooLarge       = addresses.ReasonConfigTooLarge
)

// accountEntries turns the accounts of a render into the form that
// addresses.Settle compares with the live files: who may log in (not disabled,
// not suspended, with a hash), with which hash, and the addresses.
func accountEntries(accounts []render.Account) []addresses.Entry {
	out := make([]addresses.Entry, 0, len(accounts))
	for _, a := range accounts {
		e := addresses.Entry{
			Name: a.Name, Disabled: a.Disabled || a.PasswordHash == "", Suspended: a.Suspended(),
			Credential: a.PasswordHash, Primary: a.Address,
		}
		for _, row := range a.Verdict.Rows[min(1, len(a.Verdict.Rows)):] {
			if row.Served {
				e.Aliases = append(e.Aliases, row.Address)
			}
		}
		out = append(out, e)
	}
	return out
}

// checkRendered applies the byte guard of ADR 0015, section 4.7 to every
// ConfigMap and Secret of a revision, the live users Secret included, before
// anything is sent to the API server: an object that stays under 1,000,000
// bytes (keys and values) is below the 1 MiB limit with a margin. The result
// is nil, or a *addresses.LimitError that names the object (a hash-named
// object, no content).
func checkRendered(plane *manifests.DataPlane, l addresses.Limits) error {
	configMaps := map[string]map[string]string{}
	secrets := map[string]map[string][]byte{}
	for _, obj := range plane.Configs {
		switch o := obj.(type) {
		case *corev1.ConfigMap:
			configMaps[o.Name] = o.Data
		case *corev1.Secret:
			secrets[o.Name] = o.Data
		}
	}
	if plane.LiveUsers != nil {
		secrets[plane.LiveUsers.Name] = plane.LiveUsers.Data
	}
	if err := addresses.CheckObjectsWith(configMaps, l); err != nil {
		return err
	}
	return addresses.CheckObjectsWith(secrets, l)
}

// settleLiveUsers writes the live users Secret of a blocked revision (ADR
// 0015, section 4.7, "what a blocked revision blocks"): the revocation-only
// rule. The previous accepted set is what the Secret holds now, read back
// with dovecot.ParseLive: it is in the cluster, so it survives a restart of
// the operator, and it is exactly what Dovecot reads. The passdb file keeps
// the lines that are already in it, with the current password, and drops those
// of an account that is gone, disabled or suspended; no line is added. The
// userdb file is not touched: the frozen Postfix maps still accept the mail of
// those accounts, and they stay deliverable. It returns the number of changes
// that wait for the blocking input to be fixed. A server without a live
// Secret has nothing running, and nothing to settle.
func (r *MailServerReconciler) settleLiveUsers(ctx context.Context, server *cnmsv1alpha1.MailServer, next []addresses.Entry) (held int, err error) {
	existing := &corev1.Secret{}
	err = r.APIReader.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: manifests.LiveUsersSecretName(server)}, existing)
	if errors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !metav1.IsControlledBy(existing, server) {
		return 0, &ownershipConflictError{kind: kindSecret, name: existing.Name, owner: ownerMailServer}
	}
	userdb := string(existing.Data[dovecot.UserdbKey])
	// The live files do not carry aliases, so they cannot be compared: the
	// changes that wait are the ones of accounts.
	next = slices.Clone(next)
	for i := range next {
		next[i].Aliases = nil
	}
	settled := addresses.Settle(dovecot.ParseLive(string(existing.Data[dovecot.PassdbKey]), userdb), next, true)
	logins := make([]dovecot.Login, 0, len(settled.Logins))
	for _, e := range settled.Logins {
		logins = append(logins, dovecot.Login{Address: e.Primary, Name: e.Name, Hash: e.Credential})
	}
	files := map[string]string{dovecot.PassdbKey: dovecot.RenderLogins(server, logins), dovecot.UserdbKey: userdb}
	desired := manifests.LiveUsersSecret(server, files)
	// The settled files come from this read: the write is refused when the Secret
	// is not at this resourceVersion any more (applyLiveUsers).
	desired.ResourceVersion = existing.ResourceVersion
	return len(settled.Held), r.applyLiveUsers(ctx, server, desired, false)
}

// emitLimitEvent records a Warning Event when the reason or the message of a
// limit block changes while the server was not ready already (the change of
// Ready itself has its Event): the message has counts and an object name,
// never an address or a secret.
func (r *MailServerReconciler) emitLimitEvent(server *cnmsv1alpha1.MailServer, prev *metav1.Condition, reason, message string) {
	if r.Recorder == nil || prev == nil || prev.Status != metav1.ConditionFalse || (prev.Reason == reason && prev.Message == message) {
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, reason, "LimitExceeded", "%s", eventMessage(message))
}

// holdRunning is what a blocked render does to the running data plane: the
// certificate and the live users Secret follow keepRunning. It returns the
// reason and the message of Ready: for a limit block the message says what the
// block does, and a Warning Event is emitted when it changes.
func (r *MailServerReconciler) holdRunning(ctx context.Context, server *cnmsv1alpha1.MailServer, hostname string, blocked *blockedBy, src userSource, exists bool, prevReady *metav1.Condition) (reason, message string, err error) {
	held := 0
	if exists {
		if held, err = r.keepRunning(ctx, server, hostname, blocked, src); err != nil {
			return "", "", err
		}
	}
	if !src.limited {
		return blocked.reason, blocked.message, nil
	}
	message = limitMessage(blocked.message, held, exists)
	r.emitLimitEvent(server, prevReady, blocked.reason, message)
	return blocked.reason, message, nil
}

// holdRevision handles a revision that the byte guard refused after the
// render: the running revision stays, and the live users Secret follows the
// revocation-only rule. It returns the message of Ready.
func (r *MailServerReconciler) holdRevision(ctx context.Context, server *cnmsv1alpha1.MailServer, limit *addresses.LimitError, src userSource, prevReady *metav1.Condition) (string, error) {
	held, err := r.settleLiveUsers(ctx, server, src.entries)
	if err != nil {
		return "", err
	}
	message := limitMessage(limit.Message, held, true)
	r.emitLimitEvent(server, prevReady, limit.Reason, message)
	return message, nil
}

// recordGate records the outcome of the validation gate. A revision that the
// byte guard refused is no validation result: it becomes the limit block of
// the server (holdRevision) and replaces blocked.
func (r *MailServerReconciler) recordGate(ctx context.Context, server *cnmsv1alpha1.MailServer, g *gate, src userSource, blocked *blockedBy, prevReady, prevValidated *metav1.Condition) (*blockedBy, error) {
	if g.limit == nil {
		r.setValidationCondition(server, g, prevValidated)
		return blocked, nil
	}
	message, err := r.holdRevision(ctx, server, g.limit, src, prevReady)
	if err != nil {
		return blocked, err
	}
	return &blockedBy{g.limit.Reason, message}, nil
}
