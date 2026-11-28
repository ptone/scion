// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command conduit-c19 is the Criterion 19 live load driver for Scion Conduit
// HA verification (v2.17 / v2.22 §6.2). It is non-shipped validation tooling
// and is never compiled into production binaries.
package main

import (
	"os"

	"github.com/GoogleCloudPlatform/scion/hack/conduit-c19/c19cli"
)

func main() {
	os.Exit(c19cli.Main(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
