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
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// MailUser condition types and reasons (ADR 0015, section 8). The Ready
// reasons that are the ones of other resources (ServerNotFound,
// UserSourceConflict, SecretNotFound) are reused.
const (
	// ConditionCredentialReady says whether the password hash is in the
	// credential Secret.
	ConditionCredentialReady = "CredentialReady"
	// ConditionAddressesServed is a warning that never feeds Ready: it is
	// False while an address of the account is not served.
	ConditionAddressesServed = "AddressesServed"

	// ReasonDirectoryConfigured is the reason of a MailUser whose server
	// uses a MailDirectory: a server has one user source.
	ReasonDirectoryConfigured = "DirectoryConfigured"
	// The reasons of a primary domain that fails (the account is
	// suspended, ADR 0015, section 4.6).
	ReasonDomainNotFound      = addresses.ReasonDomainNotFound
	ReasonDomainNotServed     = addresses.ReasonDomainNotServed
	ReasonDomainNotAuthorized = addresses.ReasonDomainNotAuthorized
	// ReasonDisabled is the reason of a disabled account; Ready is True.
	ReasonDisabled = "Disabled"
	// ReasonPasswordInvalid is set when a provided password is too short.
	ReasonPasswordInvalid = "PasswordInvalid"
	// ReasonHashFailed is set when the hash could not be computed.
	ReasonHashFailed = "HashFailed"
	// ReasonAllServed and ReasonPartiallyServed are the reasons of the
	// AddressesServed condition.
	ReasonAllServed       = "AllServed"
	ReasonPartiallyServed = "PartiallyServed"
	// ReasonCredentialSecretConflict is set when the Secret that would
	// hold the credential exists and is not the account's: it is never
	// taken over, because whoever made it would choose the hash. (Not in
	// the ADR; the same rule as ReasonKeySecretConflict of the DKIM keys.)
	ReasonCredentialSecretConflict = "CredentialSecretConflict"
)

// The credential Secret (ADR 0015, section 3, point 4). Its name is
// <user>-credential; it is owned by the MailUser. The label values are
// limited to 63 characters and a user name may have 64, so the name of the
// user goes into an annotation and the Secret is labelled by the (shorter)
// name of the server, which is what the MailServer controller lists by.
const (
	credentialSuffix = "-credential"

	keyUsername     = "username"
	keyPasswordHash = "passwordHash"
	keyPassword     = "password"
	// sourceKeyPassword is the key of passwordSecretRef.
	sourceKeyPassword = "password"

	// annotationSourceVersion records the resourceVersion of the Secret
	// the hash was made from, so that a write that does not change the
	// password (an annotation, a label) does not rehash.
	annotationSourceVersion = "cnms.rpp.gmbh/password-source-version"
	// annotationMailUser carries the name of the owner, whatever its length.
	annotationMailUser = "cnms.rpp.gmbh/mailuser"
)

const (
	// credentialPoll is how often a provided password is read again:
	// Secrets are not watched (ADR 0009), so a changed password is found
	// by polling (ADR 0015, section 3, point 4).
	credentialPoll = time.Minute
	// credentialRepair is the safety requeue of an account whose
	// credential needs no polling: a deleted Secret is made again.
	credentialRepair = 10 * time.Minute
	// credentialRetry is the delay after a lost race on the Secret.
	credentialRetry = time.Second
	// requeueJitter spreads the periodic requeues, so that the accounts of an
	// operator that has just started do not all come back in the same second.
	requeueJitter = 0.2
)

// MailUserReconciler reconciles a MailUser object: it decides the
// addresses of the account with the other accounts of its server and
// makes the credential Secret with the password hash. It writes the status
// and the Secret only. The MailServer controller does not read a MailUser
// yet.
type MailUserReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the Secrets (never cached).
	APIReader client.Reader
	// Recorder emits Events when the state changes (nil in tests).
	Recorder            recorder.EventRecorder
	DomainAuthorization domainAuthorizationMode
}

