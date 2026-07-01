# AEP-NNNN: DRARecreate — VPA Support for DRA ResourceClaim Capacity

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Admission Controller Changes](#admission-controller-changes)
  - [Updater Changes](#updater-changes)
  - [Recommendation Storage Convention](#recommendation-storage-convention)
  - [Test Plan](#test-plan)
  - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
  - [Graduation Criteria](#graduation-criteria)
  - [Version Skew](#version-skew)
  - [Kubernetes Version Compatibility](#kubernetes-version-compatibility)
- [Risk Mitigation](#risk-mitigation)
  - [Pod Disruption During Capacity Updates](#pod-disruption-during-capacity-updates)
  - [Recommendation Drift Loop](#recommendation-drift-loop)
  - [Mitigation Strategies](#mitigation-strategies)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This AEP introduces `UpdateModeDRARecreate`, a new VPA update mode that extends VPA to manage [Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) `ResourceClaim` consumable capacity alongside regular CPU/memory resources.

When set to `DRARecreate`, VPA applies DRA capacity recommendations at pod-creation time through the admission controller, patching the capacity values in the pod's `ResourceClaim` before it is stored in etcd. When the recommendation changes, the VPA updater evicts the pod; Kubernetes recreates it and the admission controller writes the updated capacity into the new `ResourceClaim`.

The feature is opt-in via the `DRARecreate` feature gate and a new `resourceClaimPolicies` field in `VPA.spec.resourcePolicy`. DRA capacity recommendations are stored as extended resources in the existing `VPA.status.recommendation` schema by an external recommender; VPA reads and actuates them.

## Motivation

Dynamic Resource Allocation is becoming the standard mechanism for managing specialized hardware (GPUs, FPGAs, network devices) in Kubernetes. Workloads using DRA increasingly need automatic right-sizing of device capacity allocations — for example, adjusting GPU memory allocated to an LLM inference pod based on observed KV-cache utilization — but VPA has no way to actuate changes to `ResourceClaim` capacity.

The core obstacle is that DRA capacity is stored inside a `ResourceClaim` object that is **immutable after creation**. Changing capacity requires the pod to be deleted so a new `ResourceClaim` is created from the `ResourceClaimTemplate`. VPA's existing eviction-and-recreate path (`Recreate` mode) provides exactly this lifecycle, but the admission controller has never been wired to patch `ResourceClaim` capacity on creation.

This AEP closes that gap: it adds the admission-time patching path for `ResourceClaim` capacity and the eviction-priority logic to trigger it when the recommendation diverges from the live claim.

### Goals

- Apply DRA capacity recommendations to `ResourceClaim` objects at pod-creation time via the VPA admission controller.
- Evict pods whose live `ResourceClaim` capacity diverges from the current VPA recommendation so they are recreated with updated capacity.
- Allow operators to declare bounds (`minAllowed`, `maxAllowed`) and the set of managed device capacities per claim template via a new `ResourceClaimPolicy` API field.
- Introduce a new `DRARecreate` VPA feature gate and `UpdateModeDRARecreate` update mode, keeping the feature fully opt-in and non-breaking for existing VPA users.

### Non-Goals

- Generating or collecting capacity metrics. VPA reads recommendations that an external controller has already written to `VPA.status`.
- In-place updates of `ResourceClaim` capacity. DRA consumable capacity is immutable after claim creation; pod recreation is the only supported path.
- Managing `ResourceClaim` objects that are not created from a `ResourceClaimTemplate`.
- Creating or versioning `ResourceClaimTemplate` copies. Capacity is patched directly into the new `ResourceClaim` at admission time.
- Implementing DRA drivers, device plugins, or introducing external recommenders.

## Proposal

Add a new `DRARecreate` update mode to VPA. When this mode is active:

1. **Admission time** — when a pod's `ResourceClaim` is created, the VPA admission controller reads the DRA capacity recommendations from `VPA.status` and patches the claim's `spec.devices.requests[].exactly.capacity.requests` values before the object is persisted.
2. **Update time** — the VPA updater periodically compares the live `ResourceClaim` capacity against the current recommendation. Any divergence immediately marks the pod as outside the recommended range, triggering eviction so the pod is recreated with fresh capacity.

The flow end-to-end:

```text
External Recommender
   │  writes DRA capacity as extended resources into VPA.status.recommendation
   ▼
VPA Updater (priority processor)
   │  reads current ResourceClaim capacities from the cluster
   │  compares against VPA recommendation
   │  if they differ → evicts pod
   ▼
Kubernetes recreates pod → new ResourceClaim created from template
   ▼
VPA Admission Controller
   │  Pod webhook: matches pod to VPA, caches (VPA, pod) entry
   │  ResourceClaim webhook: reads VPA recommendation from cache
   │                         applies min/max capping (ResourceClaimPolicy)
   │                         patches capacity.requests into the new ResourceClaim
   ▼
Pod starts with updated ResourceClaim capacity
```

### Example VPA configuration

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: gpu-inference-vpa
  namespace: inference
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: gpu-inference
  updatePolicy:
    updateMode: DRARecreate
  resourcePolicy:
    containerPolicies:
    - containerName: inference-server
      mode: Off          # optional: disable CPU/memory management
    resourceClaimPolicies:
    - claimTemplateName: gpu-claim-template   # matches pod.spec.resourceClaims[].resourceClaimTemplateName
      deviceClassName: vgpu.example.com       # matches the device class in the template
      minAllowed:
        memory: "8Gi"
      maxAllowed:
        memory: "80Gi"
      controlledCapacities:
      - memory                                # bare capacity name, as in ResourceClaim spec
```

## Design Details

### API Changes

#### New `UpdateMode` value

```go
// UpdateModeDRARecreate means that autoscaler assigns DRA resources on pod
// creation and additionally can update them during the lifetime of the pod
// by deleting and recreating the pod, including DRA ResourceClaim recreate.
// Requires the VPA-level feature gate "DRARecreate" to be enabled on the
// admission-controller and updater pods.
// Requires the cluster feature gate "DRAConsumableCapacity" to be enabled.
UpdateModeDRARecreate UpdateMode = "DRARecreate"
```

#### New `ResourceClaimPolicy` type

Added to `PodResourcePolicy`:

```go
type PodResourcePolicy struct {
    // ... existing ContainerPolicies field ...

    // ResourceClaimPolicies configures DRA ResourceClaim management for this pod.
    // +optional
    ResourceClaimPolicies []ResourceClaimPolicy `json:"resourceClaimPolicies,omitempty"`
}

// ResourceClaimPolicy defines the policy for managing DRA resources in a pod.
// +kubebuilder:validation:XValidation:rule="has(self.controlledCapacities) && size(self.controlledCapacities) > 0",message="controlledCapacities must contain at least one capacity name"
type ResourceClaimPolicy struct {
    // ClaimTemplateName is the name of the ResourceClaimTemplate referenced in the pod spec.
    // Must match a name in pod.spec.resourceClaims[].resourceClaimTemplateName.
    // +required
    ClaimTemplateName string `json:"claimTemplateName"`

    // DeviceClassName specifies which device class to target in the ResourceClaimTemplate.
    // Must match the deviceClassName in the ResourceClaimTemplate's device requests.
    // +required
    DeviceClassName string `json:"deviceClassName"`

    // MinAllowed specifies the minimum recommended capacity per bare capacity name
    // (e.g. "memory"). Default: no minimum.
    // +optional
    MinAllowed corev1.ResourceList `json:"minAllowed,omitempty"`

    // MaxAllowed specifies the maximum recommended capacity per bare capacity name.
    // Default: no maximum.
    // +optional
    MaxAllowed corev1.ResourceList `json:"maxAllowed,omitempty"`

    // ControlledCapacities lists which bare capacity names VPA manages.
    // Each entry corresponds to a key in ResourceClaim spec.devices.requests[].exactly.capacity.requests.
    // Must contain at least one entry.
    // +required
    // +kubebuilder:validation:MinItems=1
    ControlledCapacities []resourceapi.QualifiedName `json:"controlledCapacities"`
}
```

Validation rules (enforced by the admission controller):

- `claimTemplateName` and `deviceClassName` must be non-empty.
- `controlledCapacities` must contain at least one entry.
- `UpdateModeDRARecreate` is rejected unless the `DRARecreate` feature gate is enabled.

#### New feature gate

```go
// DRARecreate enables DRA ResourceClaim consumable capacity management.
// alpha: v1.8.0 — components: admission-controller, updater
DRARecreate featuregate.Feature = "DRARecreate"
```

### Admission Controller Changes

#### VpaCache

A new in-memory, TTL-evicting cache (`pkg/admission-controller/resource/pod/cache.go`) bridges the pod admission webhook and the `ResourceClaim` admission webhook:

- At pod admission time the matched VPA and the pod are stored under the key `"namespace/generateName%"` (or the explicit pod name).
- At `ResourceClaim` admission time the `ResourceClaim` carries an owner reference pointing to its pod. The handler strips the Kubernetes-appended random suffix from the pod name to reconstruct the cache key.
- Entries expire after 30 seconds via `time.AfterFunc`, preventing leaks if a pod is admitted but its claims never arrive. Entries are **not** removed on read, because a pod may own multiple `ResourceClaim` objects arriving in separate admission requests.

#### ResourceClaim admission handler

`pkg/admission-controller/resource/resourceclaim/handler.go` implements a new `resource_admission.Handler` registered alongside the existing pod handler:

- Accepts only `resource.k8s.io/v1` `ResourceClaims` on **CREATE** operations (capacity is immutable after creation; updates are skipped).
- Resolves the owner pod from `ResourceClaim.ownerReferences`, looks up the `VpaCacheEntry`, and delegates to `patch.Calculator` instances whose `PatchResourceTarget()` returns `ResourceClaim`.
- Translates **metadata patch paths** (`/resourceClaim/<claimName>/<deviceClass>/<capacityName>`) emitted by the calculator into real JSON Patch operations against `spec.devices.requests[N]/exactly/capacity/requests/<capacityName>`. The translation handles three states of the incoming claim:

| Claim state                   | JSON Patch op                                     |
|-------------------------------|---------------------------------------------------|
| `exactly.capacity` is `nil`   | `add /…/exactly/capacity {"requests":{…}}`        |
| `capacity.requests` is `nil`  | `add /…/exactly/capacity/requests {…}`            |
| capacity key absent           | `add /…/exactly/capacity/requests/<name>`         |
| capacity key present          | `replace /…/exactly/capacity/requests/<name>`     |

After emitting an `add capacity` patch the in-memory claim representation is updated so that subsequent patches within the same admission request do not emit a duplicate `add capacity` patch.

#### ResourceClaim patch calculator

`pkg/admission-controller/resource/pod/patch/resource_claim_updates.go` implements `Calculator` (`PatchResourceTarget() == ResourceClaim`):

1. Reads `VPA.status.recommendation.containerRecommendations[].target`.
2. Extracts extended resources (resource names containing `"/"`) via `ExtractDRACapacitiesFromRecommendations`.
3. Applies `ResourceClaimPolicy` min/max capping via `ApplyResourceClaimPolicies`.
4. For each `ControlledCapacity` in each matching policy emits a metadata patch:

   ```yaml
   Op:    "replace"
   Path:  /resourceClaim/<claimName>/<deviceClassName>/<bareCapacityName>
   Value: <capped quantity string>
   ```

### Updater Changes

#### ResourceClaim lister

`NewResourceClaimLister` in `updater.go` creates a reflector-backed `ResourceClaimLister` for the watched namespace, allowing the updater to read current claim capacities from a local cache without API-server round-trips.

The main eviction loop is extended to recognise `DRARecreate` as an evictable mode alongside `Recreate` and `Auto`.

#### Priority processor

`priority_processor.go` is extended for `DRARecreate` mode:

1. Calls `ResourceClaimRequests(pod, claimLister)` to read current capacities from the pod's live `ResourceClaim` objects, keyed as `"deviceClass/capacityName"`.
2. Merges these into the container-requests map alongside CPU and memory.
3. Marks a pod as **outside the recommended range** whenever any DRA capacity differs from its recommendation — bypassing the `MinChangePriority` and pod-lifetime thresholds that apply to CPU/memory — so that any capacity drift triggers an eviction immediately.

#### `ResourceClaimRequests` helper

`resourcehelpers.go` gains `ResourceClaimRequests(pod, claimLister)`:

- Resolves the final `ResourceClaim` name from `pod.status.resourceClaimStatuses`.
- Fetches each claim from the lister.
- Collects every `spec.devices.requests[].exactly.capacity.requests` entry as `"deviceClass/capacityName" → quantity`.
- Returns a flat `corev1.ResourceList` for use by the priority processor.

#### ResourceClaim recommendation processor

`resource_claim_recommendation_processor.go` provides `NewResourceClaimRecommendationProcessor()`, a `RecommendationProcessor` that extracts and caps DRA extended resources from `containerRecommendations[].target` before the recommendation is handed to the eviction decision logic.

When `DRARecreate` is enabled, `updater/main.go` chains this processor after the existing `CappingRecommendationProcessor` using a `SequentialProcessor`:

```text
CappingRecommendationProcessor → ResourceClaimRecommendationProcessor
      (CPU/Memory capping)               (DRA capacity capping)
```

### Recommendation Storage Convention

An external recommender writes DRA capacity recommendations as **extended resources** into the existing `containerRecommendations[].target` `ResourceList`.

Capacity keys use the full `"deviceClass/capacityName"` form (e.g. `vgpu.example.com/memory`). The `ResourceClaimPolicy.ControlledCapacities` field uses bare names (e.g. `memory`); code expands them to full names by prepending `DeviceClassName + "/"` at lookup time.

```yaml
status:
  recommendation:
    containerRecommendations:
    - containerName: "app"
      target:
        vgpu.example.com/memory: "16Gi"   # full "deviceClass/capacityName" form
```

### Test Plan

**Unit tests:**

- `resource/resourceclaim/handler_test.go` — ResourceClaim admission handler: CREATE/non-CREATE operations, cache miss, metadata-patch translation, multiple capacities.
- `resource/pod/patch/resource_claim_updates_test.go` — patch calculator: capacity extraction, min/max capping, patch generation, no policies case.
- `pkg/utils/vpa/resource_claim_recommendation_processor_test.go` — `Apply`, `ApplyResourceClaimPolicies`, `ExtractDRACapacitiesFromRecommendations`.
- `pkg/updater/priority/priority_processor_test.go` — DRARecreate eviction priority: capacity matches (no eviction), capacity differs (eviction), nil lister fallback.
- `resource/vpa/validation_test.go` — `ResourceClaimPolicy` field validation.

**E2E tests (`test/e2e/v1/`):**

| Scenario                                                  | File                       | Description                                                                                                                           |
| --------------------------------------------------------- | -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| Admission patches capacity                                | `admission_controller.go`  | Pod and `ResourceClaimTemplate` created; VPA recommendation set; verify the new pod's `ResourceClaim` has the patched capacity value. |
| Updater evicts on capacity drift                          | `updater.go`               | Pod running with 8 Gi initial capacity; VPA recommends 16 Gi; verify pod is evicted and recreated with 16 Gi.                         |
| Updater does not evict when current equals recommendation | `updater.go`               | Pod running with 16 Gi capacity; VPA recommends 16 Gi; verify no eviction occurs.                                                     |

### Feature Enablement and Rollback

**Feature gate name:** `DRARecreate`

**Components:** `admission-controller`, `updater`

**When enabled:**

- The admission controller registers a new webhook for `resource.k8s.io/resourceclaims` and patches capacity on `CREATE`.
- The updater creates a `ResourceClaimLister`, chains the `ResourceClaimRecommendationProcessor`, and recognises `UpdateModeDRARecreate` in its eviction loop.
- The `DRARecreate` update mode is accepted in VPA objects.

**When disabled after being enabled:**

- The `ResourceClaim` admission webhook is not registered; new pods receive `ResourceClaim` objects with the unpatched template capacity.
- The updater stops comparing claim capacities; no evictions are triggered for DRA resources.
- Existing pods and claims are unaffected and continue running.
- VPA objects with `resourceClaimPolicies` or `updateMode: DRARecreate` are ignored; existing cluster objects are not deleted.
- Manual intervention is required if the user wants to restore the original claim capacity.

**No gate is needed for** the VPA recommender or any external recommender — they write to `VPA.status` independently of this feature gate.

### Graduation Criteria

**Alpha:**

- Feature gate `DRARecreate` disabled by default.
- `UpdateModeDRARecreate` accepted by the admission controller when the gate is enabled.
- Admission controller patches `ResourceClaim` capacity on pod creation.
- Updater evicts pods when live claim capacity diverges from recommendation.
- Unit tests covering handler, calculator, processor, and priority logic.
- E2E tests for admission patching and updater eviction passing.
- Documentation updated.

**Beta:**

- Feature gate enabled by default.
- Tests stable across two releases with no critical bugs.
- Positive feedback from early adopters.
- E2E tests extended to cover multiple device classes per pod.

**GA:**

- `DRAConsumableCapacity` is GA.
- Feature gate removed.
- Validated in production environments.
- No open P0/P1 bugs.

### Version Skew

The `DRARecreate` feature gate must be enabled on **both** the admission controller and the updater before the feature takes effect. Enabling it on only one component results in no-ops:

- Gate enabled on admission controller only: `ResourceClaim` capacity is patched at creation time, but the updater does not evict pods for drift.
- Gate enabled on updater only: Pods are evicted for capacity drift, but the new `ResourceClaim` receives unpatched template capacity, causing a reconciliation loop.

Because VPA recommenders write only to `VPA.status`, enabling or disabling the gate on the recommender has no effect on this feature.

### Kubernetes Version Compatibility

**Minimum: Kubernetes 1.34** (DRA GA).

The `DRAConsumableCapacity` cluster feature gate must be enabled on the Kubernetes API server. This gate is required for the `spec.devices.requests[].exactly.capacity` field to be accepted by the API server.

| Kubernetes version | Status                                                                                                              |
| ------------------ | ------------------------------------------------------------------------------------------------------------------- |
| < 1.34             | DRA not GA; this feature cannot be used.                                                                            |
| 1.34               | DRA GA; `DRAConsumableCapacity` is alpha and with a known capacity consuming bug; this feature should not be used.  |
| 1.35               | DRA GA; `DRAConsumableCapacity` is alpha (must be enabled manually).                                                |
| 1.36+              | `DRAConsumableCapacity` expected to reach beta (enabled by default).                                                |

When the `DRAConsumableCapacity` cluster gate is absent or disabled, capacity patches from the admission controller are rejected by the API server. The VPA feature gate should not be enabled in this case.

## Risk Mitigation

### Pod Disruption During Capacity Updates

`DRARecreate` mode updates ResourceClaim capacity by **evicting and recreating the pod**. This is inherently disruptive: the pod is deleted, a new one is scheduled, and the workload restarts from scratch. Unlike in-place updates, there is no way to apply a capacity change without a restart — this is a hard limitation of the current DRA, not a VPA-specific behavior.

For workloads that cannot tolerate pod restarts (e.g. long-running stateful jobs), `DRARecreate` mode is unsuitable. Users should leave `updateMode: Off` for those VPAs and manage capacity changes manually.

### Recommendation Drift Loop

If the `DRARecreate` feature gate is enabled on the **updater only** (not the admission controller), evicted pods are recreated with unpatched template capacity. The updater then immediately detects a drift and evicts again, producing a reconciliation loop. The pod never stabilises.

This risk is avoided by always enabling the feature gate on **both** the admission controller and the updater together (see [Version Skew](#version-skew)).

### Mitigation Strategies

- **Opt-in per VPA**: Only VPAs with `updateMode: DRARecreate` are affected; all other VPAs are unaffected.
- **`minAllowed` / `maxAllowed` bounds**: Set conservative bounds in `ResourceClaimPolicy` to limit how aggressively VPA changes capacity and reduce unnecessary evictions.
- **Feature gate guard**: The `DRARecreate` feature gate must be explicitly enabled on both the admission controller and the updater; it is off by default.
- **Disruption budgets**: Configure `PodDisruptionBudget` objects to limit the number of pods evicted simultaneously, protecting availability during capacity rollouts.

## Implementation History

<!--
Track major milestones using absolute dates (YYYY-MM-DD):
- initial version
- significant design changes
- the first VPA release where the feature shipped
- graduation to beta / GA
-->

- 2026-MM-DD: initial version

## Alternatives

### Alternative 1: Versioned ResourceClaimTemplate manager

The initial in-house design proposed that the VPA updater would create a new `ResourceClaimTemplate` copy with the updated capacity, patch the `Deployment` to reference the new template, and clean up old template copies after pods recycled.

**Why rejected:** This approach requires the updater to write `ResourceClaimTemplate` and `Deployment` objects, expanding VPA's actuation surface significantly. It also leaves stale template copies in the cluster between eviction and pod termination. The admission-controller patching approach is simpler: it writes capacity directly into the `ResourceClaim` at the moment it is created, requires no new template objects, and reuses the existing pod eviction path without modification.

### Alternative 2: Separate DRA autoscaler

Build a new, dedicated autoscaler for DRA instead of extending VPA.

**Why rejected:** VPA already provides the recommendation storage schema (`VPA.status`), the admission webhook infrastructure, and the pod eviction loop. Duplicating this in a new controller fragments the autoscaling ecosystem and increases operational complexity for users who already run VPA for CPU/memory management. Extending VPA keeps the actuation logic in one place and reuses the existing `ResourceClaimPolicy` bounds mechanism.
