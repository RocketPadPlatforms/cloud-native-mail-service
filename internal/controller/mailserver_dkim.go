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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// ConditionDKIMKeysAvailable is false while a served domain has no
// usable signing key (ADR 0011, 0013). The domain is still served, but
// its outbound mail is deferred until a key exists. It is a warning
// only: it never feeds into Ready, and the rollout does not wait for it.
const ConditionDKIMKeysAvailable = "DKIMKeysAvailable"

// DKIMKeysAvailable condition reasons.
const (
	// ReasonDKIMKeysAvailable means every served domain has all its
	// active keys.
	ReasonDKIMKeysAvailable = "KeysAvailable"
	// ReasonDKIMKeysMissing means a served domain has no active key, or
	// one of its keys cannot be read.
	ReasonDKIMKeysMissing = "KeysMissing"
)

// dkimKeyRetry is how soon a missing key is looked for again: key
// Secrets are neither cached nor watched.
const dkimKeyRetry = time.Minute

// dkimKeys is the signing key input of one render.
type dkimKeys struct {
	keys []render.DomainKeys
	// problems say, per domain, which key is missing or unusable.
	problems []string
}

// keyRef points at the private key of an active selector.
type keyRef struct{ selector, secret, key string }

// activeKeyRefs returns the keys of a MailDomain that sign (ADR 0011).
// Bring-your-own keys are in the spec; generated keys are the active
// selectors of the rotation annotation, in the key Secrets named after
// them. Both exist whether or not the Secret can be read right now: the
// selector stays required so the domain fails closed. The MailDomain
// status is not used, because it lists only keys that could be read.
func activeKeyRefs(md *cnmsv1alpha1.MailDomain) []keyRef {
	if d := md.Spec.DKIM; d != nil && len(d.Keys) > 0 {
		var refs []keyRef
		for _, k := range d.Keys {
			if k.Signing == nil || *k.Signing {
				refs = append(refs, keyRef{k.Selector, k.SecretRef.Name, k.SecretRef.Key})
			}
		}
		return refs
	}
	state, _, _ := dkim.Decode(md.Annotations[dkim.AnnotationRotation]) // unusable: no state, no keys
	refs := make([]keyRef, len(state.Active))
	for i, sel := range state.Active {
		refs[i] = keyRef{sel, dkim.SecretName(md.Name, sel), keySecretDataKey}
	}
	return refs
}

// collectDKIMKeys gathers the active signing keys of the authorized
// domains of server and checks that each key can be read, with an
// uncached read of its Secret. A key that is missing or does not parse
// stays in the result with Available false. No key bytes are kept.
func (r *MailServerReconciler) collectDKIMKeys(ctx context.Context, server *cnmsv1alpha1.MailServer, claims []cnmsv1alpha1.MailDomain, authorized []string) (*dkimKeys, error) {
	byDomain := map[string]*cnmsv1alpha1.MailDomain{}
	for _, ref := range server.Spec.Domains {
		for i := range claims {
			if claims[i].Name == ref.Name && claims[i].Namespace == server.Namespace {
				byDomain[claims[i].Spec.Domain] = &claims[i]
			}
		}
	}
	res := &dkimKeys{}
	secrets := map[string]*corev1.Secret{} // nil: not found
	for _, domain := range authorized {
		md := byDomain[domain]
		var refs []keyRef
		if md != nil {
			refs = activeKeyRefs(md)
		}
		if len(refs) == 0 {
			res.problems = append(res.problems, domain+": no active signing key yet")
			continue
		}
		dk := render.DomainKeys{Domain: domain}
		for _, ref := range refs {
			key := render.DKIMKey{Selector: ref.selector, SecretName: ref.secret, SecretKey: ref.key}
			secret, seen := secrets[ref.secret]
			if !seen {
				secret = &corev1.Secret{}
				err := r.APIReader.Get(ctx, types.NamespacedName{Name: ref.secret, Namespace: server.Namespace}, secret)
				switch {
				case errors.IsNotFound(err):
					secret = nil
				case err != nil:
					return nil, err
				}
				secrets[ref.secret] = secret
			}
			if reason := unusableKey(secret, ref, md.Name); reason != "" {
				res.problems = append(res.problems, fmt.Sprintf("%s: key %s: %s", domain, ref.selector, reason))
			} else {
				key.Available = true
			}
			dk.Keys = append(dk.Keys, key)
		}
		res.keys = append(res.keys, dk)
	}
	return res, nil
}

