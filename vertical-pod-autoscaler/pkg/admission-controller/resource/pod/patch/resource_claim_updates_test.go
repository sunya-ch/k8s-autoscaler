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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
	vpa_api "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
)

func TestPatchResourceTarget(t *testing.T) {
	calculator := NewResourceClaimUpdatesCalculator()
	assert.Equal(t, ResourceClaim, calculator.PatchResourceTarget())
}

func TestCalculatePatches(t *testing.T) {
	claimTemplateName := "gpu-claim"
	differentClaimTemplate := "different-claim"

	tests := []struct {
		name          string
		pod           *corev1.Pod
		vpa           *vpa_types.VerticalPodAutoscaler
		expectPatches []resource_admission.PatchRecord
		expectError   bool
	}{
		{
			name: "No ResourceClaimPolicies in VPA",
			pod:  test.Pod().WithName("test-pod").Get(),
			vpa: &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vpa"},
			},
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name: "No ResourceClaims in Pod",
			pod:  test.Pod().WithName("test-pod").Get(),
			vpa: &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vpa"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName:    "gpu-claim",
								DeviceClassName:      "gpu.example.com",
								ControlledCapacities: []resourceapi.QualifiedName{"memory"},
							},
						},
					},
				},
			},
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name: "No Container Recommendations",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &claimTemplateName},
				}
				return pod
			}(),
			vpa: &vpa_types.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vpa"},
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName:    "gpu-claim",
								DeviceClassName:      "gpu.example.com",
								ControlledCapacities: []resourceapi.QualifiedName{"memory"},
							},
						},
					},
				},
			},
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name: "No DRA Capacities in Recommendations - only standard resources",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &claimTemplateName},
				}
				return pod
			}(),
			vpa: func() *vpa_types.VerticalPodAutoscaler {
				vpa := test.VerticalPodAutoscaler().
					WithName("test-vpa").
					WithContainer("container1").
					WithTarget("100m", "200Mi").
					Get()
				vpa.Spec.ResourcePolicy = &vpa_types.PodResourcePolicy{
					ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
						{
							ClaimTemplateName:    "gpu-claim",
							DeviceClassName:      "gpu.example.com",
							ControlledCapacities: []resourceapi.QualifiedName{"memory"},
						},
					},
				}
				return vpa
			}(),
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name: "Success - Single DRA Capacity",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &claimTemplateName},
				}
				return pod
			}(),
			vpa: func() *vpa_types.VerticalPodAutoscaler {
				vpa := test.VerticalPodAutoscaler().
					WithName("test-vpa").
					WithContainer("container1").
					WithTarget("100m", "200Mi").
					Get()
				vpa.Spec.ResourcePolicy = &vpa_types.PodResourcePolicy{
					ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
						{
							ClaimTemplateName:    "gpu-claim",
							DeviceClassName:      "gpu.example.com",
							ControlledCapacities: []resourceapi.QualifiedName{"memory"},
						},
					},
				}
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/memory")] = resource.MustParse("8Gi")
				return vpa
			}(),
			expectPatches: []resource_admission.PatchRecord{
				{
					Op:    "replace",
					Path:  "/resourceClaim/gpu/gpu.example.com/memory",
					Value: "8Gi",
				},
			},
		},
		{
			name: "Success - Multiple DRA Capacities",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &claimTemplateName},
				}
				return pod
			}(),
			vpa: func() *vpa_types.VerticalPodAutoscaler {
				vpa := test.VerticalPodAutoscaler().
					WithName("test-vpa").
					WithContainer("container1").
					WithTarget("100m", "200Mi").
					Get()
				vpa.Spec.ResourcePolicy = &vpa_types.PodResourcePolicy{
					ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
						{
							ClaimTemplateName: "gpu-claim",
							DeviceClassName:   "gpu.example.com",
							ControlledCapacities: []resourceapi.QualifiedName{
								"memory",
								"cores",
							},
						},
					},
				}
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/memory")] = resource.MustParse("8Gi")
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/cores")] = resource.MustParse("4")
				return vpa
			}(),
			expectPatches: []resource_admission.PatchRecord{
				{
					Op:    "replace",
					Path:  "/resourceClaim/gpu/gpu.example.com/memory",
					Value: "8Gi",
				},
				{
					Op:    "replace",
					Path:  "/resourceClaim/gpu/gpu.example.com/cores",
					Value: "4",
				},
			},
		},
		{
			name: "No Matching Claim Template",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &differentClaimTemplate},
				}
				return pod
			}(),
			vpa: func() *vpa_types.VerticalPodAutoscaler {
				vpa := test.VerticalPodAutoscaler().
					WithName("test-vpa").
					WithContainer("container1").
					WithTarget("100m", "200Mi").
					Get()
				vpa.Spec.ResourcePolicy = &vpa_types.PodResourcePolicy{
					ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
						{
							ClaimTemplateName:    "gpu-claim",
							DeviceClassName:      "gpu.example.com",
							ControlledCapacities: []resourceapi.QualifiedName{"memory"},
						},
					},
				}
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/memory")] = resource.MustParse("8Gi")
				return vpa
			}(),
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name: "Only Controlled Capacities Patched",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &claimTemplateName},
				}
				return pod
			}(),
			vpa: func() *vpa_types.VerticalPodAutoscaler {
				vpa := test.VerticalPodAutoscaler().
					WithName("test-vpa").
					WithContainer("container1").
					WithTarget("100m", "200Mi").
					Get()
				vpa.Spec.ResourcePolicy = &vpa_types.PodResourcePolicy{
					ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
						{
							ClaimTemplateName:    "gpu-claim",
							DeviceClassName:      "gpu.example.com",
							ControlledCapacities: []resourceapi.QualifiedName{"memory"},
						},
					},
				}
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/memory")] = resource.MustParse("8Gi")
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/cores")] = resource.MustParse("4")
				return vpa
			}(),
			expectPatches: []resource_admission.PatchRecord{
				{
					Op:    "replace",
					Path:  "/resourceClaim/gpu/gpu.example.com/memory",
					Value: "8Gi", // Only memory is in controlled capacities, cores is ignored
				},
			},
		},
		{
			name: "Multiple Containers with DRA Capacities - uses last container's recommendation",
			pod: func() *corev1.Pod {
				pod := test.Pod().WithName("test-pod").Get()
				pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: &claimTemplateName},
				}
				return pod
			}(),
			vpa: func() *vpa_types.VerticalPodAutoscaler {
				vpa := test.VerticalPodAutoscaler().
					WithName("test-vpa").
					WithContainer("container1").
					WithTarget("100m", "200Mi").
					Get()
				vpa.Spec.ResourcePolicy = &vpa_types.PodResourcePolicy{
					ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
						{
							ClaimTemplateName:    "gpu-claim",
							DeviceClassName:      "gpu.example.com",
							ControlledCapacities: []resourceapi.QualifiedName{"memory"},
						},
					},
				}
				// Add DRA capacity to first container
				vpa.Status.Recommendation.ContainerRecommendations[0].Target[corev1.ResourceName("gpu.example.com/memory")] = resource.MustParse("8Gi")
				// Add a second container recommendation manually
				vpa.Status.Recommendation.ContainerRecommendations = append(
					vpa.Status.Recommendation.ContainerRecommendations,
					vpa_types.RecommendedContainerResources{
						ContainerName: "container2",
						Target: corev1.ResourceList{
							corev1.ResourceCPU:                            resource.MustParse("200m"),
							corev1.ResourceMemory:                         resource.MustParse("400Mi"),
							corev1.ResourceName("gpu.example.com/memory"): resource.MustParse("16Gi"),
						},
					},
				)
				return vpa
			}(),
			expectPatches: []resource_admission.PatchRecord{
				{
					Op:    "replace",
					Path:  "/resourceClaim/gpu/gpu.example.com/memory",
					Value: "16Gi", // Uses last container's recommendation when multiple containers have same DRA capacity
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calculator := NewResourceClaimUpdatesCalculator()
			patches, err := calculator.CalculatePatches(tc.pod, tc.vpa)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, len(tc.expectPatches), len(patches), "Number of patches should match")

			if len(tc.expectPatches) > 0 {
				// For tests with multiple patches, check that all expected patches are present
				for _, expectedPatch := range tc.expectPatches {
					found := false
					for _, actualPatch := range patches {
						if actualPatch.Path == expectedPatch.Path &&
							actualPatch.Op == expectedPatch.Op &&
							actualPatch.Value == expectedPatch.Value {
							found = true
							break
						}
					}
					assert.True(t, found, "Expected patch not found: %+v", expectedPatch)
				}
			}
		})
	}
}

