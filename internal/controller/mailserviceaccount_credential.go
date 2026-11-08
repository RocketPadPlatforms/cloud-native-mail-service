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
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// The credential Secret of a service account (ADR 0008, ADR 0015 section
// 4.9) lives in the namespace of the account, is named <account>-credential
// and is owned by the account. Its keys for the application are the ones of
// the ADR; passwordHash is the operator's: the MailServer takes the hash
// from it, for the login the account object gives (never from the
// username key) and only if it has the form the operator writes, because
// the Secret is in a namespace of the tenant.
const (
	keyServer        = "server"
	keyTLS           = "tls"
	keyHelo          = "helo"
	keyTLSServerName = "tlsServerName"
	keyCACert        = "ca.crt"

	// tlsModeMandatory is the value of the tls key: the submission
	// listener takes AUTH only over TLS.
	tlsModeMandatory = "mandatory"

	// labelServiceAccount carries the name of the account on its Secret
	// and its NetworkPolicy, and is the label that selects the pods of
	// the application the policy applies to. The name of an account is
	// at most 63 characters, so it is a valid label value.
	labelServiceAccount = "cnms.rpp.gmbh/mailserviceaccount"
	// labelServerNamespace carries the namespace of the MailServer, next
	// to manifests.ServerLabel: server names are unique per namespace.
	labelServerNamespace = "cnms.rpp.gmbh/mailserver-namespace"

	egressPolicySuffix = "-submission"
)

// accountCredentialName is the name of the credential Secret.
func accountCredentialName(account string) string { return credentialSecretName(account) }

// accountEgressName is the name of the NetworkPolicy.
func accountEgressName(account string) string { return account + egressPolicySuffix }

// accountLabels are the labels of the objects the controller makes in the
// namespace of the account. The managed-by labels are the filter of the
// manager cache and the selector of the MailServer's list.
func accountLabels(acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer) map[string]string {
	return map[string]string{
		labelAppManagedBy:        manifests.ManagedByValue,
		manifests.ManagedByLabel: manifests.ManagedByValue,
		manifests.ServerLabel:    server.Name,
		labelServerNamespace:     server.Namespace,
		labelServiceAccount:      acc.Name,
	}
}

// serverEndpoint is what the application is told about the server: the
// in-cluster name of the Service that exposes the submission port, and the
// name the server presents (HELO and certificate): its authorized hostname,
// or the name of the Service while the hostname is not authorized (the same
// rule as render.RenderRequest.ServerHostname).
type serverEndpoint struct {
	service, hostname string
}

func endpointOf(server *cnmsv1alpha1.MailServer, authorizedHostname bool) serverEndpoint {
	e := serverEndpoint{service: render.ServiceFQDN(server.Name, server.Namespace, render.ComponentPostfix)}
	e.hostname = e.service
	if authorizedHostname {
		e.hostname = server.Spec.Hostname
	}
	return e
}

// maxVerifiedSecrets bounds verifiedSecrets: far more than the credential
// Secrets of one operator. When it is full the map starts again, which only
// costs a verification per Secret.
const maxVerifiedSecrets = 4096

// verifiedSecrets remembers, per credential Secret (by UID), the
// resourceVersion at which its hash was last verified against its password. It
// lives in memory: a restart verifies each Secret once more. The zero value is
// ready to use.
type verifiedSecrets struct {
	mu sync.Mutex
	rv map[types.UID]string
}

func (v *verifiedSecrets) seen(uid types.UID, rv string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	got, ok := v.rv[uid]
	return ok && rv != "" && got == rv
}

func (v *verifiedSecrets) mark(uid types.UID, rv string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.rv == nil || len(v.rv) >= maxVerifiedSecrets {
		v.rv = map[types.UID]string{}
	}
	v.rv[uid] = rv
}

func (v *verifiedSecrets) forget(uid types.UID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.rv, uid)
}

// accountCredential is the result of the credential step.
type accountCredential struct {
	status          metav1.ConditionStatus
	reason, message string
	// secretName and revision are the status fields; empty when there is
	// no credential Secret.
	secretName, revision string
	retry                bool
}

