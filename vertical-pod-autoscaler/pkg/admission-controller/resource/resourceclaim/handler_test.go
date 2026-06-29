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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"
	podpkg "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod/patch"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

type fakePatchCalculator struct {
	patches    []resource_admission.PatchRecord
	err        error
	targetType patch.PatchResourceTarget
}

func (c *fakePatchCalculator) PatchResourceTarget() patch.PatchResourceTarget {
	return c.targetType
}

func (c *fakePatchCalculator) CalculatePatches(_ *corev1.Pod, _ *vpa_types.VerticalPodAutoscaler) (
	[]resource_admission.PatchRecord, error) {
	return c.patches, c.err
}

// newCacheWith returns a VpaCache pre-populated with the given VPA and pod.
// Pass a nil vpa to get an empty cache (simulating no VPA match at pod admission).
func newCacheWith(namespace, podName string, vpa *vpa_types.VerticalPodAutoscaler, pod *corev1.Pod) *podpkg.VpaCache {
	c := podpkg.NewVpaCache()
	if vpa != nil {
		c.Store(namespace, podName, vpa, pod)
	}
	return c
}

func TestGetPatches(t *testing.T) {
	testVpa := test.VerticalPodAutoscaler().WithName("test-vpa").WithContainer("test-container").Get()
	testPatchRecord := resource_admission.PatchRecord{
		Op:    "replace",
		Path:  "/spec/devices/0/requests/0/count",
		Value: "4",
	}
	testPatchRecord2 := resource_admission.PatchRecord{
		Op:    "replace",
		Path:  "/spec/devices/0/requests/1/count",
		Value: "8",
	}

	ownerPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	// claimWithOwner has a Pod OwnerReference pointing at "test-pod".
	claimWithOwner := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claim",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Pod",
					Name:       "test-pod",
				},
			},
		},
	}

	// claimNoOwnerRef has no OwnerReferences at all.
	claimNoOwnerRef := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claim",
			Namespace: "default",
		},
	}

	// claimNonPodOwner has an OwnerReference that is not a Pod.
	claimNonPodOwner := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claim",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "test-rs",
				},
			},
		},
	}

	tests := []struct {
		name          string
		claimJson     []byte
		namespace     string
		operation     admissionv1.Operation
		version       string
		cache         *podpkg.VpaCache
		calculators   []patch.Calculator
		expectPatches []resource_admission.PatchRecord
		expectError   bool
	}{
		{
			name:        "invalid JSON",
			claimJson:   []byte("{"),
			namespace:   "default",
			operation:   admissionv1.Create,
			version:     "v1",
			cache:       newCacheWith("default", "test-pod", testVpa, ownerPod),
			expectError: true,
		},
		{
			name:          "unsupported version",
			claimJson:     mustMarshal(t, claimWithOwner),
			namespace:     "default",
			operation:     admissionv1.Create,
			version:       "v1alpha2",
			cache:         newCacheWith("default", "test-pod", testVpa, ownerPod),
			expectError:   true,
			expectPatches: nil,
		},
		{
			name:          "non-CREATE operation",
			claimJson:     mustMarshal(t, claimWithOwner),
			namespace:     "default",
			operation:     admissionv1.Update,
			version:       "v1",
			cache:         newCacheWith("default", "test-pod", testVpa, ownerPod),
			expectError:   false,
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name:          "no owner pod found - no OwnerReferences",
			claimJson:     mustMarshal(t, claimNoOwnerRef),
			namespace:     "default",
			operation:     admissionv1.Create,
			version:       "v1",
			cache:         newCacheWith("default", "test-pod", testVpa, ownerPod),
			expectError:   false,
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name:          "no owner pod found - non-pod OwnerReference",
			claimJson:     mustMarshal(t, claimNonPodOwner),
			namespace:     "default",
			operation:     admissionv1.Create,
			version:       "v1",
			cache:         newCacheWith("default", "test-pod", testVpa, ownerPod),
			expectError:   false,
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name:          "no VPA in cache (pod was admitted without a matching VPA)",
			claimJson:     mustMarshal(t, claimWithOwner),
			namespace:     "default",
			operation:     admissionv1.Create,
			version:       "v1",
			cache:         podpkg.NewVpaCache(), // empty — no entry stored
			expectError:   false,
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name:      "calculator returns error",
			claimJson: mustMarshal(t, claimWithOwner),
			namespace: "default",
			operation: admissionv1.Create,
			version:   "v1",
			cache:     newCacheWith("default", "test-pod", testVpa, ownerPod),
			calculators: []patch.Calculator{&fakePatchCalculator{
				patches:    []resource_admission.PatchRecord{},
				err:        errors.New("calculation failed"),
				targetType: patch.ResourceClaim,
			}},
			expectError:   true,
			expectPatches: []resource_admission.PatchRecord{},
		},
		{
			name:      "calculator with wrong target type is skipped",
			claimJson: mustMarshal(t, claimWithOwner),
			namespace: "default",
			operation: admissionv1.Create,
			version:   "v1",
			cache:     newCacheWith("default", "test-pod", testVpa, ownerPod),
			calculators: []patch.Calculator{&fakePatchCalculator{
				patches: []resource_admission.PatchRecord{
					testPatchRecord,
				},
				err:        nil,
				targetType: patch.Pod, // Wrong target type
			}},
			expectError:   false,
			expectPatches: []resource_admission.PatchRecord{}, // No patches because calculator is skipped
		},
		{
			name:      "patches returned correctly",
			claimJson: mustMarshal(t, claimWithOwner),
			namespace: "default",
			operation: admissionv1.Create,
			version:   "v1",
			cache:     newCacheWith("default", "test-pod", testVpa, ownerPod),
			calculators: []patch.Calculator{&fakePatchCalculator{
				patches: []resource_admission.PatchRecord{
					testPatchRecord,
					testPatchRecord2,
				},
				err:        nil,
				targetType: patch.ResourceClaim,
			}},
			expectError: false,
			expectPatches: []resource_admission.PatchRecord{
				testPatchRecord,
				testPatchRecord2,
			},
		},
		{
			name:      "patches returned correctly for multiple calculators",
			claimJson: mustMarshal(t, claimWithOwner),
			namespace: "default",
			operation: admissionv1.Create,
			version:   "v1",
			cache:     newCacheWith("default", "test-pod", testVpa, ownerPod),
			calculators: []patch.Calculator{
				&fakePatchCalculator{
					patches: []resource_admission.PatchRecord{
						testPatchRecord,
					},
					err:        nil,
					targetType: patch.ResourceClaim,
				},
				&fakePatchCalculator{
					patches: []resource_admission.PatchRecord{
						testPatchRecord2,
					},
					err:        nil,
					targetType: patch.ResourceClaim,
				},
			},
			expectError: false,
			expectPatches: []resource_admission.PatchRecord{
				testPatchRecord,
				testPatchRecord2,
			},
		},
		{
			name:      "mixed calculators - only ResourceClaim target used",
			claimJson: mustMarshal(t, claimWithOwner),
			namespace: "default",
			operation: admissionv1.Create,
			version:   "v1",
			cache:     newCacheWith("default", "test-pod", testVpa, ownerPod),
			calculators: []patch.Calculator{
				&fakePatchCalculator{
					patches: []resource_admission.PatchRecord{
						{Op: "add", Path: "/should/be/skipped", Value: "pod"},
					},
					err:        nil,
					targetType: patch.Pod, // Should be skipped
				},
				&fakePatchCalculator{
					patches: []resource_admission.PatchRecord{
						testPatchRecord,
					},
					err:        nil,
					targetType: patch.ResourceClaim, // Should be used
				},
				&fakePatchCalculator{
					patches: []resource_admission.PatchRecord{
						{Op: "add", Path: "/also/skipped", Value: "resize"},
					},
					err:        nil,
					targetType: patch.Resize, // Should be skipped
				},
			},
			expectError: false,
			expectPatches: []resource_admission.PatchRecord{
				testPatchRecord, // Only the ResourceClaim calculator's patch
			},
		},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("test case: %s", tc.name), func(t *testing.T) {
			h := NewResourceHandler(tc.cache, tc.calculators)

			patches, errs := h.GetPatches(context.Background(), &admissionv1.AdmissionRequest{
				Resource: metav1.GroupVersionResource{
					Group:    "resource.k8s.io",
					Version:  tc.version,
					Resource: "resourceclaims",
				},
				Operation: tc.operation,
				Namespace: tc.namespace,
				Object: runtime.RawExtension{
					Raw: tc.claimJson,
				},
			})

			if tc.expectError {
				assert.NotEmpty(t, errs, "expected error but got none")
			} else {
				assert.Empty(t, errs, "unexpected error: %v", errs)
			}

			if tc.expectPatches != nil {
				if assert.Equal(t, len(tc.expectPatches), len(patches),
					fmt.Sprintf("got %+v, want %+v", patches, tc.expectPatches)) {
					for i, gotPatch := range patches {
						if !patch.EqPatch(gotPatch, tc.expectPatches[i]) {
							t.Errorf("Expected patch at position %d to be %+v, got %+v",
								i, tc.expectPatches[i], gotPatch)
						}
					}
				}
			}
		})
	}
}

