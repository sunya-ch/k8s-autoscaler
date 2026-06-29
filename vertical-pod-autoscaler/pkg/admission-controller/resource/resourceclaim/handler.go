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

package resourceclaim

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"

	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"
	podpkg "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod"
	patch "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod/patch"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/admission"
)

// metadataPathPrefix is the path prefix used by resourceClaimUpdatesPatchCalculator
// to encode routing metadata instead of a real JSON Patch path. The handler
// intercepts these and translates them into actual ResourceClaim spec paths.
const metadataPathPrefix = "/resourceClaim/"

// resourceHandler builds patches for ResourceClaim objects during admission.
// This handler intercepts ResourceClaim creation and applies VPA recommendations
// to the capacity requests before the ResourceClaim is stored in etcd.
type resourceHandler struct {
	vpaCache         *podpkg.VpaCache
	patchCalculators []patch.Calculator
}

// NewResourceHandler creates a new ResourceClaim admission handler.
func NewResourceHandler(vpaCache *podpkg.VpaCache, patchCalculators []patch.Calculator) resource_admission.Handler {
	return &resourceHandler{
		vpaCache:         vpaCache,
		patchCalculators: patchCalculators,
	}
}

// AdmissionResource returns the resource type this handler processes.
func (h *resourceHandler) AdmissionResource() admission.AdmissionResource {
	return admission.Unknown // ResourceClaim is not a standard admission resource type
}

// GroupResource returns the Group and Resource type this handler accepts.
func (h *resourceHandler) GroupResource() metav1.GroupResource {
	return metav1.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}
}

// DisallowIncorrectObjects returns whether incorrect objects should be disallowed.
func (h *resourceHandler) DisallowIncorrectObjects() bool {
	// ResourceClaims are validated by API Server
	return false
}

// GetPatches builds patches for ResourceClaim in the given admission request.
// This is called when a ResourceClaim is being created, and we need to apply
// VPA recommendations to its capacity requests before it's stored.
func (h *resourceHandler) GetPatches(ctx context.Context, ar *admissionv1.AdmissionRequest) ([]resource_admission.PatchRecord, field.ErrorList) {
	if ar.Resource.Version != "v1" {
		return nil, field.ErrorList{field.Invalid(field.NewPath("."), ar.Resource.Version, "only v1 ResourceClaims are supported")}
	}

	// Only handle CREATE operations - capacity is immutable after creation
	if ar.Operation != admissionv1.Create {
		klog.V(4).InfoS("Skipping non-CREATE operation for ResourceClaim", "operation", ar.Operation)
		return []resource_admission.PatchRecord{}, nil
	}

	raw, namespace := ar.Object.Raw, ar.Namespace
	claim := resourceapi.ResourceClaim{}
	if err := json.Unmarshal(raw, &claim); err != nil {
		return nil, field.ErrorList{field.InternalError(field.NewPath("."), err)}
	}

	if len(claim.Name) == 0 {
		claim.Name = claim.GenerateName + "%"
		claim.Namespace = namespace
	}

	klog.V(4).InfoS("Admitting ResourceClaim", "claim", klog.KObj(&claim))

	// Use the OwnerReference to find the pod name, then look up the VPA that
	// was cached when that pod was admitted.
	ownerPod := findOwnerPod(&claim)
	if ownerPod == "" {
		klog.V(4).InfoS("No owner pod found for ResourceClaim, skipping VPA processing",
			"claim", klog.KObj(&claim))
		return []resource_admission.PatchRecord{}, nil
	}

	cacheEntry := h.vpaCache.Get(namespace, ownerPod)
	if cacheEntry == nil {
		klog.V(4).InfoS("No VPA cache entry found for ResourceClaim's owner pod, skipping VPA processing",
			"claim", klog.KObj(&claim), "pod", ownerPod)
		return []resource_admission.PatchRecord{}, nil
	}

	controllingVpa := cacheEntry.Vpa

	klog.V(2).InfoS("Found VPA for ResourceClaim from cache",
		"claim", klog.KObj(&claim),
		"pod", ownerPod,
		"vpa", klog.KObj(controllingVpa))

	// Use calculators to generate patches.
	// The calculators will return patches with PatchResourceTarget() == ResourceClaim.
	patches := []resource_admission.PatchRecord{}
	for _, calculator := range h.patchCalculators {
		// Only use calculators that target ResourceClaim
		if calculator.PatchResourceTarget() != patch.ResourceClaim {
			continue
		}

		partialPatches, err := calculator.CalculatePatches(cacheEntry.Pod, controllingVpa)
		if err != nil {
			return []resource_admission.PatchRecord{}, field.ErrorList{field.InternalError(field.NewPath("."), err)}
		}
		// Translate metadata patches into real ResourceClaim spec paths.
		translated := translateMetadataPatches(partialPatches, &claim)
		patches = append(patches, translated...)
	}

	if len(patches) > 0 {
		klog.V(2).InfoS("Calculated ResourceClaim patches",
			"claim", klog.KObj(&claim), "vpa", klog.KObj(controllingVpa), "patchCount", len(patches))
	}

	return patches, nil
}

