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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

func TestResourceClaimRecommendationProcessor_Apply(t *testing.T) {
	gpuMemory := corev1.ResourceName("gpu.example.com/memory")
	gpuCount := corev1.ResourceName("gpu.example.com/count")

	tests := []struct {
		name              string
		vpa               *vpa_types.VerticalPodAutoscaler
		pod               *corev1.Pod
		expectedTarget    map[corev1.ResourceName]resource.Quantity
		expectAnnotations bool
		expectError       bool
		expectNilResult   bool
	}{
		{
			name:            "Nil VPA returns nil",
			vpa:             nil,
			pod:             &corev1.Pod{},
			expectNilResult: true,
		},
		{
			name: "VPA without recommendation returns nil",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Status: vpa_types.VerticalPodAutoscalerStatus{},
			},
			pod:             &corev1.Pod{},
			expectNilResult: true,
		},
		{
			name: "VPA without ResourceClaimPolicies returns original recommendation",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{},
				},
				Status: vpa_types.VerticalPodAutoscalerStatus{
					Recommendation: &vpa_types.RecommendedPodResources{
						ContainerRecommendations: []vpa_types.RecommendedContainerResources{
							{
								ContainerName: "test-container",
								Target: corev1.ResourceList{
									gpuMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{},
			expectedTarget: map[corev1.ResourceName]resource.Quantity{
				gpuMemory: resource.MustParse("8Gi"),
			},
		},
		{
			name: "No DRA capacities in recommendation returns original",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								MinAllowed: corev1.ResourceList{
									gpuMemory: resource.MustParse("4Gi"),
								},
							},
						},
					},
				},
				Status: vpa_types.VerticalPodAutoscalerStatus{
					Recommendation: &vpa_types.RecommendedPodResources{
						ContainerRecommendations: []vpa_types.RecommendedContainerResources{
							{
								ContainerName: "test-container",
								Target: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{},
			expectedTarget: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		{
			name: "Apply minAllowed capping to DRA capacity",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName:    "gpu-claim",
								DeviceClassName:      "gpu.example.com",
								ControlledCapacities: []resourceapi.QualifiedName{"memory"},
								MinAllowed: corev1.ResourceList{
									"memory": resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
				Status: vpa_types.VerticalPodAutoscalerStatus{
					Recommendation: &vpa_types.RecommendedPodResources{
						ContainerRecommendations: []vpa_types.RecommendedContainerResources{
							{
								ContainerName: "test-container",
								Target: corev1.ResourceList{
									gpuMemory: resource.MustParse("4Gi"), // Below min
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{},
			expectedTarget: map[corev1.ResourceName]resource.Quantity{
				gpuMemory: resource.MustParse("8Gi"), // Capped to min
			},
			expectAnnotations: true,
		},
		{
			name: "Apply maxAllowed capping to DRA capacity",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName:    "gpu-claim",
								DeviceClassName:      "gpu.example.com",
								ControlledCapacities: []resourceapi.QualifiedName{"memory"},
								MaxAllowed: corev1.ResourceList{
									"memory": resource.MustParse("16Gi"),
								},
							},
						},
					},
				},
				Status: vpa_types.VerticalPodAutoscalerStatus{
					Recommendation: &vpa_types.RecommendedPodResources{
						ContainerRecommendations: []vpa_types.RecommendedContainerResources{
							{
								ContainerName: "test-container",
								Target: corev1.ResourceList{
									gpuMemory: resource.MustParse("32Gi"), // Above max
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{},
			expectedTarget: map[corev1.ResourceName]resource.Quantity{
				gpuMemory: resource.MustParse("16Gi"), // Capped to max
			},
			expectAnnotations: true,
		},
		{
			name: "Apply capping to multiple DRA capacities",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName: "gpu-claim",
								DeviceClassName:   "gpu.example.com",
								ControlledCapacities: []resourceapi.QualifiedName{
									"memory",
									"count",
								},
								MinAllowed: corev1.ResourceList{
									"memory": resource.MustParse("8Gi"),
									"count":  resource.MustParse("2"),
								},
								MaxAllowed: corev1.ResourceList{
									"memory": resource.MustParse("32Gi"),
									"count":  resource.MustParse("8"),
								},
							},
						},
					},
				},
				Status: vpa_types.VerticalPodAutoscalerStatus{
					Recommendation: &vpa_types.RecommendedPodResources{
						ContainerRecommendations: []vpa_types.RecommendedContainerResources{
							{
								ContainerName: "test-container",
								Target: corev1.ResourceList{
									gpuMemory: resource.MustParse("4Gi"), // Below min
									gpuCount:  resource.MustParse("16"),  // Above max
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{},
			expectedTarget: map[corev1.ResourceName]resource.Quantity{
				gpuMemory: resource.MustParse("8Gi"), // Capped to min
				gpuCount:  resource.MustParse("8"),   // Capped to max
			},
			expectAnnotations: true,
		},
		{
			name: "Mixed container and DRA resources - only DRA capped",
			vpa: &vpa_types.VerticalPodAutoscaler{
				Spec: vpa_types.VerticalPodAutoscalerSpec{
					ResourcePolicy: &vpa_types.PodResourcePolicy{
						ResourceClaimPolicies: []vpa_types.ResourceClaimPolicy{
							{
								ClaimTemplateName:    "gpu-claim",
								DeviceClassName:      "gpu.example.com",
								ControlledCapacities: []resourceapi.QualifiedName{"memory"},
								MaxAllowed: corev1.ResourceList{
									"memory": resource.MustParse("16Gi"),
								},
							},
						},
					},
				},
				Status: vpa_types.VerticalPodAutoscalerStatus{
					Recommendation: &vpa_types.RecommendedPodResources{
						ContainerRecommendations: []vpa_types.RecommendedContainerResources{
							{
								ContainerName: "test-container",
								Target: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									gpuMemory:             resource.MustParse("32Gi"), // Above max
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{},
			expectedTarget: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU:    resource.MustParse("500m"), // Unchanged
				corev1.ResourceMemory: resource.MustParse("1Gi"),  // Unchanged
				gpuMemory:             resource.MustParse("16Gi"), // Capped
			},
			expectAnnotations: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := NewResourceClaimRecommendationProcessor()

			result, annotations, err := processor.Apply(tt.vpa, tt.pod)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			if tt.expectNilResult {
				assert.Nil(t, result)
				return
			}

			assert.NotNil(t, result)
			assert.NotEmpty(t, result.ContainerRecommendations)

			// Check target resources
			actualTarget := result.ContainerRecommendations[0].Target
			for resourceName, expectedQty := range tt.expectedTarget {
				actualQty, exists := actualTarget[resourceName]
				assert.True(t, exists, "Resource %s should exist in target", resourceName)
				assert.True(t, expectedQty.Equal(actualQty),
					"Resource %s: expected %s, got %s",
					resourceName, expectedQty.String(), actualQty.String())
			}

			// Check annotations
			if tt.expectAnnotations {
				assert.NotNil(t, annotations)
				assert.NotEmpty(t, annotations)
			}
		})
	}
}

func TestExtractDRACapacitiesFromRecommendations(t *testing.T) {
	gpuMemory := corev1.ResourceName("gpu.example.com/memory")
	gpuCount := corev1.ResourceName("gpu.example.com/count")

	tests := []struct {
		name           string
		recommendation *vpa_types.RecommendedPodResources
		expected       map[corev1.ResourceName]corev1.ResourceList
	}{
		{
			name:           "Nil recommendation returns empty map",
			recommendation: nil,
			expected:       map[corev1.ResourceName]corev1.ResourceList{},
		},
		{
			name: "No extended resources returns empty map",
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "test",
						Target: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
			expected: map[corev1.ResourceName]corev1.ResourceList{},
		},
		{
			name: "Extract single DRA capacity",
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "test",
						Target: corev1.ResourceList{
							gpuMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			expected: map[corev1.ResourceName]corev1.ResourceList{
				gpuMemory: {
					gpuMemory: resource.MustParse("8Gi"),
				},
			},
		},
		{
			name: "Extract multiple DRA capacities",
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "test",
						Target: corev1.ResourceList{
							gpuMemory: resource.MustParse("8Gi"),
							gpuCount:  resource.MustParse("4"),
						},
					},
				},
			},
			expected: map[corev1.ResourceName]corev1.ResourceList{
				gpuMemory: {
					gpuMemory: resource.MustParse("8Gi"),
				},
				gpuCount: {
					gpuCount: resource.MustParse("4"),
				},
			},
		},
		{
			name: "Mixed resources - only extract DRA",
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "test",
						Target: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
							gpuMemory:             resource.MustParse("8Gi"),
						},
					},
				},
			},
			expected: map[corev1.ResourceName]corev1.ResourceList{
				gpuMemory: {
					gpuMemory: resource.MustParse("8Gi"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var containerRecs []vpa_types.RecommendedContainerResources
			if tt.recommendation != nil {
				containerRecs = tt.recommendation.ContainerRecommendations
			}
			result := ExtractDRACapacitiesFromRecommendations(containerRecs)

			assert.Equal(t, len(tt.expected), len(result))

			for resourceName, expectedList := range tt.expected {
				actualList, exists := result[resourceName]
				assert.True(t, exists, "Resource %s should exist", resourceName)

				for resName, expectedQty := range expectedList {
					actualQty, found := actualList[resName]
					assert.True(t, found, "Resource %s should exist in list", resName)
					assert.True(t, expectedQty.Equal(actualQty),
						"Resource %s: expected %s, got %s",
						resName, expectedQty.String(), actualQty.String())
				}
			}
		})
	}
}

// Made with Bob
