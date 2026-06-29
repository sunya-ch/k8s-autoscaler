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

package patch

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_api "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
)

// resourceClaimUpdatesPatchCalculator calculates patches for ResourceClaim objects
// referenced by pods. Unlike container resources which are patched on the pod itself,
// ResourceClaims are separate Kubernetes objects that need to be patched directly.
//
// DRA capacity recommendations are stored in ContainerRecommendations[].Target as
// extended resources (e.g., "example.com/device-capacity"). This calculator extracts
// those recommendations and creates patches for the corresponding ResourceClaim objects.
type resourceClaimUpdatesPatchCalculator struct {
}

// NewResourceClaimUpdatesCalculator returns a calculator for ResourceClaim update patches.
// This calculator generates patches that will be applied to ResourceClaim objects,
// not to the pod itself.
func NewResourceClaimUpdatesCalculator() Calculator {
	return &resourceClaimUpdatesPatchCalculator{}
}

// PatchResourceTarget returns ResourceClaim to indicate these patches should be
// applied to ResourceClaim objects, not the pod resource.
func (*resourceClaimUpdatesPatchCalculator) PatchResourceTarget() PatchResourceTarget {
	return ResourceClaim
}

// CalculatePatches calculates JSON patches for ResourceClaim objects based on VPA recommendations.
// It extracts DRA capacity recommendations from ContainerRecommendations[].Target ResourceList
// and matches them with ResourceClaimPolicies to generate patches for ResourceClaim objects.
//
// Returns a list of PatchRecords where each record contains:
// - Op: "replace" operation
// - Path: Identifies the ResourceClaim template name and capacity name
// - Value: The new capacity value from VPA recommendation
func (c *resourceClaimUpdatesPatchCalculator) CalculatePatches(pod *corev1.Pod, vpa *vpa_types.VerticalPodAutoscaler) ([]resource_admission.PatchRecord, error) {
	result := []resource_admission.PatchRecord{}

	// Check if VPA has ResourceClaimPolicies configured
	if vpa.Spec.ResourcePolicy == nil || len(vpa.Spec.ResourcePolicy.ResourceClaimPolicies) == 0 {
		klog.V(4).InfoS("VPA has no ResourceClaimPolicies configured", "vpa", klog.KObj(vpa))
		return result, nil
	}

	// Check if pod has ResourceClaims
	if len(pod.Spec.ResourceClaims) == 0 {
		klog.V(4).InfoS("Pod has no ResourceClaims", "pod", klog.KObj(pod))
		return result, nil
	}

	// Check if VPA has recommendations
	if vpa.Status.Recommendation == nil || len(vpa.Status.Recommendation.ContainerRecommendations) == 0 {
		klog.V(4).InfoS("VPA has no container recommendations", "vpa", klog.KObj(vpa))
		return result, nil
	}

	// Extract DRA capacity recommendations from container recommendations
	// DRA capacities are stored as extended resources in the Target ResourceList
	draCapacities := vpa_api.ExtractDRACapacitiesFromRecommendations(vpa.Status.Recommendation.ContainerRecommendations)
	if len(draCapacities) == 0 {
		klog.V(4).InfoS("No DRA capacity recommendations found in container recommendations", "vpa", klog.KObj(vpa))
		return result, nil
	}

	// Apply ResourceClaimPolicy capping to the extracted capacities
	// This ensures recommendations respect minAllowed/maxAllowed constraints
	cappedCapacities, annotations, err := vpa_api.ApplyResourceClaimPolicies(draCapacities, vpa.Spec.ResourcePolicy)
	if err != nil {
		klog.ErrorS(err, "Failed to apply ResourceClaimPolicy capping", "vpa", klog.KObj(vpa))
		return result, err
	}

	// Log capping actions if any occurred
	if len(annotations) > 0 {
		for claimName, claimAnnotations := range annotations {
			klog.V(2).InfoS("Applied ResourceClaimPolicy capping",
				"vpa", klog.KObj(vpa),
				"claimName", claimName,
				"cappingActions", claimAnnotations)
		}
	}

	// For each ResourceClaimPolicy, match with pod's ResourceClaims and create patches
	// Use the capped capacities instead of the original draCapacities
	for _, policy := range vpa.Spec.ResourcePolicy.ResourceClaimPolicies {
		// Find the pod's ResourceClaim that matches this policy's ClaimTemplateName
		var podClaim *corev1.PodResourceClaim
		for i := range pod.Spec.ResourceClaims {
			if pod.Spec.ResourceClaims[i].ResourceClaimTemplateName != nil &&
				*pod.Spec.ResourceClaims[i].ResourceClaimTemplateName == policy.ClaimTemplateName {
				podClaim = &pod.Spec.ResourceClaims[i]
				break
			}
		}

		if podClaim == nil {
			klog.V(4).InfoS("No matching ResourceClaim found in pod for policy",
				"pod", klog.KObj(pod),
				"claimTemplateName", policy.ClaimTemplateName)
			continue
		}

		// Create patches for this ResourceClaim based on controlled capacities
		// Use cappedCapacities which have been adjusted to respect policy constraints
		patches := c.createResourceClaimPatches(podClaim.Name, policy, cappedCapacities)
		result = append(result, patches...)
	}

	if len(result) > 0 {
		klog.V(2).InfoS("Calculated ResourceClaim patches",
			"pod", klog.KObj(pod), "vpa", klog.KObj(vpa), "patchCount", len(result))
	}

	return result, nil
}

