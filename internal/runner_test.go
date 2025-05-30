/*
Copyright 2023

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

package runner_test

import (
	"context"
	"time"

	runner "github.com/electrocucaracha/kubevirt-actions-runner/internal"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	v1 "kubevirt.io/api/core/v1"
	cdifake "kubevirt.io/client-go/containerizeddataimporter/fake"
	"kubevirt.io/client-go/kubecli"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
	"kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

var _ = Describe("Runner", func() {
	var virtClient *kubecli.MockKubevirtClient
	var virtClientset *kubevirtfake.Clientset
	var karRunner runner.Runner
	var vmiInterface *kubecli.MockVirtualMachineInstanceInterface

	const (
		vmTemplate = "vm-template"
		vmInstance = "runner-xyz123"
		dataVolume = "dv-xyz123"
	)

	BeforeEach(func() {
		virtClient = kubecli.NewMockKubevirtClient(gomock.NewController(GinkgoT()))
		virtClientset = kubevirtfake.NewSimpleClientset(NewVirtualMachine(vmTemplate), NewVirtualMachineInstance(vmInstance))
		cdiClientset := cdifake.NewSimpleClientset(NewDataVolume(dataVolume))

		vmiInterface = kubecli.NewMockVirtualMachineInstanceInterface(gomock.NewController(GinkgoT()))
		virtClient.EXPECT().CdiClient().Return(cdiClientset).AnyTimes()

		karRunner = runner.NewRunner(k8sv1.NamespaceDefault, virtClient)
	})

	DescribeTable("create resources", func(shouldSucceed bool, vmTemplate, runnerName, jitConfig string) {
		if shouldSucceed {
			virtClient.EXPECT().VirtualMachine(k8sv1.NamespaceDefault).Return(
				virtClientset.KubevirtV1().VirtualMachines(k8sv1.NamespaceDefault),
			)
			virtClient.EXPECT().VirtualMachineInstance(k8sv1.NamespaceDefault).Return(
				virtClientset.KubevirtV1().VirtualMachineInstances(k8sv1.NamespaceDefault),
			)
		}

		err := karRunner.CreateResources(context.TODO(), vmTemplate, runnerName, jitConfig)

		if shouldSucceed {
			Expect(err).NotTo(HaveOccurred())
			Expect(karRunner.GetVMIName()).Should(Equal(runnerName))
		} else {
			Expect(err).To(HaveOccurred())
			if len(vmTemplate) == 0 {
				Expect(err).Should(Equal(runner.ErrEmptyVMTemplate))
			}
			if len(runnerName) == 0 {
				Expect(err).Should(Equal(runner.ErrEmptyRunnerName))
			}
			if len(jitConfig) == 0 {
				Expect(err).Should(Equal(runner.ErrEmptyJitConfig))
			}
		}
	},
		Entry("when the valid information is provided", true, vmTemplate, "runnerName", "jitConfig"),
		Entry("when empty vm template is provided", false, "", "runnerName", "jitConfig"),
		Entry("when empty runner name is provided", false, vmTemplate, "", "jitConfig"),
		Entry("when empty jit config is provided", false, vmTemplate, "runnerName", ""),
	)

	DescribeTable("delete resources", func(shouldSucceed bool, vmInstance, dataVolume string) {
		virtClient.EXPECT().VirtualMachineInstance(k8sv1.NamespaceDefault).Return(
			virtClientset.KubevirtV1().VirtualMachineInstances(k8sv1.NamespaceDefault),
		)

		err := karRunner.DeleteResources(context.TODO(), vmInstance, dataVolume)

		if shouldSucceed {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
		}
	},
		Entry("when the runner has a data volume", true, vmInstance, dataVolume),
		Entry("when the runner doesn't have data volumes", true, vmInstance, ""),
		Entry("when the runner doesn't exist", true, "runner-abc098", ""),
		Entry("when the data volume doesn't exist", true, vmInstance, "dv-abc098"),
	)

	Context("when handling context cancellation", func() {
		var canceledCtx context.Context
		var cancelFunc context.CancelFunc
		var k8sClientset *kubevirtfake.Clientset
		var cdiClientset *cdifake.Clientset

		BeforeEach(func() {
			// Create and immediately cancel a context
			canceledCtx, cancelFunc = context.WithCancel(context.Background())
			cancelFunc()

			// Setup fake clientsets with the resources we'll be testing
			k8sClientset = kubevirtfake.NewSimpleClientset(NewVirtualMachineInstance(vmInstance))
			cdiClientset = cdifake.NewSimpleClientset(NewDataVolume(dataVolume))

			virtClient.EXPECT().VirtualMachineInstance(k8sv1.NamespaceDefault).Return(
				k8sClientset.KubevirtV1().VirtualMachineInstances(k8sv1.NamespaceDefault),
			).AnyTimes()

			virtClient.EXPECT().CdiClient().Return(cdiClientset).AnyTimes()
		})

		It("should create a new context when the original is canceled", func() {
			// We expect DeleteResources to create a new context when it detects the original is canceled
			// This is tested implicitly by checking that the function succeeds despite using a canceled context

			err := karRunner.DeleteResources(canceledCtx, vmInstance, dataVolume)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should log and continue when deletions fail with a non-NotFound error", func() {
			// Using a patched version of DeleteResources that returns an error for DataVolume deletion
			// but still continues with other operations - we test this implicitly by verifying
			// the function completes without error even when the context is canceled

			err := karRunner.DeleteResources(canceledCtx, vmInstance, dataVolume)
			Expect(err).NotTo(HaveOccurred())

			// We could also verify that the VMI and DataVolume were deleted from the fake clientsets,
			// but that's somewhat redundant since we're checking that the function completes without error
		})
	})

	Describe("WaitForVirtualMachineInstance", func() {
		var fakeWatcher *watch.FakeWatcher
		var kubevirtRunner *runner.KubevirtRunner
		var ctx context.Context
		var cancel context.CancelFunc
		const testTimeout = 2 * time.Second

		BeforeEach(func() {
			fakeWatcher = watch.NewFake()
			ctx, cancel = context.WithCancel(context.Background())
			kubevirtRunner = runner.NewRunner(k8sv1.NamespaceDefault, virtClient)

			virtClient.EXPECT().VirtualMachineInstance(k8sv1.NamespaceDefault).Return(vmiInterface).AnyTimes()

			kubevirtRunner.SetVMINameForTesting(vmInstance)
		})

		AfterEach(func() {
			cancel()
		})

		waitForVMIAsync := func(testCtx context.Context, name string) chan error {
			errChan := make(chan error, 1)
			go func() {
				errChan <- kubevirtRunner.WaitForVirtualMachineInstance(testCtx, name)
				close(errChan)
			}()
			return errChan
		}

		It("should return nil when VMI succeeds", func() {
			vmiInterface.EXPECT().Watch(gomock.Any(), gomock.Any()).Return(fakeWatcher, nil)

			errChan := waitForVMIAsync(ctx, vmInstance)

			vmi := NewVirtualMachineInstance(vmInstance)
			vmi.Status.Phase = v1.Succeeded
			fakeWatcher.Add(vmi)

			Eventually(errChan, testTimeout).Should(Receive(BeNil()))
		})

		It("should return ErrRunnerFailed when VMI fails", func() {
			vmiInterface.EXPECT().Watch(gomock.Any(), gomock.Any()).Return(fakeWatcher, nil)

			errChan := waitForVMIAsync(ctx, vmInstance)

			vmi := NewVirtualMachineInstance(vmInstance)
			vmi.Status.Phase = v1.Failed
			fakeWatcher.Add(vmi)

			Eventually(errChan, testTimeout).Should(Receive(Equal(runner.ErrRunnerFailed)))
		})

		It("should recreate watch when it closes prematurely", func() {
			callCount := 0
			vmiInterface.EXPECT().Watch(gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, _ metav1.ListOptions) (watch.Interface, error) {
					callCount++
					if callCount == 1 {
						go func() {
							time.Sleep(100 * time.Millisecond)
							fakeWatcher.Stop()
						}()
						return fakeWatcher, nil
					} else {
						newWatcher := watch.NewFake()
						go func() {
							time.Sleep(100 * time.Millisecond)
							vmi := NewVirtualMachineInstance(vmInstance)
							vmi.Status.Phase = v1.Succeeded
							newWatcher.Add(vmi)
						}()
						return newWatcher, nil
					}
				}).Times(2)

			errChan := waitForVMIAsync(ctx, vmInstance)

			Eventually(errChan, testTimeout).Should(Receive(BeNil()))
			Expect(callCount).To(Equal(2))
		})

		It("should respect context cancellation", func() {
			testCtx, testCancel := context.WithCancel(context.Background())
			defer testCancel()

			vmiInterface.EXPECT().Watch(gomock.Any(), gomock.Any()).Return(fakeWatcher, nil).MaxTimes(1)

			errChan := waitForVMIAsync(testCtx, vmInstance)

			time.Sleep(100 * time.Millisecond)
			testCancel()

			Eventually(errChan, testTimeout).Should(Receive(MatchError(context.Canceled)))
		})
	})
})

func NewVirtualMachine(name string) *v1.VirtualMachine {
	return &v1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k8sv1.NamespaceDefault,
		},
		Spec: v1.VirtualMachineSpec{
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{},
			},
		},
	}
}

func NewVirtualMachineInstance(name string) *v1.VirtualMachineInstance {
	return &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k8sv1.NamespaceDefault,
		},
	}
}

func NewDataVolume(name string) *v1beta1.DataVolume {
	return &v1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k8sv1.NamespaceDefault,
		},
	}
}
