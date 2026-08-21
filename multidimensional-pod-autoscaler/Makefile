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

# ── Variables ─────────────────────────────────────────────────────────────────

REGISTRY       ?= localhost:5001
IMG            ?= $(REGISTRY)/mpa-controller:dev
NAMESPACE      ?= kube-system
KUBECTL        ?= kubectl
CLUSTER_NAME   ?= kind
CONTAINER_TOOL ?= docker

BINARY := bin/mpa-controller
CRD    := deploy/mpa-v1alpha1-crd-gen.yaml
DEPLOY := deploy/controller.yaml

# ── Phony targets ─────────────────────────────────────────────────────────────

.PHONY: all build test e2e fmt vet lint \
        generate verify-crd \
        docker-build docker-push \
        kind-load install-crd deploy undeploy \
        help

all: build

# ── Development ───────────────────────────────────────────────────────────────

## build         Compile the controller binary → bin/mpa-controller
build:
	go build -o $(BINARY) ./cmd/...

## test          Run all unit tests with race detection
test:
	go test -race ./...

## e2e           Run end-to-end tests against a live cluster
##               Requires KUBECONFIG set and the cluster prepared — see hack/run-e2e.sh
e2e:
	hack/run-e2e.sh

## fmt           Format all Go source files
fmt:
	go fmt ./...

## vet           Run go vet
vet:
	go vet ./...

## lint          Run golangci-lint (must be installed separately)
lint:
	golangci-lint run ./...

## generate      Regenerate CRD YAML from Go types
generate:
	hack/generate-crd-yaml.sh

## verify-crd    Verify committed CRD files match the Go types
verify-crd:
	hack/verify-crd.sh

# ── Container image ───────────────────────────────────────────────────────────

## docker-build  Build the controller image   (IMG=myregistry/mpa:tag)
##               Build context is the repo root so the go.mod replace
##               directive (../vertical-pod-autoscaler) resolves correctly.
docker-build:
	$(CONTAINER_TOOL) build -t $(IMG) -f Dockerfile ..

## docker-push   Push the controller image
docker-push:
	$(CONTAINER_TOOL) push $(IMG)

## kind-load     Save image to a tar archive and load it into a kind cluster
#               Works with both docker and podman (neither supports kind load
#               directly from podman's image store).
#               CLUSTER_NAME defaults to "kind"; CONTAINER_TOOL to "docker".
kind-load:
	$(CONTAINER_TOOL) image save $(IMG) -o /tmp/image.tar
	kind load image-archive /tmp/image.tar --name $(CLUSTER_NAME)
	rm /tmp/image.tar

# ── Kubernetes deployment ─────────────────────────────────────────────────────
#
# deploy/controller.yaml is the static manifest (committed, always readable).
# IMG is substituted into the manifest at apply time so only a single rollout
# occurs (a two-step apply + set image would trigger two rolling updates and
# cause a spurious leader-election "Failed to acquire lease" message).
# NAMESPACE is applied with `kubectl -n`; the manifest itself uses kube-system
# as the default namespace for the ServiceAccount and Deployment.

## install-crd   Apply the MultidimPodAutoscaler CRD to the cluster
install-crd:
	$(KUBECTL) apply -f $(CRD)

## deploy        Install CRD + deploy the controller
#               IMG overrides the image (default: localhost:5001/mpa-controller:dev)
#               NAMESPACE overrides the target namespace (default: kube-system)
deploy: install-crd
	sed 's|mpa-controller:dev|$(IMG)|g' $(DEPLOY) | $(KUBECTL) apply -f -

## undeploy      Remove the controller deployment and RBAC from the cluster
#               (the CRD is kept — delete it manually to purge all MPA objects)
#               Also removes the leader-election Lease created at runtime by
#               controller-runtime (not present in the static manifest).
undeploy:
	$(KUBECTL) delete -f $(DEPLOY) --ignore-not-found
	$(KUBECTL) delete lease mpa-controller-leader -n $(NAMESPACE) --ignore-not-found

# ── Help ──────────────────────────────────────────────────────────────────────

## help          Print available targets
help:
	@echo "Usage: make [target] [VAR=value ...]"
	@echo ""
	@echo "Variables:  IMG=$(IMG)  NAMESPACE=$(NAMESPACE)  KUBECTL=$(KUBECTL)"
	@echo "            CLUSTER_NAME=$(CLUSTER_NAME)  CONTAINER_TOOL=$(CONTAINER_TOOL)"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