// reconcileCredential makes the credential Secret: created once, written
// only with the resourceVersion that was read, the generated password never
// replaced while the hash verifies it. A Secret of that name that the account
// does not control is not touched.
func (r *MailServiceAccountReconciler) reconcileCredential(ctx context.Context, acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer, ep serverEndpoint) (accountCredential, error) {
	name := accountCredentialName(acc.Name)
	existing := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: acc.Namespace, Name: name}, existing)
	if apierrors.IsNotFound(err) {
		existing = nil
	} else if err != nil {
		return accountCredential{}, err
	}
	if existing != nil && !metav1.IsControlledBy(existing, acc) {
		return accountCredential{status: metav1.ConditionFalse, reason: ReasonCredentialSecretConflict,
			message: fmt.Sprintf("the Secret %q exists and is not owned by this MailServiceAccount; it is not taken over", name)}, nil
	}
	keep := func(reason, message string) accountCredential {
		o := accountCredential{status: metav1.ConditionFalse, reason: reason, message: message}
		if existing != nil {
			o.secretName, o.revision = name, keptRevision(acc, existing)
		}
		return o
	}

	desired, hashChanged, err := r.decideAccountCredential(ctx, existing)
	if err != nil {
		if ctx.Err() != nil {
			return accountCredential{}, err
		}
		return keep(ReasonHashFailed, "the password could not be hashed"), nil
	}
	ca, err := r.serverCA(ctx, server)
	if err != nil {
		return accountCredential{}, err
	}
	desired.Data[keyUsername] = []byte(serviceAccountLogin(acc.Namespace, acc.Name))
	desired.Data[keyServer] = []byte(ep.service)
	desired.Data[keyTLS] = []byte(tlsModeMandatory)
	desired.Data[keyHelo] = []byte(ep.hostname)
	desired.Data[keyTLSServerName] = []byte(ep.hostname)
	if len(ca) > 0 {
		desired.Data[keyCACert] = ca
	} else {
		delete(desired.Data, keyCACert)
	}
	if err := r.shapeCredential(desired, acc, server, name); err != nil {
		return accountCredential{}, err
	}
	final, retry, err := writeCredential(ctx, r.Client, existing, desired)
	if err != nil || retry {
		return accountCredential{retry: retry}, err
	}
	// The password and the hash of what was just written belong together:
	// they were verified, or made.
	r.verified.mark(final.UID, final.ResourceVersion)
	revision := keptRevision(acc, final)
	if hashChanged {
		revision = final.ResourceVersion
	}
	message := "the credential Secret is in place"
	if len(ca) == 0 {
		message += "; the CA chain of the server is not available, so ca.crt is omitted"
	}
	return accountCredential{status: metav1.ConditionTrue, reason: ReasonReady, message: message,
		secretName: name, revision: revision}, nil
}

// serverCA reads the CA chain of the certificate of the server: the key
// ca.crt of its TLS Secret, which cert-manager writes for the issuers that
// have a CA. A Secret or key that is missing is no error: cert-manager is an
// optional integration, and the key is left out of the credential (ADR
// 0008). Only that key is read, never the private key.
func (r *MailServiceAccountReconciler) serverCA(ctx context.Context, server *cnmsv1alpha1.MailServer) ([]byte, error) {
	s := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: manifests.TLSSecretName(server)}, s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Data[keyCACert], nil
}

// decideAccountCredential returns the Secret to write and whether its hash
// is new. A password and a hash that exist are kept when the hash verifies
// the password (with the pinned parameters): the Secret is in a namespace of
// the tenant, and a hash that does not belong to the password is replaced
// together with the password. A consistent pair is kept, whoever wrote it, so
// a tenant can choose the password of its own login and of no other: the
// login comes from the account, and the username key is written again. The
// verification costs one Argon2id run on the hash slot of the operator, so it
// is skipped for a Secret whose resourceVersion was verified (verifiedSecrets):
// any write to the Secret changes it. The data holds the password and the
// hash, without the other keys.
func (r *MailServiceAccountReconciler) decideAccountCredential(ctx context.Context, existing *corev1.Secret) (*corev1.Secret, bool, error) {
	desired := &corev1.Secret{}
	if existing != nil {
		desired = existing.DeepCopy()
		pw, hash := existing.Data[keyPassword], existing.Data[keyPasswordHash]
		if len(pw) > 0 && len(hash) > 0 {
			ok := r.verified.seen(existing.UID, existing.ResourceVersion)
			if !ok {
				var err error
				if r.onVerify != nil {
					r.onVerify()
				}
				if ok, err = verifyHash(ctx, string(pw), string(hash)); err != nil {
					return nil, false, err
				}
			}
			if ok {
				if desired.Data == nil {
					desired.Data = map[string][]byte{}
				}
				return desired, false, nil
			}
		}
	}
	pw, err := generatePassword()
	if err != nil {
		return nil, false, err
	}
	hash, err := hashPassword(ctx, pw)
	if err != nil {
		return nil, false, err
	}
	desired.Data = map[string][]byte{keyPassword: []byte(pw), keyPasswordHash: []byte(hash)}
	return desired, true, nil
}

// keptRevision is the credentialRevision to report when the hash did not
// change: the one in the status, or the resourceVersion of the Secret when
// the status has none (a first reconcile, or a status lost in a restore).
func keptRevision(acc *cnmsv1alpha1.MailServiceAccount, s *corev1.Secret) string {
	if acc.Status.CredentialSecretName == s.Name && acc.Status.CredentialRevision != "" {
		return acc.Status.CredentialRevision
	}
	return s.ResourceVersion
}

