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

package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/electrocucaracha/kubevirt-actions-runner/cmd/kar/app"
	runner "github.com/electrocucaracha/kubevirt-actions-runner/internal"
	"github.com/pkg/errors"
)

func main() {
	var opts app.Opts

	runner := runner.NewRunner()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		runner.DeleteResources(ctx)
		stop()
	}()

	rootCmd := app.NewRootCommand(ctx, runner, opts)

	if err := rootCmd.Execute(); err != nil && !errors.Is(errors.Cause(err), context.Canceled) {
		log.Panicln(err.Error())
	}
}
