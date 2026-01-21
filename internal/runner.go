// Package runner provides the core logic for managing KubeVirt VirtualMachineInstances (VMIs)
// that function as ephemeral GitHub Actions runners. It handles the lifecycle of these runners,
// including their creation from templates, monitoring their execution, and cleaning them up.
// This package is the engine that interacts with the KubeVirt API to provide
// VM-based runner capabilities to the broader kubevirt-actions-runner application.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/pkg/errors"
	k8scorev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const (
	runnerInfoAnnotation string = "electrocucaracha.kubevirt-actions-runner/runner-info"
	runnerInfoVolume     string = "runner-info"
	runnerInfoPath       string = "runner-info.json"
)

// Runner defines the interface for managing a virtual machine-based runner.
// It abstracts the operations required to create, monitor, and delete
// the underlying compute resources for a job.
type Runner interface {
	CreateResources(ctx context.Context, vmTemplate string, runnerName string, jitConfig string) error
	WaitForVirtualMachineInstance(ctx context.Context, vmi string) error
	DeleteResources(ctx context.Context, vmi string, dv string) error
	GetVMIName() string
	GetDataVolumeName() string
}

// KubevirtRunner is an implementation of the Runner interface that uses KubeVirt
// to create and manage VirtualMachineInstances (VMIs) as GitHub Actions runners.
//
// How it fits in the application:
// The kubevirt-actions-runner application is designed to be a runner for GitHub Actions
// that executes jobs within ephemeral KubeVirt VMs. The KubevirtRunner is the central
// component responsible for all interactions with the KubeVirt API.
//
// Workflow:
//  1. Initialization: An instance is created via NewRunner, typically by the main application
//     logic in the `cmd/` directory, providing it with a KubeVirt client and namespace.
//  2. Resource Creation (`CreateResources`):
//     - Takes a VM template name, a unique runner name, and GitHub Actions JIT (Just-In-Time)
//     configuration.
//     - Fetches the specified KubeVirt VirtualMachine (VM) resource to use as a base template.
//     - Constructs a new VirtualMachineInstance (VMI) specification.
//     - Injects the JIT configuration into the VMI's annotations. This JIT config is then
//     made available to the VMI's guest OS via the Downward API (using `generateRunnerInfoVolume`),
//     allowing the runner agent inside the VM to register with GitHub.
//     - Handles associated DataVolumes if defined in the VM template.
//     - Creates the VMI and any DataVolumes using the KubeVirt client.
//  3. Monitoring (`WaitForVirtualMachineInstance`):
//     - After creating the VMI, the application calls this method to watch the VMI's status.
//     - It blocks until the VMI reaches a terminal state (Succeeded or Failed), indicating
//     the completion or failure of the GitHub Actions job.
//  4. Resource Deletion (`DeleteResources`):
//     - Once the job is finished (or if the application is interrupted), this method is called
//     to clean up by deleting the VMI and any associated DataVolumes.
//
// This structure allows the main application to orchestrate the runner lifecycle without
// needing to know the specifics of KubeVirt API interactions, which are encapsulated here.
type KubevirtRunner struct {
	virtClient             kubecli.KubevirtClient
	namespace              string
	dataVolume             string
	virtualMachineInstance string
	currentStatus          v1.VirtualMachineInstancePhase
	persistentVolumeClaim  string
}

var _ Runner = (*KubevirtRunner)(nil)

// GetVMIName returns the name of the VirtualMachineInstance managed by this runner.
func (rc *KubevirtRunner) GetVMIName() string {
	return rc.virtualMachineInstance
}

// GetDataVolumeName returns the name of the DataVolume associated with this runner, if any.
func (rc *KubevirtRunner) GetDataVolumeName() string {
	return rc.dataVolume
}