// The access is that of ADR 0015, section 7, plus the Secret verbs it names:
// the credential Secret is read, created and updated in place (update is the
// verb the operator did not have). Secrets are not watched and not deleted:
// the owner reference collects them. The controller sets no finalizer, but
// the controller reference it sets (blockOwnerDeletion) needs update on the
// finalizers subresource where the OwnerReferencesPermissionEnforcement
// plugin is on.
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailusers,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailusers/status,verbs=patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers;maildomains;maildomaingrants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;create;update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile decides the addresses of the account and makes its credential.
// Everything it writes is derived from the objects, so it is safe to repeat
// and to run twice at once. Messages are fixed text: no password, hash or
// driver error reaches the status, an Event or the log.
func (r *MailUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	user := &cnmsv1alpha1.MailUser{}
	if err := r.Get(ctx, req.NamespacedName, user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !user.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // the owner reference collects the credential
	}
	orig := user.DeepCopy()
	prevReady := meta.FindStatusCondition(orig.Status.Conditions, conditionReady)
	prevCredential := meta.FindStatusCondition(orig.Status.Conditions, ConditionCredentialReady)

	v, err := r.loadView(ctx, user)
	if err != nil {
		return ctrl.Result{}, err
	}

	var cred credentialOutcome
	if v.sourceConflict == "" {
		if cred, err = r.reconcileCredential(ctx, user); err != nil {
			return ctrl.Result{}, err
		}
	}
	if cred.retry {
		// The Secret write lost a race: nothing of this pass is reported, the
		// next one reads the Secret again.
		return ctrl.Result{RequeueAfter: credentialRetry}, nil
	}
	v.apply(user, cred)

	r.emitEvents(user, prevReady, prevCredential)
	if !equality.Semantic.DeepEqual(orig.Status, user.Status) {
		if err := r.Status().Patch(ctx, user, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Updated MailUser status", "ready", string(meta.FindStatusCondition(user.Status.Conditions, conditionReady).Status),
			"reason", meta.FindStatusCondition(user.Status.Conditions, conditionReady).Reason)
	}

	switch {
	case v.sourceConflict != "":
		return ctrl.Result{RequeueAfter: wait.Jitter(credentialRepair, requeueJitter)}, nil
	case user.Spec.PasswordSecretRef != nil, cred.status != metav1.ConditionTrue:
		return ctrl.Result{RequeueAfter: wait.Jitter(credentialPoll, requeueJitter)}, nil
	}
	return ctrl.Result{RequeueAfter: wait.Jitter(credentialRepair, requeueJitter)}, nil
}

// userView is what the decision of one account needs, and its verdict.
type userView struct {
	server *cnmsv1alpha1.MailServer
	// sourceConflict is the Ready reason when the server has another user
	// source; empty otherwise.
	sourceConflict string
	// domains holds the failure message of every MailDomain that fails,
	// by the name of the object.
	failures map[string]string
	own      addresses.UserResult
}

// loadView reads the account's server, the MailDomains it names, and the
// other accounts of the server, and runs the decision for the server.
func (r *MailUserReconciler) loadView(ctx context.Context, user *cnmsv1alpha1.MailUser) (userView, error) {
	v := userView{failures: map[string]string{}}
	server := &cnmsv1alpha1.MailServer{}
	err := r.Get(ctx, types.NamespacedName{Namespace: user.Namespace, Name: user.Spec.ServerRef.Name}, server)
	switch {
	case err == nil:
		v.server = server
		switch {
		case server.Spec.DirectoryRef != nil:
			v.sourceConflict = ReasonDirectoryConfigured
		case server.Spec.Submission != nil && server.Spec.Submission.InsecureStaticUsers != nil:
			v.sourceConflict = ReasonUserSourceConflict
		}
	case !apierrors.IsNotFound(err):
		return v, err
	}

	grants, claims, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return v, err
	}
	ev := &authorizationEvaluator{grants: grantRefs(grants), claims: domainClaims(claims), mode: r.DomainAuthorization}

	// The list is only read, so it may share the objects of the cache.
	all := &cnmsv1alpha1.MailUserList{}
	if err := r.List(ctx, all, client.InNamespace(user.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return v, err
	}
	// The account being reconciled is the one just read, not the one in the
	// list, which may be a revision behind (or not there yet).
	users := slices.Clone(all.Items)
	if i := slices.IndexFunc(users, func(u cnmsv1alpha1.MailUser) bool { return u.Name == user.Name }); i >= 0 {
		users[i] = *user
	} else {
		users = append(users, *user)
	}
	in, failures := serverAddressInput(user.Spec.ServerRef.Name, users, nil, namespaceDomains(claims, user.Namespace), ev)
	v.failures = failures
	for _, u := range addresses.Decide(in).Users {
		if u.Name == user.Name {
			v.own = u
		}
	}
	return v, nil
}

