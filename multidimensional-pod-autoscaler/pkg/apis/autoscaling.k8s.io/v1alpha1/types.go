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

package v1alpha1

import (
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mpa
// +kubebuilder:printcolumn:name="Target Kind",type=string,JSONPath=".spec.targetRef.kind"
// +kubebuilder:printcolumn:name="Target Name",type=string,JSONPath=".spec.targetRef.name"
// +kubebuilder:printcolumn:name="Active Autoscaler",type=string,JSONPath=".status.activeAutoscaler.name"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// MultidimPodAutoscaler coordinates all HPAs and VPAs that target the same
// workload. The user declares intent via spec.targetRef; the controller
// discovers the actual scalers and records them in status.
type MultidimPodAutoscaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MultidimPodAutoscalerSpec   `json:"spec"`
	Status MultidimPodAutoscalerStatus `json:"status,omitempty"`
}

// MultidimPodAutoscalerSpec defines the desired state of a MultidimPodAutoscaler.
type MultidimPodAutoscalerSpec struct {
	// TargetRef points to the workload being scaled.
	// The controller uses this to discover all HPAs and VPAs in the same
	// namespace whose scaleTargetRef / targetRef matches this reference.
	TargetRef autoscalingv1.CrossVersionObjectReference `json:"targetRef"`

	// StableDurationSeconds is the minimum number of seconds all HPAs must
	// have been stable (currentReplicas == desiredReplicas) before the
	// controller allows a VPA to become active. This prevents VPA from
	// resuming evictions while the fleet is still settling after a scale event.
	// Defaults to 60.
	// +optional
	StableDurationSeconds *int32 `json:"stableDurationSeconds,omitempty"`

	// TokenHoldDurationSeconds is the maximum number of seconds a single VPA
	// may remain the active autoscaler before the controller re-evaluates
	// which VPA should hold the token. This prevents a high-priority VPA from
	// monopolising actuation indefinitely when multiple VPAs target the same
	// workload. After the token expires the controller re-selects the
	// highest-priority VPA; if the winner is unchanged the token is simply
	// renewed. Defaults to 600.
	// +optional
	TokenHoldDurationSeconds *int32 `json:"tokenHoldDurationSeconds,omitempty"`
}

// MultidimPodAutoscalerStatus describes the current state of a MultidimPodAutoscaler.
type MultidimPodAutoscalerStatus struct {
	// ActiveAutoscaler is the name and type of the scaler currently allowed
	// to act. Nil when no scaler is active (e.g., between an HPA stabilizing
	// and VPA resuming after stableDurationSeconds).
	// +optional
	ActiveAutoscaler *AutoScalerRef `json:"activeAutoscaler,omitempty"`

	// LastTransitionTime is the last time the active autoscaler changed.
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`

	// AutoScalers lists the HorizontalPodAutoscalers and VerticalPodAutoscalers
	// discovered by the controller as co-targeting the same workload as this
	// MPA. Updated on every reconcile.
	// +optional
	// +listType=map
	// +listMapKey=name
	AutoScalers []AutoScalerRef `json:"autoscalers,omitempty"`

	// Conditions describes the current state of the MultidimPodAutoscaler.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ScalerType identifies the kind of autoscaler referenced by an AutoScalerRef.
// +enum
type ScalerType string

const (
	// HorizontalPodAutoscalerScalerType indicates the referenced scaler is a
	// HorizontalPodAutoscaler.
	HorizontalPodAutoscalerScalerType ScalerType = "HorizontalPodAutoscaler"

	// VerticalPodAutoscalerScalerType indicates the referenced scaler is a
	// VerticalPodAutoscaler.
	VerticalPodAutoscalerScalerType ScalerType = "VerticalPodAutoscaler"
)

const (
	// AnnotationPausedByMPA is set on a VPA's metadata.annotations by the MPA
	// controller when it manages spec.paused on that VPA. The value is
	// "namespace/name" of the owning MultidimPodAutoscaler.
	// The annotation is removed when the MPA clears spec.paused or is deleted.
	AnnotationPausedByMPA = "autoscaling.k8s.io/paused-by-mpa"

	// ConditionTypeActive is True when this MPA is the sole owner of its
	// targetRef and is actively coordinating scalers.
	ConditionTypeActive = "Active"

	// ConditionTypeConflicted is True when another MPA in the same namespace
	// already claims the same targetRef. While Conflicted=True this MPA
	// performs no VPA writes and does not modify any scaler state.
	ConditionTypeConflicted = "Conflicted"
)

// AutoScalerRef identifies a single HPA or VPA object tracked by the MPA.
type AutoScalerRef struct {
	// Name is the name of the HPA or VPA object.
	Name string `json:"name"`

	// ScalerType is the kind of the referenced scaler.
	// +kubebuilder:validation:Enum=HorizontalPodAutoscaler;VerticalPodAutoscaler
	ScalerType ScalerType `json:"scalerType"`

	// InProgress indicates whether this scaler is currently active.
	// For HPA: true when desiredReplicas != currentReplicas.
	// For VPA: true when a recommendation has been provided (RecommendationProvided condition).
	InProgress bool `json:"inProgress"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MultidimPodAutoscalerList is a list of MultidimPodAutoscaler objects.
type MultidimPodAutoscalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MultidimPodAutoscaler `json:"items"`
}
