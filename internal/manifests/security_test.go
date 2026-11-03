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
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// podCase is one pod template of the data plane, workloads and
// validation Jobs alike.
type podCase struct {
	// name is the workload name, or the name of the component the
	// validation Job checks.
	name       string
	validation bool
	pod        corev1.PodSpec
}

// allPods collects the pod specs of a built data plane.
func allPods(t *testing.T, mailboxes bool) []podCase {
	t.Helper()
	dp := buildPlane(t, func(s *cnmsv1alpha1.MailServer) {
		s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(mailboxes)}
	}, nil)
	var pods []podCase
	for _, o := range dp.Workloads {
		switch w := o.(type) {
		case *appsv1.StatefulSet:
			pods = append(pods, podCase{name: w.Name, pod: w.Spec.Template.Spec})
		case *appsv1.Deployment:
			pods = append(pods, podCase{name: w.Name, pod: w.Spec.Template.Spec})
		}
	}
	for _, v := range dp.Validations {
		pods = append(pods, podCase{name: v.Workload, validation: true, pod: v.Job.Spec.Template.Spec})
	}
	return pods
}

// baselineCaps are the capabilities the Pod Security "baseline"
// profile allows to be added.
var baselineCaps = []corev1.Capability{
	"AUDIT_WRITE", capChown, capDacOverride, capFowner, "FSETID", capKill, "MKNOD",
	capNetBindService, "SETFCAP", capSetgid, "SETPCAP", capSetuid, capSysChroot,
}

// wantCaps are the capabilities each container may add; a container
// that is not listed adds none.
func wantCaps(pc podCase, container string) []corev1.Capability {
	switch {
	case pc.validation && pc.name == testName+"-postfix":
		return postfixValidateCaps
	case pc.validation:
		return nil
	case container == postfixContainerName:
		return postfixMainCaps
	case container == certReloadContainerName && pc.name == testName+"-postfix":
		return postfixReloadCaps
	case container == dovecotContainerName:
		return []corev1.Capability{capSysChroot}
	}
	return nil
}

// Every container drops all capabilities, forbids privilege escalation
// (except the Dovecot mailbox container, see dovecotSecurity), has a
// read-only root filesystem, and only the documented containers add
// capabilities or run as root; the pods use the default seccomp
// profile. Everything added is allowed by the Pod Security baseline.
func TestContainerSecurityContexts(t *testing.T) {
	rootAllowed := map[string]bool{
		testName + "-postfix/postfix":     true,
		testName + "-postfix/cert-reload": true,
		testName + "-postfix/validate":    true,
	}
	for _, mailboxes := range []bool{false, true} {
		for _, pc := range allPods(t, mailboxes) {
			if sp := pc.pod.SecurityContext; sp == nil || sp.SeccompProfile == nil || sp.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
				t.Errorf("mailboxes=%v %s: pod seccomp profile must be RuntimeDefault", mailboxes, pc.name)
			}
			for _, c := range slices.Concat(pc.pod.InitContainers, pc.pod.Containers) {
				id := pc.name + "/" + c.Name
				if pc.validation {
					id = pc.name + "/validate"
				}
				sc := c.SecurityContext
				if sc == nil || sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
					t.Errorf("mailboxes=%v %s: must drop ALL capabilities: %+v", mailboxes, id, sc)
					continue
				}
				if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
					t.Errorf("mailboxes=%v %s: root filesystem must be read-only", mailboxes, id)
				}
				wantEscalation := !pc.validation && c.Name == dovecotContainerName && mailboxes
				if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation != wantEscalation {
					t.Errorf("mailboxes=%v %s: allowPrivilegeEscalation = %v, want %v", mailboxes, id, sc.AllowPrivilegeEscalation, wantEscalation)
				}
				if want := wantCaps(pc, c.Name); !slices.Equal(sc.Capabilities.Add, want) {
					t.Errorf("mailboxes=%v %s: added capabilities = %v, want %v", mailboxes, id, sc.Capabilities.Add, want)
				}
				for _, a := range sc.Capabilities.Add {
					if !slices.Contains(baselineCaps, a) {
						t.Errorf("%s adds %s, which the baseline profile does not allow", id, a)
					}
				}
				nonRoot := sc.RunAsNonRoot != nil && *sc.RunAsNonRoot && sc.RunAsUser != nil && *sc.RunAsUser != 0
				root := sc.RunAsUser != nil && *sc.RunAsUser == 0
				isRootAllowed := rootAllowed[id]
				switch {
				case isRootAllowed && !root:
					t.Errorf("%s must run as root explicitly", id)
				case !isRootAllowed && !nonRoot:
					t.Errorf("mailboxes=%v %s must run as a non-root user: %+v", mailboxes, id, sc)
				}
			}
		}
	}
}

// Without the exceptions (the Postfix pod and its validation Job, the
// Dovecot pod) the pods satisfy the Pod Security "restricted" profile:
// non-root, no escalation, drop ALL and no added capability.
func TestPodsWithoutExceptionsAreRestricted(t *testing.T) {
	for _, pc := range allPods(t, true) {
		// The validation Jobs of Rspamd, Dovecot and Valkey qualify too.
		postfixPod := pc.name == testName+"-postfix"
		dovecotWorkload := pc.name == testName+"-dovecot" && !pc.validation
		if postfixPod || dovecotWorkload {
			continue
		}
		for _, c := range slices.Concat(pc.pod.InitContainers, pc.pod.Containers) {
			sc := c.SecurityContext
			if len(sc.Capabilities.Add) != 0 || !*sc.RunAsNonRoot || *sc.AllowPrivilegeEscalation {
				t.Errorf("%s/%s does not satisfy the restricted profile: %+v", pc.name, c.Name, sc)
			}
		}
	}
}

// Every writable path of the read-only containers is a volume: the
// emptyDirs the tests of the images showed to be needed.
func TestReadOnlyRootNeedsScratchVolumes(t *testing.T) {
	type mount struct{ container, path string }
	want := []mount{
		{postfixContainerName, postfixDataDir},
		{queueMetricsContainerName, tmpDir},
		{dovecotContainerName, tmpDir},
		{dovecotContainerName, dovecotRunDir},
		{rspamdMetricsContainer, tmpDir},
		{"rspamd", rspamdStateDir},
		{"valkey", "/data"},
	}
	for _, pc := range allPods(t, true) {
		if pc.validation {
			continue
		}
		have := map[mount]bool{}
		for _, c := range pc.pod.Containers {
			for _, m := range c.VolumeMounts {
				have[mount{c.Name, m.MountPath}] = true
			}
		}
		for _, w := range want {
			exists := slices.ContainsFunc(pc.pod.Containers, func(c corev1.Container) bool { return c.Name == w.container })
			if exists && !have[w] {
				t.Errorf("%s: container %s misses the writable mount %s", pc.name, w.container, w.path)
			}
		}
	}
}