// apply writes the verdict into the status of the account.
func (v *userView) apply(user *cnmsv1alpha1.MailUser, cred credentialOutcome) {
	st := &user.Status
	st.ObservedGeneration = user.Generation
	gen := user.Generation

	// The addresses. While the server does not exist, or has another user
	// source, nothing serves the account and the decision (which assumes a
	// server) has no meaning: there are no rows, and the warning is Unknown
	// with the reason of Ready.
	st.Address, st.Addresses = "", nil
	switch {
	case v.server == nil:
		setCondition(&st.Conditions, gen, ConditionAddressesServed, metav1.ConditionUnknown, ReasonServerNotFound,
			"the addresses are not decided: the MailServer does not exist")
	case v.sourceConflict != "":
		setCondition(&st.Conditions, gen, ConditionAddressesServed, metav1.ConditionUnknown, v.sourceConflict,
			"the addresses are not decided: the MailServer has another user source")
	default:
		for i, row := range v.own.Rows {
			if i == 0 && row.Reason != addresses.ReasonDomainNotFound {
				st.Address = row.Address
			}
			st.Addresses = append(st.Addresses, cnmsv1alpha1.MailUserAddressStatus{
				Address: row.Address, Kind: cnmsv1alpha1.AddressKind(row.Kind), Served: row.Served, Reason: row.Reason,
			})
		}
		if v.own.PartiallyServed() {
			setCondition(&st.Conditions, gen, ConditionAddressesServed, metav1.ConditionFalse, ReasonPartiallyServed, v.unserved())
		} else {
			setCondition(&st.Conditions, gen, ConditionAddressesServed, metav1.ConditionTrue, ReasonAllServed, "every address of the account is served")
		}
	}

	// The credential.
	switch {
	case v.sourceConflict != "":
		meta.RemoveStatusCondition(&st.Conditions, ConditionCredentialReady)
	default:
		setCondition(&st.Conditions, gen, ConditionCredentialReady, cred.status, cred.reason, cred.message)
		if cred.secretName != "" {
			st.CredentialSecretName, st.CredentialRevision = cred.secretName, cred.revision
		}
	}

	// Ready: the first failure in the order of ADR 0015, section 8.
	status, reason, message := metav1.ConditionTrue, ReasonReady, "the account is ready"
	switch {
	case v.server == nil:
		status, reason, message = metav1.ConditionFalse, ReasonServerNotFound,
			fmt.Sprintf("the MailServer %q does not exist in this namespace", user.Spec.ServerRef.Name)
	case v.sourceConflict == ReasonDirectoryConfigured:
		status, reason, message = metav1.ConditionFalse, ReasonDirectoryConfigured,
			fmt.Sprintf("the MailServer %q uses a MailDirectory: a server has either a directory or MailUsers", v.server.Name)
	case v.sourceConflict == ReasonUserSourceConflict:
		status, reason, message = metav1.ConditionFalse, ReasonUserSourceConflict,
			fmt.Sprintf("the MailServer %q has submission.insecureStaticUsers: a server has one user source", v.server.Name)
	case v.own.Suspended:
		status, reason, message = metav1.ConditionFalse, v.own.Reason, v.failures[user.Spec.DomainRef.Name]
	case cred.status == metav1.ConditionFalse:
		status, reason, message = metav1.ConditionFalse, cred.reason, cred.message
	case user.Spec.Disabled != nil && *user.Spec.Disabled:
		status, reason, message = metav1.ConditionTrue, ReasonDisabled,
			"the account is disabled: no login and no sending; mail is still delivered"
	}
	setCondition(&st.Conditions, gen, conditionReady, status, reason, message)
}