// getResources prepares the VirtualMachineInstance and DataVolume specifications
// based on a template and runtime parameters. It fetches a base VirtualMachine template,
// customizes it with the runnerName and jitConfig (by adding it to annotations),
// and prepares any DataVolume definitions.
func (rc *KubevirtRunner) getResources(ctx context.Context, vmTemplate, runnerName, jitConfig string) (
	*v1.VirtualMachineInstance, *v1beta1.DataVolume, error,
) {
	virtualMachine, err := rc.virtClient.VirtualMachine(rc.namespace).Get(
		ctx, vmTemplate, k8smetav1.GetOptions{})
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot obtain KubeVirt vm list")
	}

	virtualMachineInstance := v1.NewVMIReferenceFromNameWithNS(rc.namespace, runnerName)
	virtualMachineInstance.Spec = virtualMachine.Spec.Template.Spec

	if virtualMachineInstance.Annotations == nil {
		virtualMachineInstance.Annotations = make(map[string]string)
	}

	// Embed the JIT configuration into an annotation. This will be passed to the VM
	// via the Downward API (see generateRunnerInfoVolume) so the runner agent inside
	// the VM can configure itself.
	jri := map[string]interface{}{
		"jitconfig": jitConfig,
	}

	out, err := json.Marshal(jri)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot marshal jitConfig")
	}

	virtualMachineInstance.Annotations[runnerInfoAnnotation] = string(out)

	var dataVolume *v1beta1.DataVolume

	// If the VM template includes DataVolumeTemplates, create corresponding DataVolume
	// resources for the new VMI.
	for _, dvt := range virtualMachine.Spec.DataVolumeTemplates {
		for _, volume := range virtualMachineInstance.Spec.Volumes {
			if volume.DataVolume != nil && volume.DataVolume.Name == dvt.Name {
				dataVolume = &v1beta1.DataVolume{
					ObjectMeta: k8smetav1.ObjectMeta{
						Name: fmt.Sprintf("%s-%s", dvt.Name, runnerName), // Unique name for the DV instance
					},
					Spec: dvt.Spec,
				}

				// If source is a VolumeSnapshot, use its restoreSize for exact size matching.
				// This enables fast snapshot cloning in storage backends like Mayastor.
				if dvt.Spec.Source != nil && dvt.Spec.Source.Snapshot != nil {
					snapshotSource := dvt.Spec.Source.Snapshot
					snapshotNamespace := snapshotSource.Namespace

					if snapshotNamespace == "" {
						snapshotNamespace = rc.namespace
					}

					restoreSize, err := rc.getSnapshotRestoreSize(ctx, snapshotNamespace, snapshotSource.Name)
					if err != nil {
						log.Printf("Warning: could not fetch snapshot size for %s/%s: %v",
							snapshotNamespace, snapshotSource.Name, err)
						// Continue with template-specified size as fallback
					} else if restoreSize != nil {
						log.Printf("Using snapshot restoreSize %s for DataVolume %s",
							restoreSize.String(), dataVolume.Name)

						if dataVolume.Spec.Storage != nil {
							dataVolume.Spec.Storage.Resources.Requests[k8scorev1.ResourceStorage] = *restoreSize
						} else if dataVolume.Spec.PVC != nil {
							dataVolume.Spec.PVC.Resources.Requests[k8scorev1.ResourceStorage] = *restoreSize
						}
					}
				}

				// Update the VMI's volume spec to point to the newly named DataVolume
				volume.DataVolume.Name = dataVolume.Name

				break
			}
		}
	}

	// Add the special volume that exposes the runner JIT config annotation as a file.
	virtualMachineInstance.Spec.Volumes = append(virtualMachineInstance.Spec.Volumes, generateRunnerInfoVolume())

	return virtualMachineInstance, dataVolume, nil
}

// generateRunnerInfoVolume creates a KubeVirt Volume definition that uses the
// Downward API to project the VMI's annotation (containing the JIT config)
// into a file within the VMI. This allows the runner agent inside the VM to
// access its configuration.
func generateRunnerInfoVolume() v1.Volume {
	return v1.Volume{
		Name: runnerInfoVolume, // "runner-info"
		VolumeSource: v1.VolumeSource{
			DownwardAPI: &v1.DownwardAPIVolumeSource{
				Fields: []k8scorev1.DownwardAPIVolumeFile{
					{
						Path: runnerInfoPath, // "runner-info.json"
						FieldRef: &k8scorev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", runnerInfoAnnotation),
						},
					},
				},
			},
		},
	}
}

// getSnapshotRestoreSize fetches the restoreSize from a VolumeSnapshot.
// Returns nil, nil if the snapshot exists but doesn't have a restoreSize yet.
func (rc *KubevirtRunner) getSnapshotRestoreSize(
	ctx context.Context,
	namespace, name string,
) (*resource.Quantity, error) {
	volumeSnapshot, err := rc.virtClient.KubernetesSnapshotClient().
		SnapshotV1().
		VolumeSnapshots(namespace).
		Get(ctx, name, k8smetav1.GetOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get VolumeSnapshot %s/%s", namespace, name)
	}

	if volumeSnapshot.Status == nil || volumeSnapshot.Status.RestoreSize == nil {
		return nil, nil //nolint:nilnil // Snapshot exists but restoreSize not available yet
	}

	return volumeSnapshot.Status.RestoreSize, nil
}

