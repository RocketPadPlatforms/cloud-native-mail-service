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
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// CacheOptions limits the manager cache to what the operator needs in
// memory: the objects it creates (they carry the managed-by label, also
// on the volume claim templates, so the claims of the StatefulSets
// match). Tenant objects of these kinds are never cached, so a cached
// read cannot see them: code that must notice a foreign object of the
// same name (createOwned, applyObject) reads through the API reader.
//
// shortcut: the optional cert-manager Certificate and Prometheus
// ServiceMonitor informers stay unfiltered, because a ByObject entry for
// a kind whose CRD is missing fails the manager setup; filter them when
// the CRD presence is known before the manager is built.
func CacheOptions() cache.Options {
	managed := cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{manifests.ManagedByLabel: manifests.ManagedByValue}),
	}
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&corev1.ConfigMap{}:             managed,
		&corev1.Service{}:               managed,
		&corev1.PersistentVolumeClaim{}: managed,
		&batchv1.Job{}:                  managed,
		&appsv1.StatefulSet{}:           managed,
		&appsv1.Deployment{}:            managed,
		&policyv1.PodDisruptionBudget{}: managed,
		&networkingv1.NetworkPolicy{}:   managed,
	}}
}

// ClientOptions keeps Secrets out of the cache altogether: the TLS and
// users Secrets belong to tenants, and the Secrets the operator creates
// are read rarely. Reads of Secrets go straight to the API server, so
// no informer holds their contents.
func ClientOptions() client.Options {
	return client.Options{Cache: &client.CacheOptions{
		DisableFor: []client.Object{&corev1.Secret{}},
	}}
}
