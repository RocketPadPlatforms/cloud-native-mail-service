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
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/addresses"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/directory"
)

// MailServiceAccount condition types and reasons (ADR 0008 and ADR 0015,
// sections 4.9 and 8). The reasons that are those of other resources
// (ServerNotFound, CredentialSecretConflict, HashFailed, OwnershipConflict)
// are reused.
const (
	// ConditionEgressPolicyReady says whether the NetworkPolicy of the
	// account is in place.
	ConditionEgressPolicyReady = "EgressPolicyReady"

	// ReasonNamespaceNotAllowed is set when the MailServer does not list
	// the namespace of the account in spec.serviceAccounts.allowedNamespaces.
	ReasonNamespaceNotAllowed = "NamespaceNotAllowed"
	// ReasonSenderNotAuthorized is set when the domain of the sender is
	// not one the server serves and authorizes, or one the server allows
	// the namespace.
	ReasonSenderNotAuthorized = "SenderNotAuthorized"
	// ReasonSenderConflict is set when the sender is claimed by another
	// account of the server, or belongs to a user of its directory.
	ReasonSenderConflict = addresses.ReasonSenderConflict
	// ReasonDirectoryUnavailable is set when the directory of the server
	// could not be asked whether the sender is taken, and the account is
	// new or has a new sender: nothing is issued for it (a fail-closed
	// admission, not a runtime dependency).
	ReasonDirectoryUnavailable = "DirectoryUnavailable"
	// ReasonSubmissionDisabled is set when the server has no submission
	// listener to send through.
	ReasonSubmissionDisabled = "SubmissionDisabled"
)

const (
	// accountRepair is the safety requeue of an account: Secrets and the
	// CA are not watched, so a deleted credential is made again and a CA
	// that appears is picked up by this requeue.
	accountRepair = 10 * time.Minute
	// accountDirectoryRetry is the delay before the directory is asked again.
	accountDirectoryRetry = time.Minute
)

// MailServiceAccountReconciler reconciles a MailServiceAccount: it decides
// whether the account may send as its sender and, if so, makes the
// credential Secret and the egress NetworkPolicy in its namespace (ADR 0008,
// ADR 0015 section 4.9). It writes the status and those two objects only. The
// MailServer serves the account (mailserver_serviceaccounts.go) and decides
// again, with every other claimant, whether it may.
type MailServiceAccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the Secrets and the NetworkPolicies of tenants
	// (never cached).
	APIReader client.Reader
	// Directory asks the directory of a server whether a sender is
	// taken; tests pass a fake. Nil means no directory can be asked.
	Directory directory.SenderLooker
	// Recorder emits Events when the state changes (nil in tests).
	Recorder            recorder.EventRecorder
	DomainAuthorization domainAuthorizationMode

	// verified spares the Argon2id verification of a credential Secret that
	// has not changed since it was verified.
	verified verifiedSecrets
	// onVerify is called before each verification (tests count them).
	onVerify func()
}

// The access is that of ADR 0015, section 7. The owner references the
// controller sets (blockOwnerDeletion) need update on the finalizers
// subresource where the OwnerReferencesPermissionEnforcement plugin is on.
// The controller sets no finalizer: the credential and the policy are
// collected through their owner reference, and removed explicitly when the
// account is revoked. It reads the server's TLS Secret for ca.crt only.
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailserviceaccounts,verbs=get;list;watch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailserviceaccounts/status,verbs=patch
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailserviceaccounts/finalizers,verbs=update
// +kubebuilder:rbac:groups=cnms.rpp.gmbh,resources=mailservers;maildomains;maildomaingrants;maildirectories;mailusers,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// accountVerdict is why an account is not served, in the order of the
// checks; an empty reason is a pass.
type accountVerdict struct {
	reason, message string
	// revoke removes the credential and the policy: the namespace or the
	// sender is not allowed (ADR 0008).
	revoke bool
	// unadmit clears the admitted sender: the sender is taken.
	unadmit bool
	// retryDirectory asks again soon.
	retryDirectory bool
}