// unserved names the addresses that are not served and why. Addresses and
// reasons are not secret.
func (v *userView) unserved() string {
	var parts []string
	for _, row := range v.own.Rows {
		if !row.Served {
			parts = append(parts, row.Address+" ("+row.Reason+")")
		}
	}
	if len(parts) > 10 {
		parts = append(parts[:10], fmt.Sprintf("and %d more", len(parts)-10))
	}
	return "not served: " + strings.Join(parts, ", ")
}

// credentialOutcome is the result of the credential step.
type credentialOutcome struct {
	status          metav1.ConditionStatus
	reason, message string
	// secretName and revision are the status fields; empty when there is no
	// credential Secret.
	secretName, revision string
	// retry is set after a lost race on the Secret.
	retry bool
}

func credentialSecretName(user string) string { return user + credentialSuffix }

// reconcileCredential makes the credential Secret (ADR 0015, section 3,
// point 4). The hash is made once per password: the Secret is created once
// (AlreadyExists means somebody else was first, and is read again), a
// generated password is never overwritten, every update carries the
// resourceVersion it read, and a provided password is verified against the
// stored hash before it is hashed again, so that a write to the source that
// changes no password (a label, an annotation) rewrites nothing.
func (r *MailUserReconciler) reconcileCredential(ctx context.Context, user *cnmsv1alpha1.MailUser) (credentialOutcome, error) {
	name := credentialSecretName(user.Name)
	existing := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: user.Namespace, Name: name}, existing)
	if apierrors.IsNotFound(err) {
		existing = nil
	} else if err != nil {
		return credentialOutcome{}, err
	}
	if existing != nil && !metav1.IsControlledBy(existing, user) {
		return credentialOutcome{status: metav1.ConditionFalse, reason: ReasonCredentialSecretConflict,
			message: fmt.Sprintf("the Secret %q exists and is not owned by this MailUser; it is not taken over", name)}, nil
	}
	// keep reports a problem and leaves a credential that exists alone.
	keep := func(reason, message string) credentialOutcome {
		o := credentialOutcome{status: metav1.ConditionFalse, reason: reason, message: message}
		if existing != nil {
			o.secretName, o.revision = name, kept(user, existing)
		}
		return o
	}

	var password, sourceVersion string
	if user.Spec.PasswordSecretRef != nil {
		var bad *badConfig
		if password, sourceVersion, bad, err = r.providedPassword(ctx, user); err != nil {
			return credentialOutcome{}, err
		} else if bad != nil {
			return keep(bad.reason, bad.message), nil
		}
	}
	desired, hashChanged, err := decideCredential(ctx, existing, password, sourceVersion, user.Spec.PasswordSecretRef == nil)
	if err != nil {
		if ctx.Err() != nil {
			return credentialOutcome{}, err
		}
		logf.FromContext(ctx).Info("Could not hash the password", "user", user.Name)
		return keep(ReasonHashFailed, "the password could not be hashed"), nil
	}
	desired.Data[keyUsername] = []byte(user.Name)
	if err := r.shapeCredential(desired, user, name, sourceVersion); err != nil {
		return credentialOutcome{}, err
	}
	final, retry, err := writeCredential(ctx, r.Client, existing, desired)
	if err != nil || retry {
		return credentialOutcome{retry: retry}, err
	}
	revision := kept(user, final)
	if hashChanged {
		revision = final.ResourceVersion
	}
	return credentialOutcome{status: metav1.ConditionTrue, reason: ReasonReady, message: "the password hash is in the credential Secret",
		secretName: name, revision: revision}, nil
}

