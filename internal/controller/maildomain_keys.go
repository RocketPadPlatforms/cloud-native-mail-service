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
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// The key Secrets of generated keys (ADR 0011, section 3).
const (
	// keySecretDataKey is the data key of a key Secret: a PKCS#8 PEM.
	keySecretDataKey = "private.key"
	// annotationKeyCreated is the RFC 3339 creation time of a key; the
	// rotation age is derived from it.
	annotationKeyCreated = "cnms.rpp.gmbh/created"
	// annotationKeyAlgorithm is "rsa" or "ed25519".
	annotationKeyAlgorithm = "cnms.rpp.gmbh/algorithm"
	// labelMailDomain carries the MailDomain name on its key Secrets,
	// which have no owner reference (ADR 0011).
	labelMailDomain = "cnms.rpp.gmbh/maildomain"
	// labelAppManagedBy is the standard managed-by label.
	labelAppManagedBy = "app.kubernetes.io/managed-by"
)

// byoKeyRecheck is how often the Secrets of your own keys are read:
// Secrets are not cached and not watched, so a changed or newly created
// one is only noticed by this requeue (or a change of the MailDomain).
// A missing or unusable one is retried every ownKeyRetry.
const (
	byoKeyRecheck = 5 * time.Minute
	ownKeyRetry   = time.Minute
)

// keyEntry is a published DKIM key.
type keyEntry struct {
	selector string
	key      *dkim.Key
	// created is the creation time of a generated key; zero for your
	// own keys.
	created time.Time
	// until is when a retiring key leaves DNS.
	until time.Time
}

// keySet is the outcome of the key handling of one reconcile.
type keySet struct {
	// active keys sign; pending keys are published and do not sign
	// yet; retiring keys are published and no longer sign. Only keys
	// that could be read are listed.
	active, pending, retiring []keyEntry
	// problems describes the keys that are missing or unusable, one
	// "<selector>: <reason>" each, and reason is the condition reason
	// of the first one.
	problems      []string
	problemReason string
	// ownKeys is true for bring-your-own keys.
	ownKeys bool
	// nextRotation is when the next key is created; zero if unknown.
	nextRotation time.Time
	// requeue is the delay after which the keys have to be looked at
	// again; zero means no particular time.
	requeue time.Duration
}

// generatedKey is a key Secret of a MailDomain.
type generatedKey struct {
	info dkim.KeyInfo
	key  *dkim.Key
	// err is why the Secret is unusable; key is nil then.
	err error
	// created is true when this reconcile created the Secret.
	created bool
	// conflict is true when the Secret belongs to another MailDomain
	// and was not adopted (err says which).
	conflict bool
}

// rotationSpec maps the spec to the input of the rotation.
func rotationSpec(md *cnmsv1alpha1.MailDomain) dkim.RotationSpec {
	var spec dkim.RotationSpec
	d := md.Spec.DKIM
	if d == nil {
		return spec
	}
	if d.SelectorPrefix != nil {
		spec.Prefix = *d.SelectorPrefix
	}
	spec.Ed25519 = d.Ed25519 != nil && *d.Ed25519
	if d.Rotation != nil {
		spec.Interval = d.Rotation.Interval.Duration
		if d.Rotation.Overlap != nil {
			spec.Overlap = d.Rotation.Overlap.Duration
		}
	}
	return spec
}

// keySecretLabels are the labels of the key Secrets of md.
func keySecretLabels(md *cnmsv1alpha1.MailDomain) map[string]string {
	return map[string]string{
		labelAppManagedBy:        manifests.ManagedByValue,
		manifests.ManagedByLabel: manifests.ManagedByValue,
		labelMailDomain:          md.Name,
	}
}