// Reconcile decides the account, then makes or removes its objects. Messages
// are fixed text: no password, hash or driver error reaches the status, an
// Event or the log.
func (r *MailServiceAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	acc := &cnmsv1alpha1.MailServiceAccount{}
	if err := r.Get(ctx, req.NamespacedName, acc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !acc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // the owner references collect the objects
	}
	orig := acc.DeepCopy()
	prevReady := meta.FindStatusCondition(orig.Status.Conditions, conditionReady)

	server, verdict, ep, err := r.decide(ctx, acc)
	if err != nil {
		return ctrl.Result{}, err
	}

	st := &acc.Status
	st.ObservedGeneration = acc.Generation
	var cred accountCredential
	var egress egressOutcome
	switch verdict.reason {
	case "":
		st.AdmittedSender, st.AdmittedFor = acc.Spec.Sender, admissionKey(server)
		if cred, err = r.reconcileCredential(ctx, acc, server, ep); err != nil {
			return ctrl.Result{}, err
		}
		if cred.retry {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if egress, err = r.reconcileEgress(ctx, acc, server); err != nil {
			return ctrl.Result{}, err
		}
		if egress.retry {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		setCondition(&st.Conditions, acc.Generation, ConditionCredentialReady, cred.status, cred.reason, cred.message)
		setCondition(&st.Conditions, acc.Generation, ConditionEgressPolicyReady, egress.status, egress.reason, egress.message)
		if cred.secretName != "" {
			st.CredentialSecretName, st.CredentialRevision = cred.secretName, cred.revision
		}
	default:
		if verdict.revoke {
			if err := r.revoke(ctx, acc); err != nil {
				return ctrl.Result{}, err
			}
			st.CredentialSecretName, st.CredentialRevision = "", ""
			verdict.unadmit = true
		}
		if verdict.unadmit {
			st.AdmittedSender, st.AdmittedFor = "", ""
		}
		meta.RemoveStatusCondition(&st.Conditions, ConditionCredentialReady)
		meta.RemoveStatusCondition(&st.Conditions, ConditionEgressPolicyReady)
	}
	r.setReady(acc, verdict, cred, egress)

	r.emitEvents(acc, prevReady)
	if !equality.Semantic.DeepEqual(orig.Status, acc.Status) {
		if err := r.Status().Patch(ctx, acc, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
		ready := meta.FindStatusCondition(acc.Status.Conditions, conditionReady)
		log.Info("Updated MailServiceAccount status", "ready", string(ready.Status), "reason", ready.Reason)
	}
	if verdict.retryDirectory {
		return ctrl.Result{RequeueAfter: wait.Jitter(accountDirectoryRetry, requeueJitter)}, nil
	}
	return ctrl.Result{RequeueAfter: wait.Jitter(accountRepair, requeueJitter)}, nil
}

// setReady writes Ready: the first failure in the order of the checks, then
// the credential and the policy.
func (r *MailServiceAccountReconciler) setReady(acc *cnmsv1alpha1.MailServiceAccount, v accountVerdict, cred accountCredential, egress egressOutcome) {
	status, reason, message := metav1.ConditionTrue, ReasonReady, "the account is ready"
	switch {
	case v.reason != "":
		status, reason, message = metav1.ConditionFalse, v.reason, v.message
	case cred.status == metav1.ConditionFalse:
		status, reason, message = metav1.ConditionFalse, cred.reason, cred.message
	case egress.status == metav1.ConditionFalse:
		status, reason, message = metav1.ConditionFalse, egress.reason, egress.message
	}
	setCondition(&acc.Status.Conditions, acc.Generation, conditionReady, status, reason, message)
}

// decide runs the checks of ADR 0008 and 0015 (section 4.9) in order and
// returns the first that fails. The server and the endpoint it is served at
// are returned when the server exists.
func (r *MailServiceAccountReconciler) decide(ctx context.Context, acc *cnmsv1alpha1.MailServiceAccount) (*cnmsv1alpha1.MailServer, accountVerdict, serverEndpoint, error) {
	ref := types.NamespacedName{Namespace: acc.Spec.ServerRef.Namespace, Name: acc.Spec.ServerRef.Name}
	if ref.Namespace == "" {
		ref.Namespace = acc.Namespace
	}
	notAllowed := accountVerdict{reason: ReasonNamespaceNotAllowed,
		message: fmt.Sprintf("the MailServer %q does not list the namespace %q in spec.serviceAccounts.allowedNamespaces", ref.Name, acc.Namespace)}
	server := &cnmsv1alpha1.MailServer{}
	if err := r.Get(ctx, ref, server); err != nil {
		if apierrors.IsNotFound(err) {
			if ref.Namespace != acc.Namespace {
				// A server of another namespace that is missing looks like
				// one that does not consent: whether it exists is not for
				// this namespace to learn before it is consented to. A
				// missing server revokes nothing.
				return nil, notAllowed, serverEndpoint{}, nil
			}
			return nil, accountVerdict{reason: ReasonServerNotFound,
				message: fmt.Sprintf("the MailServer %q does not exist in the namespace %q", ref.Name, ref.Namespace)}, serverEndpoint{}, nil
		}
		return nil, accountVerdict{}, serverEndpoint{}, err
	}

	allowed, senderDomains := accountConsent(server, acc.Namespace)
	if !allowed {
		notAllowed.revoke = true
		return server, notAllowed, serverEndpoint{}, nil
	}
	_, domain := senderParts(acc.Spec.Sender)
	if !senderDomainAllowed(senderDomains, domain) {
		return server, accountVerdict{reason: ReasonSenderNotAuthorized, revoke: true,
			message: fmt.Sprintf("the MailServer does not allow the namespace %q to send from the domain %q", acc.Namespace, domain)}, serverEndpoint{}, nil
	}

	grants, claims, err := listAuthorizationInput(ctx, r)
	if err != nil {
		return nil, accountVerdict{}, serverEndpoint{}, err
	}
	servers := &cnmsv1alpha1.MailServerList{}
	if err := r.List(ctx, servers, client.UnsafeDisableDeepCopy); err != nil {
		return nil, accountVerdict{}, serverEndpoint{}, err
	}
	ev := &authorizationEvaluator{server: server, grants: grantRefs(grants), claims: domainClaims(claims),
		servers: serverClaims(servers), mode: r.DomainAuthorization}
	ep := endpointOf(server, ev.authorizeHostname() == nil)

	// Every claimant of the server's address space takes part, as the
	// MailServer will see it: the MailUsers when they are its user source,
	// and the other accounts it consents to. Static test users are not read
	// here; the server decides with them.
	var users []cnmsv1alpha1.MailUser
	if server.Spec.DirectoryRef == nil && (server.Spec.Submission == nil || server.Spec.Submission.InsecureStaticUsers == nil) {
		all := &cnmsv1alpha1.MailUserList{}
		if err := r.List(ctx, all, client.InNamespace(server.Namespace), client.UnsafeDisableDeepCopy); err != nil {
			return nil, accountVerdict{}, serverEndpoint{}, err
		}
		users = all.Items
	}
	// The accounts of this server, from the index of the cache: the list is
	// only read (serviceSenders copies what it keeps).
	every := &cnmsv1alpha1.MailServiceAccountList{}
	if err := r.List(ctx, every, client.MatchingFields{mailServiceAccountServerIndex: server.Namespace + "/" + server.Name},
		client.UnsafeDisableDeepCopy); err != nil {
		return nil, accountVerdict{}, serverEndpoint{}, err
	}
	// The account being reconciled is the one just read, not the one in the
	// list, which may be a revision behind (or not there yet).
	accounts := slices.Clone(every.Items)
	if i := slices.IndexFunc(accounts, func(a cnmsv1alpha1.MailServiceAccount) bool {
		return a.Namespace == acc.Namespace && a.Name == acc.Name
	}); i >= 0 {
		accounts[i] = *acc
	} else {
		accounts = append(accounts, *acc)
	}
	in, _ := serverAddressInput(server.Name, users, serviceSenders(server, accounts), namespaceDomains(claims, server.Namespace), ev)
	owner := acc.Namespace + "/" + acc.Name
	for _, s := range addresses.Decide(in).Senders {
		if s.Owner != owner {
			continue
		}
		switch s.Row.Reason {
		case addresses.ReasonServed:
		case addresses.ReasonSenderConflict:
			return server, accountVerdict{reason: ReasonSenderConflict, message: "the sender cannot be used: " + s.Row.Detail}, ep, nil
		default:
			return server, accountVerdict{reason: ReasonSenderNotAuthorized, revoke: true,
				message: unauthorizedMessage(s.Row.Reason, domain, server.Namespace)}, ep, nil
		}
	}

	if server.Spec.Submission != nil && server.Spec.Submission.Enabled != nil && !*server.Spec.Submission.Enabled {
		return server, accountVerdict{reason: ReasonSubmissionDisabled,
			message: fmt.Sprintf("the MailServer %q has submission disabled", server.Name)}, ep, nil
	}
	v, err := r.checkDirectory(ctx, acc, server)
	return server, v, ep, err
}

// admissionKey says what an admission was checked against: the server and
// the directory it had (ADR 0015, section 4.9, point 7). A server that gains a
// directory, or another one, is another key.
func admissionKey(server *cnmsv1alpha1.MailServer) string {
	dir := ""
	if server.Spec.DirectoryRef != nil {
		dir = server.Spec.DirectoryRef.Name
	}
	return server.Namespace + "/" + server.Name + "/" + dir
}

// admitted reports whether the sender of the account was admitted against
// this server and its directory.
func admitted(acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer) bool {
	return acc.Status.AdmittedSender == acc.Spec.Sender && acc.Status.AdmittedFor == admissionKey(server)
}

// unauthorizedMessage is why the domain of the sender is not served.
func unauthorizedMessage(reason, domain, serverNamespace string) string {
	switch reason {
	case addresses.ReasonDomainNotFound:
		return fmt.Sprintf("no MailDomain for %q exists in the namespace %q of the MailServer", domain, serverNamespace)
	case addresses.ReasonDomainNotServed:
		return fmt.Sprintf("the MailDomain for %q is served by another MailServer", domain)
	}
	return fmt.Sprintf("the domain %q is not authorized for the namespace %q of the MailServer", domain, serverNamespace)
}

// checkDirectory asks the directory of a server that has one whether the
// sender already belongs to one of its accounts (ADR 0015, section 4.9,
// point 3). It asks once per account, sender and server with its directory
// (the admission, status.admittedSender and status.admittedFor): an admitted
// account is not asked again, so a directory that is down never takes a
// working account away. A new account, a new sender, another server or
// another directory is asked, and a directory that cannot answer withholds
// the credential (DirectoryUnavailable).
func (r *MailServiceAccountReconciler) checkDirectory(ctx context.Context, acc *cnmsv1alpha1.MailServiceAccount, server *cnmsv1alpha1.MailServer) (accountVerdict, error) {
	if server.Spec.DirectoryRef == nil || admitted(acc, server) {
		return accountVerdict{}, nil
	}
	unavailable := accountVerdict{reason: ReasonDirectoryUnavailable, retryDirectory: true,
		message: "the directory of the MailServer could not be asked whether the sender is taken; no credential is issued until it can"}
	dir := &cnmsv1alpha1.MailDirectory{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: server.Namespace, Name: server.Spec.DirectoryRef.Name}, dir); err != nil {
		if apierrors.IsNotFound(err) {
			return unavailable, nil
		}
		return accountVerdict{}, err
	}
	target, bad, err := directoryResolver{r.APIReader}.resolve(ctx, dir)
	if err != nil {
		return accountVerdict{}, err
	}
	if bad != nil || r.Directory == nil {
		return unavailable, nil
	}
	askCtx, cancel := context.WithTimeout(ctx, directoryProbeTimeout)
	defer cancel()
	owned, outcome := target.owns(askCtx, r.Directory, acc.Spec.Sender)
	switch {
	case outcome != directory.Reachable:
		logf.FromContext(ctx).Info("Could not ask the directory about the sender", "host", target.host(), "outcome", string(outcome))
		return unavailable, nil
	case owned:
		return accountVerdict{reason: ReasonSenderConflict, unadmit: true,
			message: "the sender is an address of an account of the directory of the MailServer"}, nil
	}
	return accountVerdict{}, nil
}

// owns asks the directory whether the address belongs to an account of it.
func (t probeTarget) owns(ctx context.Context, l directory.SenderLooker, address string) (bool, directory.Outcome) {
	if t.ldap != nil {
		return l.OwnsLDAP(ctx, *t.ldap, address)
	}
	return l.OwnsSQL(ctx, *t.sql, address)
}

// emitEvents records an Event when Ready turns bad or changes its reason, and
// when it comes back. The messages are the fixed ones of the conditions.
func (r *MailServiceAccountReconciler) emitEvents(acc *cnmsv1alpha1.MailServiceAccount, prevReady *metav1.Condition) {
	if r.Recorder == nil {
		return
	}
	ready := meta.FindStatusCondition(acc.Status.Conditions, conditionReady)
	switch {
	case ready.Status == metav1.ConditionFalse && changed(prevReady, ready):
		r.Recorder.Eventf(acc, nil, corev1.EventTypeWarning, ready.Reason, "Reconcile", "%s", eventMessage(ready.Message))
	case ready.Status == metav1.ConditionTrue && prevReady != nil && prevReady.Status == metav1.ConditionFalse:
		r.Recorder.Eventf(acc, nil, corev1.EventTypeNormal, ready.Reason, "Reconcile", "%s", eventMessage(ready.Message))
	}
}

// SetupWithManager sets up the controller with the Manager. The account
// reacts to changes of its spec, of the MailServer, the MailDomains, the
// grants and the MailUsers that decide its sender, of the accounts that claim
// the same sender, and of its NetworkPolicy. The decision reads specs only,
// so every watch ignores status writes: the generation predicate keeps a
// status write, of this account or of a MailServer, from requeueing the
// accounts that depend on it. The credential Secret is not watched: Secrets
// are not cached (ADR 0009, ClientOptions), so the reconcile requeues
// (accountRepair) to make a deleted Secret again.
//
// The watches map through the indexes of the accounts (the server, the
// sender), so an event reaches the accounts of one server and not every
// account of the cluster. Only a grant, which decides the domains of every
// namespace and changes rarely, reaches all of them.
func (r *MailServiceAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	gen := builder.WithPredicates(predicate.GenerationChangedPredicate{})
	return ctrl.NewControllerManagedBy(mgr).
		For(&cnmsv1alpha1.MailServiceAccount{}, gen).
		Named("mailserviceaccount").
		WithOptions(runtimecontroller.Options{MaxConcurrentReconciles: 2}).
		Owns(&networkingv1.NetworkPolicy{}).
		Watches(&cnmsv1alpha1.MailServiceAccount{}, handler.EnqueueRequestsFromMapFunc(r.mapClaimants), gen).
		Watches(&cnmsv1alpha1.MailServer{}, handler.EnqueueRequestsFromMapFunc(r.mapServerAccounts), gen).
		Watches(&cnmsv1alpha1.MailDomain{}, handler.EnqueueRequestsFromMapFunc(r.mapDomainAccounts), gen).
		Watches(&cnmsv1alpha1.MailDomainGrant{}, handler.EnqueueRequestsFromMapFunc(r.mapGrantAccounts), gen).
		Watches(&cnmsv1alpha1.MailUser{}, handler.EnqueueRequestsFromMapFunc(r.mapUserAccounts), gen).
		Complete(r)
}

// accountsBy enqueues the accounts of an index value, sorted.
func (r *MailServiceAccountReconciler) accountsBy(ctx context.Context, index, value string) []reconcile.Request {
	list := &cnmsv1alpha1.MailServiceAccountList{}
	if err := r.List(ctx, list, client.MatchingFields{index: value}); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list MailServiceAccounts", "index", index)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// accountsOfServers enqueues the accounts of several servers, each once.
func (r *MailServiceAccountReconciler) accountsOfServers(ctx context.Context, keys ...string) []reconcile.Request {
	var reqs []reconcile.Request
	seen := map[types.NamespacedName]bool{}
	for _, key := range keys {
		for _, q := range r.accountsBy(ctx, mailServiceAccountServerIndex, key) {
			if !seen[q.NamespacedName] {
				seen[q.NamespacedName] = true
				reqs = append(reqs, q)
			}
		}
	}
	slices.SortFunc(reqs, func(a, b reconcile.Request) int { return strings.Compare(a.String(), b.String()) })
	return reqs
}

// mapClaimants enqueues the other accounts that claim the sender of obj on
// the same server: a conflict is decided by all claimants. For an update the
// handler maps the old object as well, so a sender that was given up reaches
// the account that lost it.
func (r *MailServiceAccountReconciler) mapClaimants(ctx context.Context, obj client.Object) []reconcile.Request {
	a, ok := obj.(*cnmsv1alpha1.MailServiceAccount)
	if !ok {
		return nil
	}
	keys := indexAccountBySender(a)
	if len(keys) == 0 {
		return nil
	}
	self := client.ObjectKeyFromObject(a)
	var reqs []reconcile.Request
	for _, q := range r.accountsBy(ctx, mailServiceAccountSenderIndex, keys[0]) {
		if q.NamespacedName != self {
			reqs = append(reqs, q)
		}
	}
	return reqs
}

// mapServerAccounts enqueues the accounts of a MailServer, and those of the
// servers that present the same hostname: the hostname is unique among the
// competing servers (ADR 0014).
func (r *MailServiceAccountReconciler) mapServerAccounts(ctx context.Context, obj client.Object) []reconcile.Request {
	s, ok := obj.(*cnmsv1alpha1.MailServer)
	if !ok {
		return nil
	}
	keys := []string{s.Namespace + "/" + s.Name}
	if s.Spec.Hostname != "" {
		peers := &cnmsv1alpha1.MailServerList{}
		if err := r.List(ctx, peers, client.MatchingFields{mailServerHostnameIndex: s.Spec.Hostname}); err != nil {
			logf.FromContext(ctx).Error(err, "Could not list MailServers", "hostname", s.Spec.Hostname)
		}
		for i := range peers.Items {
			keys = append(keys, peers.Items[i].Namespace+"/"+peers.Items[i].Name)
		}
	}
	return r.accountsOfServers(ctx, keys...)
}

// mapDomainAccounts enqueues the accounts of the server a MailDomain names,
// and those of the servers named by the MailDomains of other namespaces for
// the same domain: the uniqueness of a domain is judged across namespaces
// (ADR 0003).
func (r *MailServiceAccountReconciler) mapDomainAccounts(ctx context.Context, obj client.Object) []reconcile.Request {
	d, ok := obj.(*cnmsv1alpha1.MailDomain)
	if !ok {
		return nil
	}
	keys := []string{d.Namespace + "/" + d.Spec.ServerRef.Name}
	if d.Spec.Domain != "" {
		peers := &cnmsv1alpha1.MailDomainList{}
		if err := r.List(ctx, peers, client.MatchingFields{mailDomainDomainIndex: d.Spec.Domain}); err != nil {
			logf.FromContext(ctx).Error(err, "Could not list MailDomains", "domain", d.Spec.Domain)
		}
		for i := range peers.Items {
			keys = append(keys, peers.Items[i].Namespace+"/"+peers.Items[i].Spec.ServerRef.Name)
		}
	}
	return r.accountsOfServers(ctx, keys...)
}

// mapUserAccounts enqueues the accounts of the server a MailUser names: the
// addresses of the users rank above the senders.
func (r *MailServiceAccountReconciler) mapUserAccounts(ctx context.Context, obj client.Object) []reconcile.Request {
	u, ok := obj.(*cnmsv1alpha1.MailUser)
	if !ok {
		return nil
	}
	return r.accountsOfServers(ctx, u.Namespace+"/"+u.Spec.ServerRef.Name)
}

// mapGrantAccounts enqueues every account: a grant decides which namespace
// may use a domain, and grants change rarely.
func (r *MailServiceAccountReconciler) mapGrantAccounts(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &cnmsv1alpha1.MailServiceAccountList{}
	if err := r.List(ctx, list); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list MailServiceAccounts")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