// findOwnerPodName returns a pod name from the ResourceClaim's OwnerReferences.
func findOwnerPod(claim *resourceapi.ResourceClaim) string {
	for _, ownerRef := range claim.OwnerReferences {
		if ownerRef.Kind == "Pod" && ownerRef.APIVersion == "v1" {
			klog.V(4).InfoS("Found owner pod for ResourceClaim",
				"claim", klog.KObj(claim), "pod", ownerRef.Name)
			return ownerRef.Name
		}
	}
	klog.V(4).InfoS("No pod owner reference found in ResourceClaim", "claim", klog.KObj(claim))
	return ""
}

// translateMetadataPatches converts "metadata patches" emitted by
// resourceClaimUpdatesPatchCalculator into real JSON Patch operations against
// the ResourceClaim spec.
//
// Metadata patch path format (from the calculator):
//
//	/resourceClaim/<claimName>/<deviceClassName>/<capacityName>
//
// Real patch path format:
//
//	/spec/devices/requests/<index>/exactly/capacity/requests/<capacityName>
//
// The translation:
//  1. Parses the deviceClassName and capacityName from the metadata path.
//  2. Finds the request index in the claim whose exactly.deviceClassName matches.
//  3. Determines whether to use "add" or "replace" based on whether the
//     capacity entry already exists in the incoming claim.
//  4. Returns the equivalent real patch. Non-metadata patches are passed through.
func translateMetadataPatches(patches []resource_admission.PatchRecord, claim *resourceapi.ResourceClaim) []resource_admission.PatchRecord {
	result := make([]resource_admission.PatchRecord, 0, len(patches))
	for _, p := range patches {
		if !strings.HasPrefix(p.Path, metadataPathPrefix) {
			// Not a metadata patch — pass through unchanged.
			result = append(result, p)
			continue
		}

		// Strip the prefix and split: <claimName>/<deviceClassName>/<capacityName>
		// capacityName itself contains a slash (e.g. "vgpu.example.com/memory"), so
		// we split into exactly 3 parts using the first two slashes only.
		rest := strings.TrimPrefix(p.Path, metadataPathPrefix)
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) != 3 {
			klog.V(2).InfoS("Skipping malformed metadata patch path", "path", p.Path)
			continue
		}
		deviceClassName := parts[1]
		capacityName := parts[2]

		// Find the request index whose exactly.deviceClassName matches.
		reqIdx := -1
		for i, req := range claim.Spec.Devices.Requests {
			if req.Exactly != nil && req.Exactly.DeviceClassName == deviceClassName {
				reqIdx = i
				break
			}
		}
		if reqIdx == -1 {
			klog.V(2).InfoS("No matching DeviceRequest found for deviceClassName, skipping patch",
				"claim", klog.KObj(claim), "deviceClassName", deviceClassName)
			continue
		}

		req := &claim.Spec.Devices.Requests[reqIdx]

		// Decide op and path based on whether the capacity map entry exists.
		// If capacity or its requests map is nil we must "add" the missing layer(s).
		// If the specific key already exists we must "replace" it.
		var op, patchPath string
		switch {
		case req.Exactly.Capacity == nil:
			// Capacity object absent: add the whole capacity object including the key.
			capPath := fmt.Sprintf("/spec/devices/requests/%d/exactly/capacity", reqIdx)
			result = append(result, resource_admission.PatchRecord{
				Op:   "add",
				Path: capPath,
				Value: map[string]interface{}{
					"requests": map[string]interface{}{capacityName: p.Value},
				},
			})
			klog.V(4).InfoS("Translated metadata patch (add capacity)",
				"originalPath", p.Path, "realPath", capPath)
			continue

		case req.Exactly.Capacity.Requests == nil:
			// Capacity object exists but requests map is nil: add the map.
			patchPath = fmt.Sprintf("/spec/devices/requests/%d/exactly/capacity/requests", reqIdx)
			result = append(result, resource_admission.PatchRecord{
				Op:    "add",
				Path:  patchPath,
				Value: map[string]interface{}{capacityName: p.Value},
			})
			klog.V(4).InfoS("Translated metadata patch (add requests map)",
				"originalPath", p.Path, "realPath", patchPath)
			continue

		default:
			// Both capacity and requests map exist. Use "replace" if the key is
			// present, "add" if not — JSON Patch requires "add" for new map keys.
			escapedName := jsonPointerEscape(capacityName)
			patchPath = fmt.Sprintf("/spec/devices/requests/%d/exactly/capacity/requests/%s", reqIdx, escapedName)
			if _, exists := req.Exactly.Capacity.Requests[resourceapi.QualifiedName(capacityName)]; exists {
				op = "replace"
			} else {
				op = "add"
			}
		}

		result = append(result, resource_admission.PatchRecord{
			Op:    op,
			Path:  patchPath,
			Value: p.Value,
		})
		klog.V(4).InfoS("Translated metadata patch",
			"originalPath", p.Path, "realPath", patchPath, "op", op)
	}
	return result
}

// jsonPointerEscape escapes a string for use as a JSON Pointer token (RFC 6901).
// '~' must be escaped as '~0' and '/' must be escaped as '~1'.
func jsonPointerEscape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}
