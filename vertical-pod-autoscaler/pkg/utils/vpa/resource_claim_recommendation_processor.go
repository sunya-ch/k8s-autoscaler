/*
Copyright 2024 The Kubernetes Authors.

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

package api

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/klog/v2"
)

// resourceClaimRecommendationProcessor implements RecommendationProcessor for DRA ResourceClaim capping
type resourceClaimRecommendationProcessor struct{}

// NewResourceClaimRecommendationProcessor creates a new processor for applying ResourceClaimPolicy capping
func NewResourceClaimRecommendationProcessor() RecommendationProcessor {
	return &resourceClaimRecommendationProcessor{}
}

// Apply implements RecommendationProcessor interface for ResourceClaim capping.
// It applies ResourceClaimPolicy min/max constraints to DRA capacity recommendations.
func (r *resourceClaimRecommendationProcessor) Apply(
	vpa *vpa_types.VerticalPodAutoscaler,
	pod *corev1.Pod,
) (*vpa_types.RecommendedPodResources, ContainerToAnnotationsMap, error) {

	// If no VPA or no recommendation, return nil (no changes)
	if vpa == nil || vpa.Status.Recommendation == nil {
		return nil, nil, nil
	}

	// Check if VPA has ResourceClaimPolicies
	if vpa.Spec.ResourcePolicy == nil || len(vpa.Spec.ResourcePolicy.ResourceClaimPolicies) == 0 {
		// No DRA policies, return original recommendations unchanged
		return vpa.Status.Recommendation, nil, nil
	}

	// Extract DRA capacities from container recommendations
	// DRA capacities are stored as extended resources (with "/" in name) in Target
	draCapacities := ExtractDRACapacitiesFromRecommendations(vpa.Status.Recommendation.ContainerRecommendations)

	if len(draCapacities) == 0 {
		// No DRA recommendations to cap
		return vpa.Status.Recommendation, nil, nil
	}

	// Apply ResourceClaimPolicy capping
	cappedCapacities, annotations, err := ApplyResourceClaimPolicies(
		draCapacities,
		vpa.Spec.ResourcePolicy,
	)
	if err != nil {
		return nil, nil, err
	}

	// Create a deep copy of the recommendation to modify
	cappedRecommendation := vpa.Status.Recommendation.DeepCopy()

	// Update container recommendations with capped DRA values
	for i := range cappedRecommendation.ContainerRecommendations {
		containerRec := &cappedRecommendation.ContainerRecommendations[i]

		if containerRec.Target == nil {
			continue
		}

		// Update each extended resource (DRA capacity) with capped value
		for resourceName := range containerRec.Target {
			// Only process extended resources (DRA capacities have "/" in name)
			if !strings.Contains(string(resourceName), "/") {
				continue
			}

			// Find capped value for this resource
			if cappedList, exists := cappedCapacities[resourceName]; exists {
				if cappedValue, found := cappedList[resourceName]; found {
					containerRec.Target[resourceName] = cappedValue

					// Also update UncappedTarget if it exists
					if containerRec.UncappedTarget != nil {
						if _, hasUncapped := containerRec.UncappedTarget[resourceName]; hasUncapped {
							// Keep original uncapped value, only update Target
							// UncappedTarget shows what was recommended before capping
						}
					}
				}
			}
		}
	}

	return cappedRecommendation, annotations, nil
}

// ApplyResourceClaimPolicies applies all ResourceClaimPolicies to a map of DRA capacities.
// The input is a map from resource name to capacity recommendations (as extracted from VPA recommendations).
// Returns updated capacities and annotations for each resource.
func ApplyResourceClaimPolicies(
	draCapacities map[corev1.ResourceName]corev1.ResourceList,
	resourcePolicy *vpa_types.PodResourcePolicy,
) (map[corev1.ResourceName]corev1.ResourceList, map[string][]string, error) {
	if len(draCapacities) == 0 {
		return draCapacities, nil, nil
	}

	if resourcePolicy == nil || len(resourcePolicy.ResourceClaimPolicies) == 0 {
		return draCapacities, nil, nil
	}

	cappedCapacities := make(map[corev1.ResourceName]corev1.ResourceList)
	allAnnotations := make(map[string][]string)

	// For each DRA capacity, find matching policy and apply capping.
	// draCapacities keys are full names ("deviceClass/capacity"); ControlledCapacities
	// entries are bare names ("capacity"), so we reconstruct the full name for comparison.
	for resourceName, capacities := range draCapacities {
		// Find the policy that controls this resource
		var matchingPolicy *vpa_types.ResourceClaimPolicy
		for i := range resourcePolicy.ResourceClaimPolicies {
			policy := &resourcePolicy.ResourceClaimPolicies[i]
			for _, controlledCap := range policy.ControlledCapacities {
				fullName := policy.DeviceClassName + "/" + string(controlledCap)
				if string(resourceName) == fullName {
					matchingPolicy = policy
					break
				}
			}
			if matchingPolicy != nil {
				break
			}
		}

		if matchingPolicy == nil {
			// No policy for this resource, keep original
			cappedCapacities[resourceName] = capacities
			continue
		}

		capped, annotations, err := applyResourceClaimPolicy(capacities, matchingPolicy)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to apply policy for resource %s: %w", resourceName, err)
		}

		cappedCapacities[resourceName] = capped
		if len(annotations) > 0 {
			claimName := matchingPolicy.ClaimTemplateName
			allAnnotations[claimName] = append(allAnnotations[claimName], annotations...)
		}
	}

	return cappedCapacities, allAnnotations, nil
}

// applyResourceClaimPolicy applies VPA ResourceClaimPolicy constraints to capacity recommendations.
// It caps the recommended capacities to respect minAllowed and maxAllowed values.
//
// MinAllowed and MaxAllowed keys are bare capacity names (e.g. "memory"); capacities keys are
// full names from the recommender (e.g. "deviceClass/memory"). The full name is reconstructed
// using policy.DeviceClassName for the lookup.
//
// Returns the capped capacities, annotations describing what was capped, and any error.
func applyResourceClaimPolicy(
	capacities corev1.ResourceList,
	policy *vpa_types.ResourceClaimPolicy,
) (corev1.ResourceList, []string, error) {
	if policy == nil {
		return capacities, nil, nil
	}

	if capacities == nil {
		return nil, nil, fmt.Errorf("cannot apply policy to nil capacities")
	}

	cappedCapacities := capacities.DeepCopy()
	annotations := make([]string, 0)

	// Apply minAllowed constraints.
	// MinAllowed keys are bare names; map to full names for the capacities lookup.
	if policy.MinAllowed != nil {
		for bareCapName, minValue := range policy.MinAllowed {
			fullCapName := corev1.ResourceName(policy.DeviceClassName + "/" + string(bareCapName))
			if currentValue, exists := cappedCapacities[fullCapName]; exists {
				if !minValue.IsZero() && currentValue.Cmp(minValue) < 0 {
					cappedCapacities[fullCapName] = minValue.DeepCopy()
					annotations = append(annotations, fmt.Sprintf("%s capped to minAllowed", bareCapName))
					klog.V(4).InfoS("Capped capacity to minAllowed",
						"capacityName", bareCapName,
						"original", currentValue.String(),
						"capped", minValue.String())
				}
			}
		}
	}

	// Apply maxAllowed constraints.
	// MaxAllowed keys are bare names; map to full names for the capacities lookup.
	if policy.MaxAllowed != nil {
		for bareCapName, maxValue := range policy.MaxAllowed {
			fullCapName := corev1.ResourceName(policy.DeviceClassName + "/" + string(bareCapName))
			if currentValue, exists := cappedCapacities[fullCapName]; exists {
				if !maxValue.IsZero() && currentValue.Cmp(maxValue) > 0 {
					cappedCapacities[fullCapName] = maxValue.DeepCopy()
					annotations = append(annotations, fmt.Sprintf("%s capped to maxAllowed", bareCapName))
					klog.V(4).InfoS("Capped capacity to maxAllowed",
						"capacityName", bareCapName,
						"original", currentValue.String(),
						"capped", maxValue.String())
				}
			}
		}
	}

	return cappedCapacities, annotations, nil
}

// ExtractDRACapacitiesFromRecommendations extracts DRA capacity recommendations from container recommendations.
// DRA capacities are stored as extended resources (e.g., "example.com/device-capacity")
// in the Target ResourceList of container recommendations.
// Extended resources are identified by having "/" in the resource name.
//
// This is a utility function that can be used by both the RecommendationProcessor
// and the DRA patch calculator to extract DRA capacities from VPA recommendations.
func ExtractDRACapacitiesFromRecommendations(
	containerRecs []vpa_types.RecommendedContainerResources,
) map[corev1.ResourceName]corev1.ResourceList {
	capacities := make(map[corev1.ResourceName]corev1.ResourceList)

	for _, containerRec := range containerRecs {
		if containerRec.Target == nil {
			continue
		}

		// Look for extended resources (resources with "/" in the name)
		// These represent DRA capacities
		for resourceName, quantity := range containerRec.Target {
			// Extended resources have a domain prefix (e.g., "example.com/capacity")
			if strings.Contains(string(resourceName), "/") {
				if capacities[resourceName] == nil {
					capacities[resourceName] = corev1.ResourceList{}
				}
				capacities[resourceName][resourceName] = quantity
			}
		}
	}

	return capacities
}
