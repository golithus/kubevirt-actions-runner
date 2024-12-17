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

package app

import (
	"context"

	runner "github.com/electrocucaracha/kubevirt-actions-runner/internal"
	"github.com/spf13/cobra"
)

func NewRootCommand(ctx context.Context, cmdOptions Opts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kar",
		Short: "Tool that creates a GitHub Self-Host runner with Kubevirt Virtual Machine Instance",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return initializeConfig(cmd)
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(ctx, cmdOptions)
		},
	}

	installFlags(cmd.Flags(), &cmdOptions)

	return cmd
}

func run(ctx context.Context, c Opts) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	runnerClient := runner.NewRunner()
	runnerClient.CreateResources(ctx, c.vmTemplate, c.runnerName, c.jsonConfig)
	defer runnerClient.DeleteResources(ctx)

	runnerClient.WaitForVirtualMachineInstance(ctx)

	return nil
}
