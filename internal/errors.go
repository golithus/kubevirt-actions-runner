/*
Copyright © 2025

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

import "errors"

// ErrEmptyVMTemplate indicates that virtual machine template provided is empty.
var ErrEmptyVMTemplate = errors.New("empty vm template")

// ErrEmptyRunnerName indicates that runner name provided is empty.
var ErrEmptyRunnerName = errors.New("empty runner name")

// ErrEmptyJitConfig indicates that Just-in-Time configuration provided is empty.
var ErrEmptyJitConfig = errors.New("empty jit config")
