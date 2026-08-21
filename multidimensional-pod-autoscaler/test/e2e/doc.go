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

// Package e2e holds end-to-end tests for the MultidimPodAutoscaler controller.
//
// The tests require a live Kubernetes cluster with:
//   - The MPA CRD installed (deploy/mpa-v1alpha1-crd-gen.yaml)
//   - The VPA CRDs installed (vertical-pod-autoscaler/deploy/vpa-v1-crd-gen.yaml)
//   - The VPA components deployed with the MultidimPodAutoscaler feature gate
//     enabled on both the updater and the admission controller
//   - The MPA controller deployed
//
// Run the suite via hack/run-e2e.sh or:
//
//	cd test/e2e && go test -v -count=1 ./... \
//	  --kubeconfig=$HOME/.kube/config \
//	  --namespace=e2e-mpa-test
package e2e
