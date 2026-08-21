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

# Verifies that the committed CRD YAML files are up to date with the Go types.
# Fails if hack/generate-crd-yaml.sh would produce a different result.
#
# Usage:
#   hack/verify-crd.sh

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT="$(dirname "${BASH_SOURCE[0]}")/.."
SCRIPT_ROOT="$(realpath "${SCRIPT_ROOT}")"

DEPLOY_CRD="${SCRIPT_ROOT}/deploy/mpa-v1alpha1-crd-gen.yaml"
CHARTS_CRD="${SCRIPT_ROOT}/charts/multidimensional-pod-autoscaler/crds/mpa-v1alpha1-crd-gen.yaml"

_TMP="${SCRIPT_ROOT}/_tmp"
TMP_DEPLOY_CRD="${_TMP}/deploy/mpa-v1alpha1-crd-gen.yaml"
TMP_CHARTS_CRD="${_TMP}/charts/multidimensional-pod-autoscaler/crds/mpa-v1alpha1-crd-gen.yaml"

cleanup() {
    rm -rf "${_TMP}"
}
trap "cleanup" EXIT SIGINT

cleanup

# Ensure committed CRD files exist.
if [[ ! -f "${DEPLOY_CRD}" ]]; then
    echo "ERROR: ${DEPLOY_CRD} does not exist. Run hack/generate-crd-yaml.sh first."
    exit 1
fi
if [[ ! -f "${CHARTS_CRD}" ]]; then
    echo "ERROR: ${CHARTS_CRD} does not exist. Run hack/generate-crd-yaml.sh first."
    exit 1
fi

# Save current committed files.
mkdir -p "${_TMP}/deploy"
mkdir -p "${_TMP}/charts/multidimensional-pod-autoscaler/crds"
cp "${DEPLOY_CRD}"  "${TMP_DEPLOY_CRD}"
cp "${CHARTS_CRD}"  "${TMP_CHARTS_CRD}"

# Regenerate in place.
"${SCRIPT_ROOT}/hack/generate-crd-yaml.sh"

echo "Diffing committed CRD files against freshly generated CRDs ..."
ret=0
diff -Naupr "${TMP_DEPLOY_CRD}"  "${DEPLOY_CRD}"  || ret=$?
diff -Naupr "${TMP_CHARTS_CRD}"  "${CHARTS_CRD}"  || ret=$?

# Restore committed files so this script is idempotent.
cp "${TMP_DEPLOY_CRD}"  "${DEPLOY_CRD}"
cp "${TMP_CHARTS_CRD}"  "${CHARTS_CRD}"

if [[ ${ret} -eq 0 ]]; then
    echo "CRD files are up to date."
else
    echo "ERROR: CRD files are out of date. Please run hack/generate-crd-yaml.sh and commit the result."
    exit 1
fi
