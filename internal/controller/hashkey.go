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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/manifests"
)

// kindSecret is the kind in an ownershipConflictError for a Secret.
const kindSecret = "Secret"

// ownerMailServer names the owner kind in an ownershipConflictError.
const ownerMailServer = "MailServer"

// hashKeyLength is the size of the random hash key in bytes.
const hashKeyLength = 32

// hashKey returns the key that the digests of the secret files are
// computed under (ADR 0015, section 6): a plain hash of a rendered
// Secret that holds a bind password would let anyone who can read the
// pod template confirm a guess of that password offline. The key lives
// in an immutable Secret of the MailServer, created once with random
// bytes. A Secret of that name that the server does not control is a
// conflict, not a key: a tenant could pre-create one with a known value.
func (r *MailServerReconciler) hashKey(ctx context.Context, server *cnmsv1alpha1.MailServer) ([]byte, error) {
	name := manifests.HashKeySecretName(server)
	read := func() (*corev1.Secret, error) {
		s := &corev1.Secret{}
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: server.Namespace, Name: name}, s)
		return s, err
	}
	s, err := read()
	if errors.IsNotFound(err) {
		key := make([]byte, hashKeyLength)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := createOwned(ctx, r.Client, r.APIReader, r.Scheme, server, manifests.HashKeySecret(server, key)); err != nil {
			return nil, err
		}
		// Read what was stored: a concurrent reconcile may have won.
		s, err = read()
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(s, server) {
		return nil, &ownershipConflictError{kind: kindSecret, name: name, owner: ownerMailServer}
	}
	key := s.Data[manifests.HashKeyData]
	if len(key) < hashKeyLength {
		return nil, fmt.Errorf("the Secret %s holds no hash key of %d bytes under %q; delete it to have a new one created",
			name, hashKeyLength, manifests.HashKeyData)
	}
	return key, nil
}