// loadGeneratedKeys lists the key Secrets of md, read from the API
// server and not from the cache (Secrets are never cached). A Secret
// that carries the labels but not the name of a key is ignored.
func (r *MailDomainReconciler) loadGeneratedKeys(ctx context.Context, md *cnmsv1alpha1.MailDomain) ([]generatedKey, error) {
	list := &corev1.SecretList{}
	if err := r.APIReader.List(ctx, list, client.InNamespace(md.Namespace), client.MatchingLabels(keySecretLabels(md))); err != nil {
		return nil, err
	}
	var keys []generatedKey
	for i := range list.Items {
		if sel, ok := strings.CutPrefix(list.Items[i].Name, dkim.SecretName(md.Name, "")); ok && sel != "" {
			keys = append(keys, parseKeySecret(&list.Items[i], sel))
		}
	}
	return keys, nil
}

// parseKeySecret reads a key Secret. The algorithm comes from the key
// itself; the annotation only stands in for a key that does not parse.
func parseKeySecret(s *corev1.Secret, selector string) generatedKey {
	g := generatedKey{info: dkim.KeyInfo{
		Selector:  selector,
		Algorithm: dkim.Algorithm(s.Annotations[annotationKeyAlgorithm]),
		CreatedAt: s.CreationTimestamp.Time,
	}}
	if t, err := time.Parse(time.RFC3339, s.Annotations[annotationKeyCreated]); err == nil {
		g.info.CreatedAt = t
	}
	if g.info.Algorithm != dkim.Ed25519 {
		g.info.Algorithm = dkim.RSA
	}
	key, err := dkim.Parse(s.Data[keySecretDataKey])
	if err != nil {
		g.err = fmt.Errorf("key Secret %q: %w", s.Name, err)
		return g
	}
	g.key, g.info.Algorithm = key, key.Algorithm
	return g
}

// reconcileGeneratedKeys runs the rotation (ADR 0011, section 6) for
// the keys the operator generates. The order is crash safe: new key
// Secrets are created first, then expired ones deleted, and the state
// annotation is written last, so a crash in between leaves a key that
// is implicitly pending or a state entry for a Secret that is gone,
// both of which the next pass resolves. A key whose Secret exists is
// never generated again.
func (r *MailDomainReconciler) reconcileGeneratedKeys(ctx context.Context, md *cnmsv1alpha1.MailDomain, existing []generatedKey, dns map[string]dkim.DNSState) (*keySet, error) {
	now := r.now()
	state, haveState, err := dkim.Decode(md.Annotations[dkim.AnnotationRotation])
	if err != nil {
		log.FromContext(ctx).Info("Rotation annotation is unusable, deriving the state from the key Secrets", "reason", err.Error())
		r.event(md, corev1.EventTypeWarning, "RotationStateIgnored", "ReadRotationState", err.Error())
	}
	infos := make([]dkim.KeyInfo, 0, len(existing))
	bySelector := make(map[string]generatedKey, len(existing))
	for _, g := range existing {
		infos = append(infos, g.info)
		bySelector[g.info.Selector] = g
	}
	plan := dkim.Rotate(dkim.Input{
		Keys: infos, State: state, HaveState: haveState,
		Spec: rotationSpec(md), DNS: dns, Now: now,
	})

	created := map[string]bool{}
	var conflicts []generatedKey
	for _, nk := range plan.Create {
		g, err := r.ensureKeySecret(ctx, md, nk, now)
		if err != nil {
			return nil, err
		}
		if g.conflict {
			// The Secret of that name is another MailDomain's: the state
			// must not name it, or the data plane would sign with the
			// other domain's key (the name is derived from the selector).
			r.event(md, corev1.EventTypeWarning, ReasonKeySecretConflict, "CreateKey", g.err.Error())
			conflicts = append(conflicts, g)
			isSel := func(s string) bool { return s == nk.Selector }
			plan.State.Active = slices.DeleteFunc(plan.State.Active, isSel)
			plan.Pending = slices.DeleteFunc(plan.Pending, isSel)
			continue
		}
		created[nk.Selector] = true
		bySelector[nk.Selector] = g
		if g.created { // an adopted Secret (a restore) is not a new key
			r.event(md, corev1.EventTypeNormal, "KeyCreated", "CreateKey",
				fmt.Sprintf("created the %s DKIM key %s", nk.Algorithm, nk.Selector))
		}
	}
	for _, sel := range plan.Delete {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: dkim.SecretName(md.Name, sel), Namespace: md.Namespace}}
		if err := r.Delete(ctx, secret); err != nil && !errors.IsNotFound(err) {
			return nil, err
		}
		delete(bySelector, sel)
		r.event(md, corev1.EventTypeNormal, "KeyRetired", "RetireKey",
			fmt.Sprintf("deleted the retired DKIM key %s", sel))
	}
	if err := r.writeRotationState(ctx, md, plan.State); err != nil {
		return nil, err
	}
	if haveState {
		for _, sel := range plan.State.Active {
			if !slices.Contains(state.Active, sel) && !created[sel] {
				r.event(md, corev1.EventTypeNormal, "KeyActivated", "ActivateKey",
					fmt.Sprintf("the DKIM key %s signs now", sel))
			}
		}
	}
	for sel := range created {
		if !slices.Contains(plan.State.Active, sel) {
			r.event(md, corev1.EventTypeNormal, "RotationStarted", "RotateKey",
				fmt.Sprintf("the DKIM key %s is published and signs once its record is verified", sel))
		}
	}

	set := &keySet{nextRotation: plan.NextRotation}
	if !plan.NextWake.IsZero() {
		set.requeue = max(plan.NextWake.Sub(now), time.Second)
	}
	for _, g := range conflicts {
		set.addProblem(g.info.Selector, ReasonKeySecretConflict, g.err.Error())
	}
	entry := func(sel string) (keyEntry, bool) {
		g, ok := bySelector[sel]
		if !ok {
			return keyEntry{}, false
		}
		if g.err != nil {
			set.addProblem(sel, ReasonInvalidKey, g.err.Error())
			return keyEntry{}, false
		}
		return keyEntry{selector: sel, key: g.key, created: g.info.CreatedAt}, true
	}
	for _, sel := range plan.State.Active {
		if e, ok := entry(sel); ok {
			set.active = append(set.active, e)
		}
	}
	for _, sel := range plan.Pending {
		if e, ok := entry(sel); ok {
			set.pending = append(set.pending, e)
		}
	}
	for _, rk := range plan.State.Retiring {
		if e, ok := entry(rk.Selector); ok {
			e.until = rk.Until
			set.retiring = append(set.retiring, e)
		}
	}
	return set, nil
}