// CreateResources creates the KubeVirt VirtualMachineInstance and any associated
// DataVolumes required for the runner. It uses a VM template and customizes it
// with the provided runner name and JIT configuration.
func (rc *KubevirtRunner) CreateResources(ctx context.Context,
	vmTemplate, runnerName, jitConfig string,
) error {
	if len(vmTemplate) == 0 {
		return ErrEmptyVMTemplate
	}

	if len(runnerName) == 0 {
		return ErrEmptyRunnerName
	}

	if len(jitConfig) == 0 {
		return ErrEmptyJitConfig
	}

	virtualMachineInstance, dataVolume, err := rc.getResources(ctx, vmTemplate, runnerName, jitConfig)
	if err != nil {
		return err
	}

	log.Printf("Creating %s Virtual Machine Instance\n", virtualMachineInstance.Name)

	vmi, err := rc.virtClient.VirtualMachineInstance(rc.namespace).Create(ctx,
		virtualMachineInstance, k8smetav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "fail to create runner instance")
	}

	rc.virtualMachineInstance = virtualMachineInstance.Name // Store the name of the created VMI

	if dataVolume != nil {
		log.Printf("Creating %s Data Volume\n", dataVolume.Name)

		// Set OwnerReference so the DataVolume is garbage collected with the VMI
		dataVolume.OwnerReferences = []k8smetav1.OwnerReference{
			{
				APIVersion: "kubevirt.io/v1",
				Kind:       "VirtualMachineInstance",
				Name:       vmi.Name,
				UID:        vmi.UID,
				Controller: ptr.To(false), // Not a controlling owner, but an owner
			},
		}

		if _, err := rc.virtClient.CdiClient().CdiV1beta1().DataVolumes(
			rc.namespace).Create(ctx, dataVolume, k8smetav1.CreateOptions{}); err != nil {
			return errors.Wrap(err, "cannot create data volume")
		}

		rc.dataVolume = dataVolume.Name // Store the name of the created DataVolume
	}

	return nil
}

// WaitForVirtualMachineInstance watches the specified VirtualMachineInstance until it
// reaches a terminal phase (Succeeded or Failed). It logs phase transitions and
// periodically reports the current status.
func (rc *KubevirtRunner) WaitForVirtualMachineInstance(ctx context.Context, virtualMachineInstance string) error {
	log.Printf("Watching %s Virtual Machine Instance\n", virtualMachineInstance)

	const reportingElapse = 5.0      // Minutes
	const watchTimeoutMinutes = 55.0 // Proactively recreate watch before k8s 60 min timeout

	// Store the VMI name for consistency
	rc.virtualMachineInstance = virtualMachineInstance

	for {
		// Check if context is cancelled before creating a new watch
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Continue with watch creation
		}

		// Create a new watch
		watch, err := rc.virtClient.VirtualMachineInstance(rc.namespace).Watch(ctx, k8smetav1.ListOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to watch the virtual machine instance")
		}

		// Always ensure watch is cleaned up when we leave this iteration
		defer watch.Stop()

		lastTimeChecked := time.Now()
		watchStartTime := time.Now()

		// Use a channel to track when we should exit the watch loop due to context cancellation
		ctxDone := make(chan struct{}, 1)

		// Start a goroutine to monitor context cancellation
		go func() {
			select {
			case <-ctx.Done():
				watch.Stop() // This will cause the watch.ResultChan() to close
				ctxDone <- struct{}{}
			}
		}()

		// Process watch events
		var watchResult error
		watchCompleted := false

		for event := range watch.ResultChan() {
			// Check if we're approaching the k8s watch timeout and need to recreate the watch
			if time.Since(watchStartTime).Minutes() > watchTimeoutMinutes {
				log.Printf("Proactively recreating watch for %s to avoid k8s timeout\n", virtualMachineInstance)
				watch.Stop() // Stop the watch to exit the loop and create a new one
				break
			}

			vmi, ok := event.Object.(*v1.VirtualMachineInstance)
			if ok && vmi.Name == virtualMachineInstance { // Ensure it's the VMI we are interested in
				// Check if the VMI's phase has changed or if it's time to report status
				if vmi.Status.Phase != rc.currentStatus || time.Since(lastTimeChecked).Minutes() > reportingElapse {
					rc.currentStatus = vmi.Status.Phase
					lastTimeChecked = time.Now()

					switch rc.currentStatus {
					case v1.Succeeded:
						log.Printf("%s has successfully completed\n", virtualMachineInstance)
						watchCompleted = true
						watch.Stop() // Stop the watch to exit the loop
						return nil
					case v1.Failed:
						log.Printf("%s has failed\n", virtualMachineInstance)
						watchCompleted = true
						watch.Stop() // Stop the watch to exit the loop
						return ErrRunnerFailed
					default:
						log.Printf("%s has transitioned to %s phase \n", virtualMachineInstance, rc.currentStatus)
					}
				}
			} else if time.Since(lastTimeChecked).Minutes() > reportingElapse {
				// If no relevant event received for a while, log current known status
				log.Printf("%s is in %s phase \n", virtualMachineInstance, rc.currentStatus)
				lastTimeChecked = time.Now()
			}
		}

		// If we're here, the watch loop exited without completing
		if watchCompleted {
			return watchResult
		}

		// Check if context was cancelled
		select {
		case <-ctxDone:
			return ctx.Err()
		default:
			// If we didn't break out of the loop due to watchTimeout, log unexpected closure
			if time.Since(watchStartTime).Minutes() <= watchTimeoutMinutes {
				log.Printf("Watch for %s closed unexpectedly, recreating...\n", virtualMachineInstance)
				// Small delay to avoid hammering the API if there's an issue
				time.Sleep(1 * time.Second)
			}
			// Continue to recreate the watch
		}
	}
}