// unusableKey says why the key in secret (nil: not found) cannot sign
// for the MailDomain mailDomain, or returns "". A Secret labelled for
// another MailDomain is refused (the names of generated keys can
// collide); an unlabelled one is accepted, as after a restore. The
// message holds no key bytes.
func unusableKey(secret *corev1.Secret, ref keyRef, mailDomain string) string {
	if secret == nil {
		return fmt.Sprintf("Secret %q not found", ref.secret)
	}
	if owner := secret.Labels[labelMailDomain]; owner != "" && owner != mailDomain {
		return fmt.Sprintf("Secret %q belongs to MailDomain %q", ref.secret, owner)
	}
	data, ok := secret.Data[ref.key]
	if !ok {
		return fmt.Sprintf("Secret %q has no key %q", ref.secret, ref.key)
	}
	if _, err := dkim.Parse(data); err != nil {
		return fmt.Sprintf("%s in Secret %q is not usable: %v", ref.key, ref.secret, err)
	}
	return ""
}

// dkimCondition returns the DKIMKeysAvailable state for the keys of the
// domains that are rendered.
func dkimCondition(rendered []string, k *dkimKeys) (metav1.ConditionStatus, string, string) {
	switch {
	case len(rendered) == 0:
		return metav1.ConditionTrue, ReasonDKIMKeysAvailable, "no domain is served, so no signing key is needed"
	case len(k.problems) == 0:
		return metav1.ConditionTrue, ReasonDKIMKeysAvailable, "every served domain has its active signing keys"
	}
	slices.Sort(k.problems)
	return metav1.ConditionFalse, ReasonDKIMKeysMissing, fmt.Sprintf(
		"outbound mail of the domains below is deferred until they have a usable signing key; inbound mail is not affected: %s",
		strings.Join(k.problems, "; "))
}

// reconcileDKIMKeys collects the keys of the rendered domains and
// reports them in DKIMKeysAvailable, with a Warning Event when a key
// became missing.
func (r *MailServerReconciler) reconcileDKIMKeys(ctx context.Context, server *cnmsv1alpha1.MailServer, claims []cnmsv1alpha1.MailDomain, rendered []string) (*dkimKeys, error) {
	keys, err := r.collectDKIMKeys(ctx, server, claims, rendered)
	if err != nil {
		return nil, err
	}
	conds := &server.Status.Conditions
	prev := meta.FindStatusCondition(*conds, ConditionDKIMKeysAvailable).DeepCopy()
	status, reason, message := dkimCondition(rendered, keys)
	setCondition(conds, server.Generation, ConditionDKIMKeysAvailable, status, reason, message)
	r.emitDKIMEvent(server, prev, status, message)
	return keys, nil
}

// requeue shortens the delay of res while a key is missing: key Secrets
// are not watched, so the controller looks for the key again.
func (k *dkimKeys) requeue(res ctrl.Result) ctrl.Result {
	if len(k.problems) > 0 {
		res.RequeueAfter = min(res.RequeueAfter, dkimKeyRetry)
	}
	return res
}

// emitDKIMEvent records a Warning Event when keys became missing or the
// description of what is missing changed.
func (r *MailServerReconciler) emitDKIMEvent(server *cnmsv1alpha1.MailServer, prev *metav1.Condition, status metav1.ConditionStatus, message string) {
	if r.Recorder == nil || status != metav1.ConditionFalse {
		return
	}
	if prev != nil && prev.Status == metav1.ConditionFalse && prev.Message == message {
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, ReasonDKIMKeysMissing, "CheckDKIMKeys", "%s", eventMessage(message))
}
