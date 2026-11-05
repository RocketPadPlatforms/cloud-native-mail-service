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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
)

// The StatefulSet volume claim templates cannot change and a removed
// Postfix replica would strand its queue, so the API rejects both
// (ADR 0010).
var _ = Describe("MailServer storage and scale-down validation", func() {
	var ns string
	serverKey := func(ns string) types.NamespacedName {
		return types.NamespacedName{Name: testServerName, Namespace: ns}
	}
	// update creates a server with the given initial spec mutation,
	// then applies the change and returns the update error.
	update := func(initial, change func(*cnmsv1alpha1.MailServer)) error {
		ns, _ = grantFixture()
		createServerG(ns, testServerName, initial)
		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, serverKey(ns), server)).To(Succeed())
		change(server)
		return k8sClient.Update(ctx, server)
	}
	postfixSpec := func(replicas int32) func(*cnmsv1alpha1.MailServer) {
		return func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Postfix = &cnmsv1alpha1.Postfix{Replicas: &replicas}
		}
	}

	It("rejects decreasing postfix.replicas", func() {
		err := update(postfixSpec(3), postfixSpec(2))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decreasing postfix.replicas is not supported yet"))
	})

	It("rejects a decrease below the defaulted replicas", func() {
		err := update(nil, postfixSpec(1))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decreasing postfix.replicas is not supported yet"))
	})

	It("rejects removing the postfix block of a scaled-up server", func() {
		err := update(postfixSpec(3), func(s *cnmsv1alpha1.MailServer) { s.Spec.Postfix = nil })
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decreasing postfix.replicas is not supported yet"))
	})

	It("accepts keeping and increasing postfix.replicas", func() {
		Expect(update(postfixSpec(3), postfixSpec(3))).To(Succeed())
		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, serverKey(ns), server)).To(Succeed())
		server.Spec.Postfix.Replicas = new(int32(4))
		Expect(k8sClient.Update(ctx, server)).To(Succeed())
	})

	It("accepts the defaulted replicas being set explicitly", func() {
		Expect(update(nil, postfixSpec(2))).To(Succeed())
	})

	It("rejects changing postfix.queueStorage", func() {
		err := update(nil, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Postfix = &cnmsv1alpha1.Postfix{QueueStorage: new(resource.MustParse("1Gi"))}
		})
		Expect(err).ToNot(HaveOccurred())

		server := &cnmsv1alpha1.MailServer{}
		Expect(k8sClient.Get(ctx, serverKey(ns), server)).To(Succeed())
		server.Spec.Postfix.QueueStorage = new(resource.MustParse("5Gi"))
		err = k8sClient.Update(ctx, server)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("queueStorage is immutable"))
	})

	It("rejects changing mailboxes.storage", func() {
		err := update(func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes = &cnmsv1alpha1.Mailboxes{Enabled: new(true)}
		}, func(s *cnmsv1alpha1.MailServer) {
			s.Spec.Mailboxes.Storage = new(resource.MustParse("20Gi"))
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("storage is immutable"))
	})
})
