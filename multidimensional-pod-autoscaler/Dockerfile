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

# Build context must be the repository root so that the go.mod replace
# directive (../vertical-pod-autoscaler) can be resolved inside the builder.
# The Makefile docker-build target passes ".." as the context and "-f
# multidimensional-pod-autoscaler/Dockerfile" for this reason.

FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

WORKDIR /workspace

# Copy dependency manifests for both modules first so the module cache layer
# is invalidated only when dependencies change, not on every source edit.
COPY multidimensional-pod-autoscaler/go.mod multidimensional-pod-autoscaler/go.mod
COPY multidimensional-pod-autoscaler/go.sum multidimensional-pod-autoscaler/go.sum
COPY vertical-pod-autoscaler/go.mod         vertical-pod-autoscaler/go.mod
COPY vertical-pod-autoscaler/go.sum         vertical-pod-autoscaler/go.sum

# Download dependencies with the replace directive in place.
RUN cd multidimensional-pod-autoscaler && go mod download

# Copy all source needed for the build.
COPY multidimensional-pod-autoscaler/ multidimensional-pod-autoscaler/
COPY vertical-pod-autoscaler/         vertical-pod-autoscaler/

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG BUILD_LD_FLAGS="-s"

RUN CGO_ENABLED=0 GOARCH=$TARGETARCH GOOS=$TARGETOS \
    go build \
      -C multidimensional-pod-autoscaler \
      -ldflags="${BUILD_LD_FLAGS}" \
      -o /workspace/mpa-controller-$TARGETARCH \
      ./cmd/...

# ── Runtime image ─────────────────────────────────────────────────────────────

FROM gcr.io/distroless/static:nonroot

ARG TARGETARCH=amd64

COPY --from=builder /workspace/mpa-controller-$TARGETARCH /mpa-controller

ENTRYPOINT ["/mpa-controller"]