// providedPassword reads the password of spec.passwordSecretRef. A Secret or
// key that is missing, or a password that is too short, is a problem to
// report, not an error: it is read again on the next requeue.
func (r *MailUserReconciler) providedPassword(ctx context.Context, user *cnmsv1alpha1.MailUser) (password, version string, bad *badConfig, err error) {
	ref := user.Spec.PasswordSecretRef.Name
	src := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: user.Namespace, Name: ref}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return "", "", &badConfig{ReasonSecretNotFound, fmt.Sprintf("the Secret %q does not exist", ref)}, nil
		}
		return "", "", nil, err
	}
	pw, ok := src.Data[sourceKeyPassword]
	if !ok {
		return "", "", &badConfig{ReasonSecretNotFound, fmt.Sprintf("the Secret %q has no key %q", ref, sourceKeyPassword)}, nil
	}
	if utf8.RuneCount(pw) < minPasswordLength {
		return "", "", &badConfig{ReasonPasswordInvalid,
			fmt.Sprintf("the password in the Secret %q must have at least %d characters", ref, minPasswordLength)}, nil
	}
	return string(pw), src.ResourceVersion, nil, nil
}

// decideCredential returns the Secret to write and whether its hash is new.
// A generated password that exists is kept. A provided password is hashed
// when there is no hash or the stored one does not verify; if the source is
// the one the hash was made from, nothing is hashed or verified. The data
// holds the hash (and the generated password), without the username.
func decideCredential(ctx context.Context, existing *corev1.Secret, password, sourceVersion string, generated bool) (*corev1.Secret, bool, error) {
	desired := &corev1.Secret{}
	if existing != nil {
		desired = existing.DeepCopy()
	}
	if desired.Data == nil {
		desired.Data = map[string][]byte{}
	}
	if generated {
		if existing != nil && len(existing.Data[keyPassword]) > 0 && len(existing.Data[keyPasswordHash]) > 0 &&
			existing.Annotations[annotationSourceVersion] == "" {
			return desired, false, nil // the generated password is kept, never overwritten
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
	stored := ""
	if existing != nil {
		stored = string(existing.Data[keyPasswordHash])
	}
	if existing != nil && existing.Annotations[annotationSourceVersion] == sourceVersion &&
		len(existing.Data[keyPassword]) == 0 && stored != "" {
		return desired, false, nil
	}
	match := false
	if stored != "" {
		var err error
		if match, err = verifyHash(ctx, password, stored); err != nil {
			return nil, false, err
		}
	}
	hash := stored
	if !match {
		var err error
		if hash, err = hashPassword(ctx, password); err != nil {
			return nil, false, err
		}
	}
	desired.Data = map[string][]byte{keyPasswordHash: []byte(hash)}
	return desired, !match, nil
}

// writeCredential creates the Secret, or updates it when it differs. The
// update carries the resourceVersion that was read: a write that raced with
// this one is a conflict, as is a create that finds the Secret, and both are
// reported as retry, to read again, never as an overwrite.
//
// The MailServiceAccount controller writes its credential Secret with the
// same function.
func writeCredential(ctx context.Context, c client.Client, existing, desired *corev1.Secret) (final *corev1.Secret, retry bool, err error) {
	switch {
	case existing == nil:
		if err := c.Create(ctx, desired); err != nil {
			return nil, apierrors.IsAlreadyExists(err), ignoreRace(err, apierrors.IsAlreadyExists)
		}
	case !equality.Semantic.DeepEqual(existing.Data, desired.Data) || !equality.Semantic.DeepEqual(existing.ObjectMeta, desired.ObjectMeta):
		if err := c.Update(ctx, desired); err != nil {
			return nil, apierrors.IsConflict(err), ignoreRace(err, apierrors.IsConflict)
		}
	default:
		return existing, false, nil
	}
	return desired, false, nil
}

// ignoreRace drops the error when it is the lost race.
func ignoreRace(err error, lost func(error) bool) error {
	if lost(err) {
		return nil
	}
	return err
}

// kept is the credentialRevision to report when the hash did not change:
// the one already in the status, or the resourceVersion of the Secret when
// the status has none (a first reconcile, or a status lost in a restore).
func kept(user *cnmsv1alpha1.MailUser, s *corev1.Secret) string {
	if user.Status.CredentialSecretName == s.Name && user.Status.CredentialRevision != "" {
		return user.Status.CredentialRevision
	}
	return s.ResourceVersion
}

// shapeCredential sets the metadata of the credential Secret: the labels
// that the MailServer controller lists by, the annotations and the
// controller reference. The label of the server is left out when the name
// is not a valid label value (a MailServer name is limited to 44
// characters, so such a server does not exist); the name of the user, up to
// 64 characters, is never a label.
func (r *MailUserReconciler) shapeCredential(s *corev1.Secret, user *cnmsv1alpha1.MailUser, name, sourceVersion string) error {
	s.Name, s.Namespace, s.Type = name, user.Namespace, corev1.SecretTypeOpaque
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	s.Labels[labelAppManagedBy] = manifests.ManagedByValue
	s.Labels[manifests.ManagedByLabel] = manifests.ManagedByValue
	delete(s.Labels, manifests.ServerLabel)
	if server := user.Spec.ServerRef.Name; len(validation.IsValidLabelValue(server)) == 0 {
		s.Labels[manifests.ServerLabel] = server
	}
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[annotationMailUser] = user.Name
	if sourceVersion == "" {
		delete(s.Annotations, annotationSourceVersion)
	} else {
		s.Annotations[annotationSourceVersion] = sourceVersion
	}
	return controllerutil.SetControllerReference(user, s, r.Scheme)
}

// emitEvents records an Event when Ready or CredentialReady turn bad, and
// when Ready comes back. The messages are the fixed ones of the conditions.
func (r *MailUserReconciler) emitEvents(user *cnmsv1alpha1.MailUser, prevReady, prevCredential *metav1.Condition) {
	if r.Recorder == nil {
		return
	}
	ready := meta.FindStatusCondition(user.Status.Conditions, conditionReady)
	credential := meta.FindStatusCondition(user.Status.Conditions, ConditionCredentialReady)
	switch {
	case ready.Status == metav1.ConditionFalse && changed(prevReady, ready):
		r.Recorder.Eventf(user, nil, corev1.EventTypeWarning, ready.Reason, "Reconcile", "%s", eventMessage(ready.Message))
	case ready.Status == metav1.ConditionTrue && prevReady != nil && prevReady.Status == metav1.ConditionFalse:
		r.Recorder.Eventf(user, nil, corev1.EventTypeNormal, ready.Reason, "Reconcile", "%s", eventMessage(ready.Message))
	}
	if credential != nil && credential.Status == metav1.ConditionFalse && changed(prevCredential, credential) &&
		(ready.Reason != credential.Reason) {
		r.Recorder.Eventf(user, nil, corev1.EventTypeWarning, credential.Reason, "Credential", "%s", eventMessage(credential.Message))
	}
}

// SetupWithManager sets up the controller with the Manager. The account
// reacts to changes of its spec, of the MailServer, the MailDomains and the
// grants that decide its addresses, and of the accounts that claim the same
// address. The decision reads specs only (and the existence of the objects),
// so every watch ignores status writes: the generation predicate keeps a
// status write, of this account or of a MailDomain, from requeueing every
// account that depends on it. The watch of the MailServer controller on
// MailUsers (the next step) is the opposite and has no such predicate: it
// must see the status write of a new credentialRevision (ADR 0015, section
// 7). The credential Secret is not watched:
// Secrets are not cached (ADR 0009, ClientOptions), so the reconcile
// requeues (credentialPoll, credentialRepair) to read a provided password
// and to make a deleted Secret again.
//
// One hash holds 19 MiB and the manager container is limited to 192 MiB (with
// GOMEMLIMIT 160MiB), so the hashes run one at a time (hashSlots); two reconciles may run at once,
// the second doing the cheap parts while the first hashes.
func (r *MailUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailUser{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("mailuser").
		WithOptions(runtimecontroller.Options{MaxConcurrentReconciles: 2}).
		Watches(&cnmsv1alpha1.MailUser{}, handler.EnqueueRequestsFromMapFunc(r.mapClaimants),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&cnmsv1alpha1.MailServer{}, handler.EnqueueRequestsFromMapFunc(r.mapServerUsers),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&cnmsv1alpha1.MailDomain{}, handler.EnqueueRequestsFromMapFunc(r.mapDomainUsers),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&cnmsv1alpha1.MailDomainGrant{}, handler.EnqueueRequestsFromMapFunc(r.mapGrantUsers)).
		Complete(r)
}

// mapClaimants enqueues the other accounts of the server that claim an
// address of obj: a conflict is decided by all claimants, so a change of
// one reaches the others (ADR 0015, section 7). For an update the handler
// maps the old object as well, so an address that was given up reaches the
// account that lost it.
func (r *MailUserReconciler) mapClaimants(ctx context.Context, obj client.Object) []reconcile.Request {
	u, ok := obj.(*cnmsv1alpha1.MailUser)
	if !ok {
		return nil
	}
	var reqs []reconcile.Request
	seen := map[types.NamespacedName]bool{client.ObjectKeyFromObject(u): true}
	for _, key := range claimKeys(u) {
		list := &cnmsv1alpha1.MailUserList{}
		if err := r.List(ctx, list, client.InNamespace(u.Namespace), client.MatchingFields{mailUserAddressIndex: key}); err != nil {
			logf.FromContext(ctx).Error(err, "Could not list MailUsers", "server", u.Spec.ServerRef.Name)
			continue
		}
		for i := range list.Items {
			if k := client.ObjectKeyFromObject(&list.Items[i]); !seen[k] {
				seen[k] = true
				reqs = append(reqs, reconcile.Request{NamespacedName: k})
			}
		}
	}
	return reqs
}

// mapServerUsers enqueues the accounts that name a MailServer.
func (r *MailUserReconciler) mapServerUsers(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.usersBy(ctx, obj.GetNamespace(), mailUserServerIndex, obj.GetName())
}

// mapDomainUsers enqueues the accounts that name a MailDomain, and those
// that name a MailDomain of another namespace for the same domain: the
// uniqueness of a domain is judged across namespaces (ADR 0003).
func (r *MailUserReconciler) mapDomainUsers(ctx context.Context, obj client.Object) []reconcile.Request {
	d, ok := obj.(*cnmsv1alpha1.MailDomain)
	if !ok {
		return nil
	}
	reqs := r.usersBy(ctx, d.Namespace, mailUserDomainIndex, d.Name)
	seen := map[types.NamespacedName]bool{}
	for _, q := range reqs {
		seen[q.NamespacedName] = true
	}
	if d.Spec.Domain != "" {
		peers := &cnmsv1alpha1.MailDomainList{}
		if err := r.List(ctx, peers, client.MatchingFields{mailDomainDomainIndex: d.Spec.Domain}); err != nil {
			logf.FromContext(ctx).Error(err, "Could not list MailDomains", "domain", d.Spec.Domain)
		}
		for i := range peers.Items {
			for _, q := range r.usersBy(ctx, peers.Items[i].Namespace, mailUserDomainIndex, peers.Items[i].Name) {
				if !seen[q.NamespacedName] {
					seen[q.NamespacedName] = true
					reqs = append(reqs, q)
				}
			}
		}
	}
	return reqs
}

// mapGrantUsers enqueues every account: a grant decides which namespace
// may use a domain, and grants change rarely.
func (r *MailUserReconciler) mapGrantUsers(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &cnmsv1alpha1.MailUserList{}
	if err := r.List(ctx, list); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list MailUsers")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

func (r *MailUserReconciler) usersBy(ctx context.Context, ns, index, value string) []reconcile.Request {
	list := &cnmsv1alpha1.MailUserList{}
	if err := r.List(ctx, list, client.InNamespace(ns), client.MatchingFields{index: value}); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list MailUsers", "index", index)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	slices.SortFunc(reqs, func(a, b reconcile.Request) int { return strings.Compare(a.String(), b.String()) })
	return reqs
}
