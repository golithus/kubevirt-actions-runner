/*
Copyright © 2024

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

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/spf13/pflag"
	k8scorev1 "k8s.io/api/core/v1"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "kubevirt.io/api/core/v1"
	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const (
	runnerInfoAnnotation string = "electrocucaracha.kubevirt-actions-runner/runner-info"
	runnerInfoVolume     string = "runner-info"
	runnerInfoPath       string = "runner-info.json"
)

type Runner struct {
	virtClient             kubecli.KubevirtClient
	cdiClient              cdiclient.Interface
	namespace              string
	dataVolume             string
	virtualMachineInstance string
}

func (rc *Runner) getResources(ctx context.Context, vmTemplate, runnerName, jitConfig string) (
	*v1.VirtualMachineInstance, *v1beta1.DataVolume,
) {
	virtualMachine, err := rc.virtClient.VirtualMachine(rc.namespace).Get(
		ctx, vmTemplate, k8smetav1.GetOptions{})
	if err != nil {
		log.Fatalf("cannot obtain KubeVirt vm list: %v\n", err)
	}

	virtualMachineInstance := v1.NewVMIReferenceFromNameWithNS(rc.namespace, runnerName)
	virtualMachineInstance.Spec = virtualMachine.Spec.Template.Spec

	if virtualMachineInstance.Annotations == nil {
		virtualMachineInstance.Annotations = make(map[string]string)
	}

	jri := map[string]interface{}{
		"jitconfig": jitConfig,
	}

	out, err := json.Marshal(jri)
	if err != nil {
		log.Fatalf("cannot marshal jitConfig: %v\n", err)
	}

	virtualMachineInstance.Annotations[runnerInfoAnnotation] = string(out)

	var dataVolume *v1beta1.DataVolume

	for _, dvt := range virtualMachine.Spec.DataVolumeTemplates {
		for _, volume := range virtualMachineInstance.Spec.Volumes {
			if volume.DataVolume != nil && volume.DataVolume.Name == dvt.Name {
				dataVolume = &v1beta1.DataVolume{}
				dataVolume.Name = fmt.Sprintf("%s-%s", dvt.Name, runnerName)
				dataVolume.Spec = dvt.Spec

				volume.DataVolume.Name = dataVolume.Name

				break
			}
		}
	}

	virtualMachineInstance.Spec.Volumes = append(virtualMachineInstance.Spec.Volumes, v1.Volume{
		Name: runnerInfoVolume,
		VolumeSource: v1.VolumeSource{
			DownwardAPI: &v1.DownwardAPIVolumeSource{
				Fields: []k8scorev1.DownwardAPIVolumeFile{
					{
						Path: runnerInfoPath,
						FieldRef: &k8scorev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", runnerInfoAnnotation),
						},
					},
				},
			},
		},
	})

	return virtualMachineInstance, dataVolume
}

func (rc *Runner) CreateResources(ctx context.Context,
	vmTemplate, runnerName, jitConfig string,
) {
	virtualMachineInstance, dataVolume := rc.getResources(ctx, vmTemplate, runnerName, jitConfig)

	log.Printf("Creating %s Data Volume\n", dataVolume.Name)

	if _, err := rc.cdiClient.CdiV1beta1().DataVolumes(
		rc.namespace).Create(ctx, dataVolume, k8smetav1.CreateOptions{}); err != nil {
		log.Fatalf("cannot create data volume: %v\n", err)
	}

	rc.dataVolume = dataVolume.Name

	log.Printf("Creating %s Virtual Machine Instance\n", virtualMachineInstance.Name)

	if _, err := rc.virtClient.VirtualMachineInstance(rc.namespace).Create(ctx,
		virtualMachineInstance, k8smetav1.CreateOptions{}); err != nil {
		log.Fatal(err.Error())
	}

	rc.virtualMachineInstance = virtualMachineInstance.Name
}

func (rc *Runner) WaitForVirtualMachineInstance(ctx context.Context) {
	log.Printf("Watching %s Virtual Machine Instance\n", rc.virtualMachineInstance)

	watch, err := rc.virtClient.VirtualMachineInstance(rc.namespace).Watch(ctx, k8smetav1.ListOptions{})
	if err != nil {
		log.Fatalf("Failed to watch Virtual Machine Instance: %v", err)
	}
	defer watch.Stop()

	lastPhase := ""

	for event := range watch.ResultChan() {
		vmi, ok := event.Object.(*v1.VirtualMachineInstance)
		if ok && vmi.Name == rc.virtualMachineInstance && vmi.Status.Phase != v1.VirtualMachineInstancePhase(lastPhase) {
			lastPhase = string(vmi.Status.Phase)
			log.Printf("%s has transitioned to %s phase \n", rc.virtualMachineInstance, lastPhase)

			switch lastPhase {
			case "Succeeded", "Failed":
				return
			}
		}
	}
}

func (rc *Runner) DeleteResources(ctx context.Context) {
	log.Printf("Cleaning %s Virtual Machine Instance resources\n",
		rc.virtualMachineInstance)

	if err := rc.virtClient.VirtualMachineInstance(rc.namespace).Delete(
		ctx, rc.virtualMachineInstance, k8smetav1.DeleteOptions{}); err != nil {
		log.Fatal(err.Error())
	}

	if len(rc.dataVolume) > 0 {
		if err := rc.cdiClient.CdiV1beta1().DataVolumes(rc.namespace).Delete(ctx, rc.dataVolume,
			k8smetav1.DeleteOptions{}); err != nil {
			log.Fatal(err.Error())
		}
	}
}

func NewRunner() *Runner {
	var err error

	clientConfig := kubecli.DefaultClientConfig(&pflag.FlagSet{})

	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		log.Fatalf("error in namespace : %v\n", err)
	}

	virtClient, err := kubecli.GetKubevirtClientFromClientConfig(clientConfig)
	if err != nil {
		log.Fatalf("cannot obtain KubeVirt client: %v\n", err)
	}

	return &Runner{
		namespace:  namespace,
		virtClient: virtClient,
		cdiClient:  virtClient.CdiClient(),
	}
}
