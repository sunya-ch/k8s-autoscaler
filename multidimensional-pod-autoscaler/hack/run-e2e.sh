#!/bin/bash

# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# hack/run-e2e.sh — run the MPA end-to-end tests against a live cluster.
#
# Prerequisites:
#   - A running Kubernetes cluster with KUBECONFIG pointing to it.
#   - The MPA CRD installed:
#       kubectl apply -f deploy/mpa-v1alpha1-crd-gen.yaml
#   - The VPA CRDs and components (updater + admission-controller) deployed
#     with --feature-gates=MultidimPodAutoscaler=true.
#   - The MPA controller deployed:
#       make deploy IMG=<your-image>
#   - The test namespace created:
#       kubectl create namespace e2e-mpa
#
# Usage:
#   hack/run-e2e.sh [--namespace <ns>] [-- <extra ginkgo flags>]

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
E2E_DIR="${REPO_ROOT}/test/e2e"

NAMESPACE="${NAMESPACE:-e2e-mpa}"
KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/config}"

# Parse optional --namespace flag before the -- separator.
while [[ $# -gt 0 && "$1" != "--" ]]; do
  case "$1" in
    --namespace|-n)
      NAMESPACE="$2"; shift 2 ;;
    *)
      echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done
# Drop the -- separator if present.
[[ "${1:-}" == "--" ]] && shift

echo "▶ Running MPA e2e tests"
echo "  namespace : ${NAMESPACE}"
echo "  kubeconfig: ${KUBECONFIG}"
echo

# Ensure the test namespace exists.
kubectl --kubeconfig="${KUBECONFIG}" get namespace "${NAMESPACE}" > /dev/null 2>&1 \
  || kubectl --kubeconfig="${KUBECONFIG}" create namespace "${NAMESPACE}"

pushd "${E2E_DIR}" > /dev/null

go test -v -count=1 -timeout=20m ./... \
  --kubeconfig="${KUBECONFIG}" \
  --namespace="${NAMESPACE}" \
  "$@"

popd > /dev/null
