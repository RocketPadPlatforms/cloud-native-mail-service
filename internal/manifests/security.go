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

package manifests

import (
	corev1 "k8s.io/api/core/v1"
)

// Security contexts of the data plane (ADR 0006). Every container
// drops all capabilities (those it needs are added back) and has a
// read-only root filesystem; all but the Dovecot container of a pod
// with mailboxes forbid privilege escalation (see dovecotSecurity).
// The pods use the runtime's default seccomp profile. Writable paths
// are emptyDir mounts.
//
// Rspamd, Valkey, the exporters, the sidecars of the Rspamd and
// Valkey pods and their validation Jobs run as an unprivileged user
// and satisfy the Pod Security "restricted" profile. Postfix and
// Dovecot cannot (the exceptions below), so a namespace that runs a
// MailServer has to allow the "baseline" profile; every capability
// they add is in the baseline set.
//
// Postfix runs its master as root: the master binds ports 25 and 587,
// chowns the queue, drops privileges to the postfix user for every
// daemon (setuid/setgid), and signals those daemons (kill). Each
// capability was found by running the pinned image with `--cap-drop
// ALL` and removing the added capabilities one at a time; the start
// fails (or, for KILL, the master could not stop a service whose
// children it cannot signal) without any of them.
const (
	capChown          corev1.Capability = "CHOWN"
	capDacOverride    corev1.Capability = "DAC_OVERRIDE"
	capFowner         corev1.Capability = "FOWNER"
	capSetuid         corev1.Capability = "SETUID"
	capSetgid         corev1.Capability = "SETGID"
	capKill           corev1.Capability = "KILL"
	capNetBindService corev1.Capability = "NET_BIND_SERVICE"
	capSysChroot      corev1.Capability = "SYS_CHROOT"
)

var (
	// postfixMainCaps are the capabilities of the Postfix main container.
	postfixMainCaps = []corev1.Capability{
		capChown,          // queue directory ownership at start
		capDacOverride,    // root reads and writes the postfix-owned queue (mode 0700)
		capFowner,         // chmod of queue directories it does not own
		capSetuid,         // drop to the postfix user ...
		capSetgid,         // ... and the postdrop group
		capKill,           // the root master signals the postfix-owned daemons
		capNetBindService, // the master binds ports 25 and 587 as root
	}
	// postfixValidateCaps are those of `postfix check`: it creates and
	// chowns the queue directories and runs postsuper as postfix, but
	// binds and signals nothing.
	postfixValidateCaps = []corev1.Capability{capChown, capDacOverride, capFowner, capSetuid, capSetgid}
	// postfixReloadCaps are those of `postfix reload` in the cert-reload
	// sidecar: it signals the root-owned master as root (same user, no
	// capability) and runs postsuper as the postfix user.
	postfixReloadCaps = []corev1.Capability{capSetuid, capSetgid}
)

// podSeccomp is the pod-level seccomp profile of every data-plane pod.
func podSeccomp() *corev1.SeccompProfile {
	return &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
}

// containerSecurity is the common floor: no escalation, no
// capabilities except add, read-only root filesystem.
func containerSecurity(add ...corev1.Capability) *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: new(false),
		ReadOnlyRootFilesystem:   new(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: add},
	}
}

// nonRootSecurity is containerSecurity for an unprivileged user.
func nonRootSecurity(uid, gid int64, add ...corev1.Capability) *corev1.SecurityContext {
	sc := containerSecurity(add...)
	sc.RunAsNonRoot = new(true)
	sc.RunAsUser = new(uid)
	sc.RunAsGroup = new(gid)
	return sc
}

// rootSecurity is containerSecurity for a container that runs as root
// (Postfix, see above). The explicit user makes the exception visible.
func rootSecurity(add ...corev1.Capability) *corev1.SecurityContext {
	sc := containerSecurity(add...)
	sc.RunAsUser = new(int64(0))
	return sc
}

// tmpMount mounts the shared scratch emptyDir at /tmp.
func tmpMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: tmpVolumeName, MountPath: tmpDir}
}

// emptyDirVolume is a node-local scratch volume.
func emptyDirVolume(name string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
}

// dovecotSecurity is the Dovecot main container: the rootless image
// runs as vmail and gets its one privilege, chroot, from file
// capabilities (cap_sys_chroot on anvil and the login processes), so
// SYS_CHROOT has to stay in the bounding set. Verified with
// dovecot/dovecot:2.4.5: without it the anvil service does not start.
// The kernel ignores file capabilities under no_new_privs, which is
// what allowPrivilegeEscalation=false sets: the auth-only pod (no
// login processes) works with it, but with mailboxes imap-login dies
// with "chroot(/var/run/dovecot/login) failed: Operation not
// permitted". So the mailbox pod must allow escalation; the capability
// set stays {SYS_CHROOT} and every other capability is dropped.
func dovecotSecurity(mailboxes bool) *corev1.SecurityContext {
	sc := nonRootSecurity(dovecotVmailID, dovecotVmailID, capSysChroot)
	sc.AllowPrivilegeEscalation = new(mailboxes)
	return sc
}
