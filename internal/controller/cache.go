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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// CacheOptions limits the manager cache to what the operator needs in
// memory: the ConfigMaps and Jobs it creates (they carry the managed-by
// label). Tenant ConfigMaps and Jobs are never cached, so a cached read
// cannot see them: code that must notice a foreign object of the same
// name (createOwned) reads through the API reader.
func CacheOptions() cache.Options {
	managed := cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{manifests.ManagedByLabel: manifests.ManagedByValue}),
	}
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&corev1.ConfigMap{}: managed,
		&batchv1.Job{}:      managed,
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
