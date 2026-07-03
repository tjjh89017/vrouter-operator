/*
Copyright 2026.

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

package v1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vrouterv1 "github.com/tjjh89017/vrouter-operator/api/v1"
)

var _ = Describe("ProxmoxCluster Webhook", func() {
	var (
		obj       *vrouterv1.ProxmoxCluster
		oldObj    *vrouterv1.ProxmoxCluster
		validator ProxmoxClusterCustomValidator
		defaulter ProxmoxClusterCustomDefaulter
	)

	// validSpec returns a ProxmoxClusterSpec that satisfies every field the
	// validating webhook checks.
	validSpec := func() vrouterv1.ProxmoxClusterSpec {
		return vrouterv1.ProxmoxClusterSpec{
			Endpoints: []string{"https://pve.example.com:8006"},
			CredentialsRef: vrouterv1.SecretReference{
				Name: "pve-credentials",
			},
			SyncInterval: metav1.Duration{Duration: 60 * time.Second},
		}
	}

	BeforeEach(func() {
		obj = &vrouterv1.ProxmoxCluster{Spec: validSpec()}
		oldObj = &vrouterv1.ProxmoxCluster{Spec: validSpec()}
		validator = ProxmoxClusterCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = ProxmoxClusterCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating or updating ProxmoxCluster under Validating Webhook", func() {
		It("Should admit a fully valid spec on create", func() {
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should admit a fully valid spec on update", func() {
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should deny creation when spec.endpoints is empty", func() {
			obj.Spec.Endpoints = []string{}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.endpoints"))
		})

		It("Should deny update when spec.endpoints is empty", func() {
			obj.Spec.Endpoints = []string{}
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.endpoints"))
		})

		It("Should deny creation when an endpoint entry is blank", func() {
			obj.Spec.Endpoints = []string{""}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.endpoints[0]"))
		})

		It("Should deny creation when spec.syncInterval is zero", func() {
			obj.Spec.SyncInterval = metav1.Duration{}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.syncInterval"))
		})

		It("Should deny creation when spec.syncInterval is negative", func() {
			obj.Spec.SyncInterval = metav1.Duration{Duration: -60 * time.Second}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.syncInterval"))
		})

		It("Should deny update when spec.syncInterval is negative", func() {
			obj.Spec.SyncInterval = metav1.Duration{Duration: -60 * time.Second}
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.syncInterval"))
		})

		It("Should admit a positive spec.syncInterval", func() {
			obj.Spec.SyncInterval = metav1.Duration{Duration: 30 * time.Second}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should deny creation when spec.credentialsRef.name is empty", func() {
			obj.Spec.CredentialsRef.Name = ""
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.credentialsRef.name"))
		})

		It("Should deny update when spec.credentialsRef.name is empty", func() {
			obj.Spec.CredentialsRef.Name = ""
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.credentialsRef.name"))
		})

		It("Should skip validation on update when the object is being deleted", func() {
			now := metav1.Now()
			obj.DeletionTimestamp = &now
			obj.Finalizers = []string{"example.com/finalizer"}
			obj.Spec.Endpoints = []string{}
			obj.Spec.SyncInterval = metav1.Duration{Duration: -60 * time.Second}
			obj.Spec.CredentialsRef.Name = ""

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
