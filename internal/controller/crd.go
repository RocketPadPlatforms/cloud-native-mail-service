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
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Discovery of optional integrations (principle 10): the cert-manager,
// Prometheus Operator and external-dns CRDs are never required.

// crdCacheTTL is how long CRD-availability results are cached, so
// integrations installed later are picked up without a restart
// (principle 10).
const crdCacheTTL = 5 * time.Minute

// apiReadTimeout bounds the reads that bypass the cache (CRDs), so a
// slow API server cannot stall a reconcile or the startup.
const apiReadTimeout = 10 * time.Second

// crdCache caches whether CRDs are installed, per CRD name.
type crdCache struct {
	mu      sync.Mutex
	results map[string]crdResult
}

type crdResult struct {
	checkedAt time.Time
	installed bool
}

// installed reports whether the CRD with the given name is installed,
// cached for crdCacheTTL. The CRDs are read directly with a timeout:
// the informer cache would watch every CRD of the cluster for a few
// lookups every few minutes. A failed read is returned and not cached.
func (c *crdCache) installed(ctx context.Context, reader client.Reader, name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if res, ok := c.results[name]; ok && time.Since(res.checkedAt) < crdCacheTTL {
		return res.installed, nil
	}
	ctx, cancel := context.WithTimeout(ctx, apiReadTimeout)
	defer cancel()
	err := reader.Get(ctx, client.ObjectKey{Name: name}, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	}
	if c.results == nil {
		c.results = map[string]crdResult{}
	}
	c.results[name] = crdResult{checkedAt: time.Now(), installed: err == nil}
	return err == nil, nil
}

// crdInstalled reports whether the CRD with the given name is installed
// (direct API read; the manager cache has not started yet at setup
// time). On an error other than NotFound the watch is skipped so that
// a transient API failure can never crash the operator (principle 10).
func crdInstalled(mgr ctrl.Manager, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), apiReadTimeout)
	defer cancel()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := mgr.GetAPIReader().Get(ctx, client.ObjectKey{Name: name}, crd)
	if err == nil {
		return true
	}
	if !errors.IsNotFound(err) {
		log.Log.Info("Could not check the CRD at startup, skipping the optional watch",
			"crd", name, "error", err.Error())
	}
	return false
}