// DeleteResources removes the VirtualMachineInstance and its associated DataVolume.
// It includes conditional logging to avoid interference with test runners.
func (rc *KubevirtRunner) DeleteResources(ctx context.Context, virtualMachineInstance, dataVolume string) error {
	log.Printf("Entering DeleteResources for VMI: %s, DataVolume: %s", virtualMachineInstance, dataVolume)
	startTime := time.Now()
	defer func() {
		log.Printf("DeleteResources for VMI: %s, DataVolume: %s took %s", virtualMachineInstance, dataVolume, time.Since(startTime))
	}()

	log.Printf("Cleaning %s Virtual Machine Instance resources\n",
		virtualMachineInstance)

	// Check if context is already canceled, if so create a new one with timeout
	// This ensures cleanup operations can complete even if the original context was canceled
	var deleteCtx context.Context
	var cancel context.CancelFunc

	if ctx.Err() != nil {
		// Original context is already canceled, create a new one with a timeout
		log.Printf("Original context was canceled, creating a new one for cleanup operations")
		deleteCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	} else {
		// Use the original context if it's still valid
		deleteCtx = ctx
	}

	// Delete the VMI
	if err := rc.virtClient.VirtualMachineInstance(rc.namespace).Delete(
		deleteCtx, virtualMachineInstance, k8smetav1.DeleteOptions{}); err != nil {
		if !k8serrors.IsNotFound(err) {
			log.Printf("Warning: Failed to delete VMI %s: %v", virtualMachineInstance, err)
			// We continue with other deletions instead of returning early
		}
	}

	// Delete the DataVolume if provided
	if len(dataVolume) > 0 {
		if err := rc.virtClient.CdiClient().CdiV1beta1().DataVolumes(rc.namespace).Delete(
			deleteCtx, dataVolume, k8smetav1.DeleteOptions{}); err != nil {
			if !k8serrors.IsNotFound(err) {
				log.Printf("Warning: Failed to delete DataVolume %s: %v", dataVolume, err)
				// We continue instead of returning early
			}
		}
	}

	// Check if there were any PVCs created from snapshots that need to be cleaned up
	// We don't return errors for PVC cleanup failures to ensure other resources are still deleted
	if len(rc.persistentVolumeClaim) > 0 {
		if err := rc.virtClient.CoreV1().PersistentVolumeClaims(rc.namespace).Delete(
			deleteCtx, rc.persistentVolumeClaim, k8smetav1.DeleteOptions{}); err != nil {
			if !k8serrors.IsNotFound(err) {
				log.Printf("Warning: Failed to delete PVC %s: %v", rc.persistentVolumeClaim, err)
			}
		}
	}

	return nil
}

// SetVMINameForTesting is a helper method for unit tests to set the virtualMachineInstance field
// directly. This should only be used in test code.
func (rc *KubevirtRunner) SetVMINameForTesting(name string) {
	rc.virtualMachineInstance = name
}

// SetPersistentVolumeClaimForTesting is a helper method for unit tests to set the persistentVolumeClaim field
// directly. This should only be used in test code.
func (rc *KubevirtRunner) SetPersistentVolumeClaimForTesting(name string) {
	rc.persistentVolumeClaim = name
}

// NewRunner creates a new KubevirtRunner instance.
// It requires the Kubernetes namespace and a KubeVirt client.
func NewRunner(namespace string, virtClient kubecli.KubevirtClient) *KubevirtRunner {
	return &KubevirtRunner{
		namespace:  namespace,
		virtClient: virtClient,
	}
}