func TestGroupResource(t *testing.T) {
	h := NewResourceHandler(podpkg.NewVpaCache(), nil)
	gr := h.GroupResource()
	assert.Equal(t, "resource.k8s.io", gr.Group)
	assert.Equal(t, "resourceclaims", gr.Resource)
}

func TestDisallowIncorrectObjects(t *testing.T) {
	h := NewResourceHandler(podpkg.NewVpaCache(), nil)
	assert.False(t, h.DisallowIncorrectObjects())
}

// TestTranslateMetadataPatches exercises the translateMetadataPatches helper
// directly, covering all nil-capacity branches and JSON Pointer escaping.
func TestTranslateMetadataPatches(t *testing.T) {
	// capacityName is the bare name as emitted by createResourceClaimPatches after
	// the deviceClassName prefix has been stripped (e.g. "memory", not "vgpu.example.com/memory").
	const deviceClassName = "vgpu.example.com"
	const capacityName = "memory"

	// claimNoCapacity: ExactDeviceRequest has no Capacity at all.
	claimNoCapacity := &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "req0",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: deviceClassName,
						},
					},
				},
			},
		},
	}

	// claimNilRequests: Capacity object exists but Requests map is nil.
	claimNilRequests := &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "req0",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: deviceClassName,
							Capacity:        &resourceapi.CapacityRequirements{},
						},
					},
				},
			},
		},
	}

	// claimHasCapacityKey: the bare key already exists → must "replace".
	claimHasCapacityKey := &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "req0",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: deviceClassName,
							Capacity: &resourceapi.CapacityRequirements{
								Requests: map[resourceapi.QualifiedName]resource.Quantity{
									resourceapi.QualifiedName(capacityName): resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	// claimNewKey: requests map exists but this key is absent → must "add".
	claimNewKey := &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "req0",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: deviceClassName,
							Capacity: &resourceapi.CapacityRequirements{
								Requests: map[resourceapi.QualifiedName]resource.Quantity{},
							},
						},
					},
				},
			},
		},
	}

	// metaPath reflects the format emitted by createResourceClaimPatches:
	// /resourceClaim/<claimName>/<deviceClassName>/<bareCapacityName>
	metaPath := "/resourceClaim/gpu-claim/" + deviceClassName + "/" + capacityName

	tests := []struct {
		name    string
		patches []resource_admission.PatchRecord
		claim   *resourceapi.ResourceClaim
		want    []resource_admission.PatchRecord
	}{
		{
			name:    "non-metadata patch passed through unchanged",
			patches: []resource_admission.PatchRecord{{Op: "add", Path: "/spec/foo", Value: "bar"}},
			claim:   claimNoCapacity,
			want:    []resource_admission.PatchRecord{{Op: "add", Path: "/spec/foo", Value: "bar"}},
		},
		{
			name:    "malformed metadata path (too few segments) is skipped",
			patches: []resource_admission.PatchRecord{{Op: "replace", Path: "/resourceClaim/only-two", Value: "x"}},
			claim:   claimNoCapacity,
			want:    []resource_admission.PatchRecord{},
		},
		{
			name:    "unknown deviceClassName is skipped",
			patches: []resource_admission.PatchRecord{{Op: "replace", Path: "/resourceClaim/gpu-claim/unknown.class/some-cap", Value: "1Gi"}},
			claim:   claimNoCapacity,
			want:    []resource_admission.PatchRecord{},
		},
		{
			name:    "nil Capacity adds whole capacity object",
			patches: []resource_admission.PatchRecord{{Op: "replace", Path: metaPath, Value: "16Gi"}},
			claim:   claimNoCapacity,
			want: []resource_admission.PatchRecord{
				{
					Op:   "add",
					Path: "/spec/devices/requests/0/exactly/capacity",
					Value: map[string]interface{}{
						"requests": map[string]interface{}{capacityName: "16Gi"},
					},
				},
			},
		},
		{
			name:    "nil Requests map adds requests map",
			patches: []resource_admission.PatchRecord{{Op: "replace", Path: metaPath, Value: "16Gi"}},
			claim:   claimNilRequests,
			want: []resource_admission.PatchRecord{
				{
					Op:    "add",
					Path:  "/spec/devices/requests/0/exactly/capacity/requests",
					Value: map[string]interface{}{capacityName: "16Gi"},
				},
			},
		},
		{
			name:    "existing key uses replace",
			patches: []resource_admission.PatchRecord{{Op: "replace", Path: metaPath, Value: "16Gi"}},
			claim:   claimHasCapacityKey,
			want: []resource_admission.PatchRecord{
				{
					Op:    "replace",
					Path:  "/spec/devices/requests/0/exactly/capacity/requests/" + capacityName,
					Value: "16Gi",
				},
			},
		},
		{
			name:    "absent key in existing map uses add",
			patches: []resource_admission.PatchRecord{{Op: "replace", Path: metaPath, Value: "16Gi"}},
			claim:   claimNewKey,
			want: []resource_admission.PatchRecord{
				{
					Op:    "add",
					Path:  "/spec/devices/requests/0/exactly/capacity/requests/" + capacityName,
					Value: "16Gi",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := translateMetadataPatches(tc.patches, tc.claim)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestJsonPointerEscape verifies RFC 6901 escaping.
func TestJsonPointerEscape(t *testing.T) {
	assert.Equal(t, "vgpu.example.com~1memory", jsonPointerEscape("vgpu.example.com/memory"))
	assert.Equal(t, "a~0b~1c", jsonPointerEscape("a~b/c"))
	assert.Equal(t, "nospecial", jsonPointerEscape("nospecial"))
}

func mustMarshal(t *testing.T, obj interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("failed to marshal object: %v", err)
	}
	return data
}