// addProblem records an unusable key.
func (s *keySet) addProblem(selector, reason, message string) {
	if s.problemReason == "" {
		s.problemReason = reason
	}
	s.problems = append(s.problems, selector+": "+message)
}

// ensureKeySecret returns the key Secret of nk, generating it only when
// no Secret of that name exists: an existing one (a restore, or a
// concurrent reconcile) is adopted, never replaced. A Secret labelled
// for another MailDomain is not adopted: the names
// "<name>-dkim-<selector>" can collide inside a namespace. A Secret
// without the label is adopted (a restore may bring it back unlabelled,
// ADR 0011). Secrets of keys are immutable and have no owner reference
// (ADR 0011).
func (r *MailDomainReconciler) ensureKeySecret(ctx context.Context, md *cnmsv1alpha1.MailDomain, nk dkim.NewKey, now time.Time) (generatedKey, error) {
	name := types.NamespacedName{Name: dkim.SecretName(md.Name, nk.Selector), Namespace: md.Namespace}
	adopt := func(s *corev1.Secret) generatedKey {
		if owner := s.Labels[labelMailDomain]; owner != "" && owner != md.Name {
			return generatedKey{
				info:     dkim.KeyInfo{Selector: nk.Selector, Algorithm: nk.Algorithm},
				err:      fmt.Errorf("key Secret %q belongs to MailDomain %q", s.Name, owner),
				conflict: true,
			}
		}
		return parseKeySecret(s, nk.Selector)
	}
	existing := &corev1.Secret{}
	err := r.APIReader.Get(ctx, name, existing)
	if err == nil {
		return adopt(existing), nil
	}
	if !errors.IsNotFound(err) {
		return generatedKey{}, err
	}
	pemKey, err := dkim.Generate(nk.Algorithm, rand.Reader)
	if err != nil {
		return generatedKey{}, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name.Name, Namespace: name.Namespace,
			Labels: keySecretLabels(md),
			Annotations: map[string]string{
				annotationKeyCreated:   now.Format(time.RFC3339),
				annotationKeyAlgorithm: string(nk.Algorithm),
			},
		},
		Immutable: new(true),
		Type:      corev1.SecretTypeOpaque,
		Data:      map[string][]byte{keySecretDataKey: pemKey},
	}
	if err := r.Create(ctx, secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return generatedKey{}, err
		}
		if err := r.APIReader.Get(ctx, name, existing); err != nil {
			return generatedKey{}, err
		}
		return adopt(existing), nil
	}
	g := parseKeySecret(secret, nk.Selector)
	g.created = true
	return g, nil
}

