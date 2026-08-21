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

# Generates the MultidimPodAutoscaler CRD YAML from the Go types using
# controller-gen, writes it to deploy/mpa-v1alpha1-crd-gen.yaml, and copies
# it to the Helm chart CRD directory.
#
# Usage:
#   hack/generate-crd-yaml.sh
#
# Requirements:
#   controller-gen v0.21.0  (installed automatically if absent or wrong version)

set -o errexit
set -o nounset
set -o pipefail

REPOSITORY_ROOT=$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")
CRD_OPTS="crd:allowDangerousTypes=true"
APIS_PATH="${REPOSITORY_ROOT}/pkg/apis"
OUTPUT="${REPOSITORY_ROOT}/deploy/mpa-v1alpha1-crd-gen.yaml"
CHARTS_CRD_DIR="${REPOSITORY_ROOT}/charts/multidimensional-pod-autoscaler/crds"
CONTROLLER_GEN_VERSION="v0.21.0"
WORKSPACE=$(mktemp -d)

function cleanup() {
    rm -rf "${WORKSPACE}"
}
trap cleanup EXIT

# Install controller-gen if the required version is not available.
if [[ -z "$(which controller-gen 2>/dev/null)" || \
      "$(controller-gen --version 2>/dev/null)" != "Version: ${CONTROLLER_GEN_VERSION}" ]]; then
    echo "Installing controller-gen ${CONTROLLER_GEN_VERSION} ..."
    (
        cd "${WORKSPACE}"
        go install "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}"
    )
    CONTROLLER_GEN="${GOBIN:-$(go env GOPATH)/bin}/controller-gen"
else
    CONTROLLER_GEN="$(which controller-gen)"
fi

echo "Using controller-gen: ${CONTROLLER_GEN} ($(${CONTROLLER_GEN} --version 2>/dev/null))"

# Run controller-gen.  The tool exits non-zero when it encounters map keys that
# are not strings (a known limitation); we suppress only that specific error and
# treat any other output as a real failure.
"${CONTROLLER_GEN}" "${CRD_OPTS}" \
    paths="${APIS_PATH}/..." \
    output:crd:dir="\"${WORKSPACE}\"" \
    >& "${WORKSPACE}/errors.log" ||:

grep -v \
    -e 'map keys must be strings, not int' \
    -e 'not all generators ran successfully' \
    -e 'usage' \
    "${WORKSPACE}/errors.log" \
    && { echo "ERROR: controller-gen failed to generate CRD YAMLs."; exit 1; }

GENERATED="${WORKSPACE}/autoscaling.x-k8s.io_multidimpodautoscalers.yaml"
if [[ ! -f "${GENERATED}" ]]; then
    echo "ERROR: expected generated file not found: ${GENERATED}"
    echo "Files in workspace:"
    ls "${WORKSPACE}/"
    exit 1
fi

# Write the output file.
mkdir -p "$(dirname "${OUTPUT}")"
cp "${GENERATED}" "${OUTPUT}"
echo "CRD written to ${OUTPUT}"

# Copy to Helm chart CRD directory.
mkdir -p "${CHARTS_CRD_DIR}"
cp "${OUTPUT}" "${CHARTS_CRD_DIR}/mpa-v1alpha1-crd-gen.yaml"
echo "CRD copied to ${CHARTS_CRD_DIR}/mpa-v1alpha1-crd-gen.yaml"
