/*
Copyright 2025 The Kubernetes Authors.

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

package e2e

import (
	"flag"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Flags consumed by the test suite — declared here (not in e2e_test.go) so that
// non-test files such as utils.go can reference them.
var (
	flagKubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig file (defaults to KUBECONFIG env var or in-cluster config)")
	flagNamespace  = flag.String("namespace", "e2e-mpa", "Namespace to create test objects in (must already exist)")
)

// loadConfig returns a *rest.Config from the given kubeconfig path, or falls
// back to in-cluster config when path is empty.
func loadConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return clientcmd.BuildConfigFromFlags("", kc)
	}
	return rest.InClusterConfig()
}
