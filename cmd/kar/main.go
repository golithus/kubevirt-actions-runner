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
	"runtime/debug"

	"github.com/electrocucaracha/kubevirt-actions-runner/cmd/kar/app"
	runner "github.com/electrocucaracha/kubevirt-actions-runner/internal"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"kubevirt.io/client-go/kubecli"
)

type buildInfo struct {
	gitCommit       string
	gitTreeModified string
	buildDate       string
	goVersion       string
}

func getBuildInfo() buildInfo {
	out := buildInfo{}

	if info, ok := debug.ReadBuildInfo(); ok {
		out.goVersion = info.GoVersion

		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				out.gitCommit = setting.Value
			case "vcs.time":
				out.buildDate = setting.Value
			case "vcs.modified":
				out.gitTreeModified = setting.Value
			}
		}
	}

	return out
}

func main() {
	var (
		opts app.Opts
		err  error
	)

	buildInfo := getBuildInfo()
	log.Printf("starting kubevirt action runner\ncommit: %v\tmodified:%v\n",
		buildInfo.gitCommit, buildInfo.gitTreeModified)

	clientConfig := kubecli.DefaultClientConfig(&pflag.FlagSet{})

	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		log.Fatalf("error in namespace : %v\n", err)
	}

	virtClient, err := kubecli.GetKubevirtClientFromClientConfig(clientConfig)
	if err != nil {
		log.Fatalf("cannot obtain KubeVirt client: %v\n", err)
	}

	runner := runner.NewRunner(namespace, virtClient)

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