// writeRotationState stores the rotation state in the annotation of the
// MailDomain (a metadata patch: the object is also written by GitOps,
// so only this annotation is touched). The encoding is deterministic, so
// an unchanged state writes nothing.
func (r *MailDomainReconciler) writeRotationState(ctx context.Context, md *cnmsv1alpha1.MailDomain, state dkim.State) error {
	value := dkim.Encode(state)
	if md.Annotations[dkim.AnnotationRotation] == value {
		return nil
	}
	patched := md.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = map[string]string{}
	}
	patched.Annotations[dkim.AnnotationRotation] = value
	if err := r.Patch(ctx, patched, client.MergeFrom(md)); err != nil {
		return err
	}
	if md.Annotations == nil {
		md.Annotations = map[string]string{}
	}
	md.Annotations[dkim.AnnotationRotation] = value
	return nil
}

// reconcileOwnKeys reads the keys of spec.dkim.keys (bring your own
// keys): the spec is the state, there is no rotation. A key that is
// missing or unusable is reported and left out, the others keep
// working. Keys with signing false are published but listed as pending.
func (r *MailDomainReconciler) reconcileOwnKeys(ctx context.Context, md *cnmsv1alpha1.MailDomain) (*keySet, error) {
	set := &keySet{ownKeys: true, requeue: byoKeyRecheck}
	secrets := map[string]*corev1.Secret{}
	for _, k := range md.Spec.DKIM.Keys {
		ref := k.SecretRef
		s, seen := secrets[ref.Name]
		if !seen {
			s = &corev1.Secret{}
			err := r.APIReader.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: md.Namespace}, s)
			switch {
			case errors.IsNotFound(err):
				s = nil
			case err != nil:
				return nil, err
			}
			secrets[ref.Name] = s
		}
		if s == nil {
			set.addProblem(k.Selector, ReasonKeySecretNotFound, fmt.Sprintf("Secret %q not found", ref.Name))
			continue
		}
		data, ok := s.Data[ref.Key]
		if !ok {
			set.addProblem(k.Selector, ReasonKeySecretNotFound, fmt.Sprintf("Secret %q has no key %q", ref.Name, ref.Key))
			continue
		}
		key, err := dkim.Parse(data)
		if err != nil {
			set.addProblem(k.Selector, ReasonInvalidKey, fmt.Sprintf("%s in Secret %q: %v", ref.Key, ref.Name, err))
			continue
		}
		e := keyEntry{selector: k.Selector, key: key}
		if k.Signing == nil || *k.Signing {
			set.active = append(set.active, e)
		} else {
			set.pending = append(set.pending, e)
		}
	}
	if len(set.problems) > 0 {
		set.requeue = ownKeyRetry
	}
	return set, nil
}

// event emits an Event about a key; messages embed selectors and
// errors, so they are truncated.
func (r *MailDomainReconciler) event(md *cnmsv1alpha1.MailDomain, eventType, reason, action, message string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(md, nil, eventType, reason, action, "%s", eventMessage(message))
	}
}

// all returns every published key: active, pending and retiring.
func (s *keySet) all() []keyEntry {
	return slices.Concat(s.active, s.pending, s.retiring)
}
