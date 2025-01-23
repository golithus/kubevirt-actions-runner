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
)

type buildInfo struct {
	gitCommit       string
	gitTreeModified string
	buildDate       string
	goVersion       string
}

func getBuildInfo() buildInfo {
	b := buildInfo{}
	if info, ok := debug.ReadBuildInfo(); ok {
		b.goVersion = info.GoVersion
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.revision":
				b.gitCommit = kv.Value
			case "vcs.time":
				b.buildDate = kv.Value
			case "vcs.modified":
				b.gitTreeModified = kv.Value
			}
		}
	}

	return b
}

func main() {
	var opts app.Opts

	b := getBuildInfo()
	log.Printf("starting kubevirt action runner\ncommit: %v\tmodified:%v\n", b.gitCommit, b.gitTreeModified)

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