// createResourceClaimPatches creates JSON patch records for a single ResourceClaim.
// The patches will be applied to the ResourceClaim object's spec.devices.requests[<index>]/exactly/capacity
// following the DRA ResourceClaim API structure.
//
// Note: This calculator is used by the updater (not admission controller), so it needs
// to encode metadata that allows the updater's restriction logic to:
// 1. Identify which ResourceClaim object to patch (via claimName)
// 2. Find the correct request index (by matching deviceClassName)
// 3. Apply the capacity update
//
// The actual path construction happens in the updater's restriction logic when it
// has access to the ResourceClaim object to determine the request index.
func (c *resourceClaimUpdatesPatchCalculator) createResourceClaimPatches(
	claimName string,
	policy vpa_types.ResourceClaimPolicy,
	draCapacities map[corev1.ResourceName]corev1.ResourceList,
) []resource_admission.PatchRecord {
	patches := []resource_admission.PatchRecord{}

	// For each controlled capacity in the policy, create a patch if we have a recommendation.
	// ControlledCapacities entries are bare capacity names (e.g. "memory"); recommender keys
	// are full names (e.g. "deviceClass/memory"), so reconstruct for the draCapacities lookup.
	for _, controlledCapacity := range policy.ControlledCapacities {
		fullResourceName := corev1.ResourceName(policy.DeviceClassName + "/" + string(controlledCapacity))

		// Check if we have a recommendation for this capacity
		if capacityList, found := draCapacities[fullResourceName]; found {
			for _, quantity := range capacityList {
				// Create a patch record with metadata encoded in the path.
				// Format: /resourceClaim/<claimName>/<deviceClassName>/<bareCapacityName>
				//
				// The handler's translateMetadataPatches will:
				// 1. Parse this metadata path
				// 2. Find request index where deviceClassName matches
				// 3. Construct actual API path: /spec/devices/requests/<index>/exactly/capacity/requests/<bareCapacityName>
				// 4. Apply the patch to the ResourceClaim object
				patch := resource_admission.PatchRecord{
					Op:    "replace",
					Path:  fmt.Sprintf("/resourceClaim/%s/%s/%s", claimName, policy.DeviceClassName, controlledCapacity),
					Value: quantity.String(),
				}
				patches = append(patches, patch)

				klog.V(4).InfoS("Created ResourceClaim patch metadata",
					"claimName", claimName,
					"deviceClassName", policy.DeviceClassName,
					"capacityName", controlledCapacity,
					"capacity", quantity.String())
			}
		}
	}

	return patches
}
