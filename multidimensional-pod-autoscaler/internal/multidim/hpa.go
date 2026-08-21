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

package multidim

import (
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v2"
)

// findScalingHPA returns the first HPA whose desiredReplicas != currentReplicas,
// or nil if all HPAs are stable.
func findScalingHPA(hpas []*autoscalingv2.HorizontalPodAutoscaler) *autoscalingv2.HorizontalPodAutoscaler {
	for _, h := range hpas {
		if hpaIsScaling(h) {
			return h
		}
	}
	return nil
}

func hpaIsScaling(h *autoscalingv2.HorizontalPodAutoscaler) bool {
	return h.Status.DesiredReplicas != h.Status.CurrentReplicas
}

func DiscoverHPAs(hpaLister autoscalinglisters.HorizontalPodAutoscalerLister,
	mpa *mpav1alpha1.MultidimPodAutoscaler) ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	all, err := hpaLister.HorizontalPodAutoscalers(mpa.Namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	var matched []*autoscalingv2.HorizontalPodAutoscaler
	for _, h := range all {
		ref := h.Spec.ScaleTargetRef
		if ref.Name == mpa.Spec.TargetRef.Name &&
			ref.Kind == mpa.Spec.TargetRef.Kind &&
			ref.APIVersion == mpa.Spec.TargetRef.APIVersion {
			klog.V(5).InfoS("HPA matched targetRef",
				"mpa", klog.KObj(mpa),
				"hpa", klog.KRef(mpa.Namespace, h.Name),
				"desiredReplicas", h.Status.DesiredReplicas,
				"currentReplicas", h.Status.CurrentReplicas,
			)
			matched = append(matched, h)
		}
	}
	klog.V(4).InfoS("DiscoverHPAs complete",
		"mpa", klog.KObj(mpa),
		"total", len(all),
		"matched", len(matched),
	)
	return matched, nil
}
