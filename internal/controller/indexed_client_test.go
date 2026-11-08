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
	"slices"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// indexedClient stands in for the client of a manager, whose cache answers a
// List with a field selector from the indexes of IndexFields: the client of
// the envtest suite talks to the API server, which does not know the fields.
// It lists the MailServiceAccounts, keeps those whose index values contain the
// value, and records each index lookup it served.
type indexedClient struct {
	client.Client
	served *indexLookups
}

type indexLookups struct {
	mu   sync.Mutex
	list []string
}

func (l *indexLookups) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, s)
}

func (l *indexLookups) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.list)
}

func newIndexedClient(c client.Client) indexedClient {
	return indexedClient{Client: c, served: &indexLookups{}}
}

func (c indexedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	accounts, ok := list.(*cnmsv1alpha1.MailServiceAccountList)
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	if !ok || lo.FieldSelector == nil {
		return c.Client.List(ctx, list, opts...)
	}
	extract := map[string]client.IndexerFunc{
		mailServiceAccountServerIndex: indexAccountByServer,
		mailServiceAccountSenderIndex: indexAccountBySender,
	}
	all := &cnmsv1alpha1.MailServiceAccountList{}
	if err := c.Client.List(ctx, all, &client.ListOptions{Namespace: lo.Namespace, LabelSelector: lo.LabelSelector}); err != nil {
		return err
	}
	accounts.Items = nil
	for _, req := range lo.FieldSelector.Requirements() {
		c.served.add(req.Field + "=" + req.Value)
	}
	for i := range all.Items {
		keep := true
		for _, req := range lo.FieldSelector.Requirements() {
			if !slices.Contains(extract[req.Field](&all.Items[i]), req.Value) {
				keep = false
			}
		}
		if keep {
			accounts.Items = append(accounts.Items, all.Items[i])
		}
	}
	return nil
}