// shapeCredential sets the metadata of the credential Secret.
func (r *MailServiceAccountReconciler) shapeCredential(s *corev1.Secret, acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer, name string) error {
	s.Name, s.Namespace, s.Type = name, acc.Namespace, corev1.SecretTypeOpaque
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	maps.Copy(s.Labels, accountLabels(acc, server))
	return controllerutil.SetControllerReference(acc, s, r.Scheme)
}

// egressOutcome is the result of the NetworkPolicy step.
type egressOutcome struct {
	status          metav1.ConditionStatus
	reason, message string
	retry           bool
}

// egressPolicy is the NetworkPolicy of an account (ADR 0008): the pods of the
// application that carry the account's label may reach the Postfix pods of
// the server on the submission port, and the cluster DNS. It selects those
// pods only, because a policy isolates every pod it selects: a policy over
// the whole namespace would cut off the rest of what the application talks
// to. The pods that carry the label are limited to this (and to what other
// policies of the namespace allow).
func egressPolicy(acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer) *networkingv1.NetworkPolicy {
	port := func(proto corev1.Protocol, p int32) networkingv1.NetworkPolicyPort {
		return networkingv1.NetworkPolicyPort{Protocol: new(proto), Port: new(intstr.FromInt32(p))}
	}
	anywhere := func(cidr string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}
	}
	kubeDNS := networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
	}
	postfix := networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": server.Namespace}},
		PodSelector:       manifests.PostfixPodSelector(server),
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      accountEgressName(acc.Name),
			Namespace: acc.Namespace,
			Labels:    accountLabels(acc, server),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{labelServiceAccount: acc.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To:    []networkingv1.NetworkPolicyPeer{kubeDNS, anywhere("0.0.0.0/0"), anywhere("::/0")},
					Ports: []networkingv1.NetworkPolicyPort{port(corev1.ProtocolTCP, 53), port(corev1.ProtocolUDP, 53)},
				},
				{
					To:    []networkingv1.NetworkPolicyPeer{postfix},
					Ports: []networkingv1.NetworkPolicyPort{port(corev1.ProtocolTCP, render.PortPostfixSubmission)},
				},
			},
		},
	}
}

// reconcileEgress makes the NetworkPolicy. The read goes to the API server:
// the cache holds only the objects that carry the managed-by label, and a
// policy of a tenant under the same name must be noticed, not taken over.
func (r *MailServiceAccountReconciler) reconcileEgress(ctx context.Context, acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer) (egressOutcome, error) {
	desired := egressPolicy(acc, server)
	if err := controllerutil.SetControllerReference(acc, desired, r.Scheme); err != nil {
		return egressOutcome{}, err
	}
	ready := egressOutcome{status: metav1.ConditionTrue, reason: ReasonReady, message: "the egress policy of the account is in place"}
	existing := &networkingv1.NetworkPolicy{}
	err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return egressOutcome{retry: apierrors.IsAlreadyExists(err)}, ignoreRace(err, apierrors.IsAlreadyExists)
		}
		return ready, nil
	case err != nil:
		return egressOutcome{}, err
	case !metav1.IsControlledBy(existing, acc):
		return egressOutcome{status: metav1.ConditionFalse, reason: ReasonOwnershipConflict,
			message: fmt.Sprintf("the NetworkPolicy %q exists and is not owned by this MailServiceAccount; it is not taken over", desired.Name)}, nil
	}
	if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) && equality.Semantic.DeepEqual(existing.Labels, desired.Labels) &&
		equality.Semantic.DeepEqual(existing.OwnerReferences, desired.OwnerReferences) {
		return ready, nil
	}
	existing.Spec, existing.Labels, existing.OwnerReferences = desired.Spec, desired.Labels, desired.OwnerReferences
	if err := r.Update(ctx, existing); err != nil {
		return egressOutcome{retry: apierrors.IsConflict(err)}, ignoreRace(err, apierrors.IsConflict)
	}
	return ready, nil
}

// revoke removes what an account that is no longer allowed would have: the
// credential Secret and the NetworkPolicy, each only when the account
// controls it. A missing object is fine.
func (r *MailServiceAccountReconciler) revoke(ctx context.Context, acc *cnmsv1alpha1.MailServiceAccount) error {
	for _, obj := range []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: accountCredentialName(acc.Name), Namespace: acc.Namespace}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: accountEgressName(acc.Name), Namespace: acc.Namespace}},
	} {
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if !metav1.IsControlledBy(obj, acc) {
			continue
		}
		if err := r.Delete(ctx, obj, client.Preconditions{UID: new(obj.GetUID())}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		r.verified.forget(obj.GetUID())
	}
	return nil
}