func TestExtractDRACapacities(t *testing.T) {
	tests := []struct {
		name               string
		containerRecs      []vpa_types.RecommendedContainerResources
		expectedCount      int
		expectedCapacities []corev1.ResourceName
	}{
		{
			name: "Extract DRA Capacities - Mixed Resources",
			containerRecs: []vpa_types.RecommendedContainerResources{
				{
					ContainerName: "container1",
					Target: corev1.ResourceList{
						corev1.ResourceCPU:                            resource.MustParse("100m"),
						corev1.ResourceMemory:                         resource.MustParse("200Mi"),
						corev1.ResourceName("gpu.example.com/memory"): resource.MustParse("8Gi"),
						corev1.ResourceName("gpu.example.com/cores"):  resource.MustParse("4"),
					},
				},
			},
			expectedCount: 2,
			expectedCapacities: []corev1.ResourceName{
				"gpu.example.com/memory",
				"gpu.example.com/cores",
			},
		},
		{
			name: "No Extended Resources",
			containerRecs: []vpa_types.RecommendedContainerResources{
				{
					ContainerName: "container1",
					Target: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("200Mi"),
					},
				},
			},
			expectedCount:      0,
			expectedCapacities: []corev1.ResourceName{},
		},
		{
			name: "Nil Target",
			containerRecs: []vpa_types.RecommendedContainerResources{
				{
					ContainerName: "container1",
					Target:        nil,
				},
			},
			expectedCount:      0,
			expectedCapacities: []corev1.ResourceName{},
		},
		{
			name: "Multiple Containers with DRA Capacities",
			containerRecs: []vpa_types.RecommendedContainerResources{
				{
					ContainerName: "container1",
					Target: corev1.ResourceList{
						corev1.ResourceName("gpu.example.com/memory"): resource.MustParse("8Gi"),
					},
				},
				{
					ContainerName: "container2",
					Target: corev1.ResourceList{
						corev1.ResourceName("gpu.example.com/cores"): resource.MustParse("4"),
					},
				},
			},
			expectedCount: 2,
			expectedCapacities: []corev1.ResourceName{
				"gpu.example.com/memory",
				"gpu.example.com/cores",
			},
		},
		{
			name:               "Empty Container Recommendations",
			containerRecs:      []vpa_types.RecommendedContainerResources{},
			expectedCount:      0,
			expectedCapacities: []corev1.ResourceName{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capacities := vpa_api.ExtractDRACapacitiesFromRecommendations(tc.containerRecs)

			assert.Equal(t, tc.expectedCount, len(capacities), "Number of extracted capacities should match")

			for _, expectedCap := range tc.expectedCapacities {
				assert.Contains(t, capacities, expectedCap, "Expected capacity not found: %s", expectedCap)
			}

			// Verify no standard resources are included
			assert.NotContains(t, capacities, corev1.ResourceCPU)
			assert.NotContains(t, capacities, corev1.ResourceMemory)
		})
	}
}
