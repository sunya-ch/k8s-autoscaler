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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
)

const (
	// defaultStableDurationSeconds is used when spec.stableDurationSeconds is nil.
	// After all HPAs stabilise, the controller waits this long before allowing
	// a VPA to resume actuation.
	defaultStableDurationSeconds int32 = 60

	// defaultTokenHoldDurationSeconds is used when spec.tokenHoldDurationSeconds is nil.
	// A VPA may hold the active token for at most this many seconds before the
	// controller re-evaluates which VPA should be active.
	defaultTokenHoldDurationSeconds int32 = 600
)

// StableDuration returns the effective stableDurationSeconds for mpa as a
// time.Duration.  Exported so the controller can schedule timed requeues.
func StableDuration(mpa *mpav1alpha1.MultidimPodAutoscaler) time.Duration {
	v := defaultStableDurationSeconds
	if mpa.Spec.StableDurationSeconds != nil {
		v = *mpa.Spec.StableDurationSeconds
	}
	return time.Duration(v) * time.Second
}

// TokenDuration returns the effective tokenHoldDurationSeconds for mpa as a
// time.Duration.  Exported so the controller can schedule timed requeues.
func TokenDuration(mpa *mpav1alpha1.MultidimPodAutoscaler) time.Duration {
	v := defaultTokenHoldDurationSeconds
	if mpa.Spec.TokenHoldDurationSeconds != nil {
		v = *mpa.Spec.TokenHoldDurationSeconds
	}
	return time.Duration(v) * time.Second
}

// stableDurationSeconds is the unexported alias used inside this package.
func stableDurationSeconds(mpa *mpav1alpha1.MultidimPodAutoscaler) time.Duration {
	return StableDuration(mpa)
}

// tokenHoldDurationSeconds is the unexported alias used inside this package.
func tokenHoldDurationSeconds(mpa *mpav1alpha1.MultidimPodAutoscaler) time.Duration {
	return TokenDuration(mpa)
}

func Decide(
	now time.Time,
	mpa *mpav1alpha1.MultidimPodAutoscaler,
	hpas []*autoscalingv2.HorizontalPodAutoscaler,
	vpas []*VPAInfo,
) *mpav1alpha1.MultidimPodAutoscalerStatus {
	t := metav1.NewTime(now)
	prev := mpa.Status
	stableDuration := stableDurationSeconds(mpa)
	tokenDuration := tokenHoldDurationSeconds(mpa)

	prevActive := "<none>"
	if prev.ActiveAutoscaler != nil {
		prevActive = string(prev.ActiveAutoscaler.ScalerType) + "/" + prev.ActiveAutoscaler.Name
	}
	klog.V(4).InfoS("Decide called",
		"mpa", klog.KObj(mpa),
		"hpaCount", len(hpas),
		"vpaCount", len(vpas),
		"prevActive", prevActive,
		"stableDuration", stableDuration,
		"tokenDuration", tokenDuration,
	)

	// Build discovered scalers list for status.
	autoScalers := make([]mpav1alpha1.AutoScalerRef, 0, len(hpas)+len(vpas))
	for _, h := range hpas {
		autoScalers = append(autoScalers, mpav1alpha1.AutoScalerRef{
			Name:       h.Name,
			ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType,
			InProgress: hpaIsScaling(h),
		})
	}
	for _, v := range vpas {
		autoScalers = append(autoScalers, mpav1alpha1.AutoScalerRef{
			Name:       v.Name,
			ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
			InProgress: v.RecommendationProvided,
		})
	}

	// Carry forward previous transition time unless we change it.
	lastTransition := prev.LastTransitionTime

	// If any HPA is actively scaling it takes priority unconditionally.
	if scalingHPA := findScalingHPA(hpas); scalingHPA != nil {
		klog.V(4).InfoS("HPA is actively scaling — HPA takes priority",
			"mpa", klog.KObj(mpa),
			"hpa", klog.KRef(mpa.Namespace, scalingHPA.Name),
			"desiredReplicas", scalingHPA.Status.DesiredReplicas,
			"currentReplicas", scalingHPA.Status.CurrentReplicas,
		)
		newActive := &mpav1alpha1.AutoScalerRef{
			Name:       scalingHPA.Name,
			ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType,
			InProgress: true,
		}
		if !sameActiveAutoscaler(prev.ActiveAutoscaler, newActive) {
			lastTransition = &t
		}
		return &mpav1alpha1.MultidimPodAutoscalerStatus{
			ActiveAutoscaler:   newActive,
			LastTransitionTime: lastTransition,
			AutoScalers:        autoScalers,
		}
	}

	switch {
	case prev.ActiveAutoscaler == nil:
		// No active autoscaler. Allow VPA only after the stable window has elapsed.
		elapsed := now.Sub(func() time.Time {
			if lastTransition != nil {
				return lastTransition.Time
			}
			return time.Time{}
		}())
		klog.V(4).InfoS("No active autoscaler — waiting for stable window",
			"mpa", klog.KObj(mpa),
			"elapsed", elapsed,
			"stableDuration", stableDuration,
			"windowPassed", passMoreThanDuration(lastTransition, &t, stableDuration),
		)
		if passMoreThanDuration(lastTransition, &t, stableDuration) {
			// Pick the highest-priority VPA.
			if best := pickBestVPA(vpas); best != nil {
				klog.V(3).InfoS("Stable window elapsed — activating VPA",
					"mpa", klog.KObj(mpa),
					"vpa", klog.KRef(mpa.Namespace, best.Name),
					"priority", best.Priority,
					"recommendationProvided", best.RecommendationProvided,
				)
				return &mpav1alpha1.MultidimPodAutoscalerStatus{
					ActiveAutoscaler: &mpav1alpha1.AutoScalerRef{
						Name:       best.Name,
						ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
						InProgress: best.RecommendationProvided,
					},
					LastTransitionTime: &t,
					AutoScalers:        autoScalers,
				}
			}
			klog.V(4).InfoS("Stable window elapsed but no VPA available",
				"mpa", klog.KObj(mpa),
			)
		}
	case prev.ActiveAutoscaler.ScalerType == mpav1alpha1.HorizontalPodAutoscalerScalerType:
		// Active was HPA — it has now stabilised (otherwise we would have
		// matched the scalingHPA branch above). Clear the active autoscaler
		// and start the settling timer.
		klog.V(3).InfoS("HPA stabilised — clearing active autoscaler, starting stable window",
			"mpa", klog.KObj(mpa),
			"prevHPA", prev.ActiveAutoscaler.Name,
		)
		return &mpav1alpha1.MultidimPodAutoscalerStatus{
			ActiveAutoscaler:   nil,
			LastTransitionTime: &t,
			AutoScalers:        autoScalers,
		}
	case prev.ActiveAutoscaler.ScalerType == mpav1alpha1.VerticalPodAutoscalerScalerType:
		elapsed := now.Sub(func() time.Time {
			if lastTransition != nil {
				return lastTransition.Time
			}
			return time.Time{}
		}())
		klog.V(4).InfoS("VPA is active — checking token hold duration",
			"mpa", klog.KObj(mpa),
			"activeVPA", prev.ActiveAutoscaler.Name,
			"elapsed", elapsed,
			"tokenDuration", tokenDuration,
			"tokenExpired", passMoreThanDuration(lastTransition, &t, tokenDuration),
		)
		activeDeleted := !vpaExists(vpas, prev.ActiveAutoscaler.Name)
		if activeDeleted || passMoreThanDuration(lastTransition, &t, tokenDuration) {
			best := pickBestVPA(vpas)
			if best == nil {
				// No VPA available — clear active autoscaler.
				klog.V(3).InfoS("No VPA available — clearing active autoscaler",
					"mpa", klog.KObj(mpa),
					"prevVPA", prev.ActiveAutoscaler.Name,
					"reason", map[bool]string{true: "activeDeleted", false: "tokenExpired"}[activeDeleted],
				)
				return &mpav1alpha1.MultidimPodAutoscalerStatus{
					ActiveAutoscaler:   nil,
					LastTransitionTime: &t,
					AutoScalers:        autoScalers,
				}
			}
			newActive := &mpav1alpha1.AutoScalerRef{
				Name:       best.Name,
				ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
				InProgress: best.RecommendationProvided,
			}
			if prev.ActiveAutoscaler.Name != newActive.Name {
				klog.V(3).InfoS("Switching active VPA",
					"mpa", klog.KObj(mpa),
					"prevVPA", prev.ActiveAutoscaler.Name,
					"newVPA", best.Name,
					"priority", best.Priority,
					"reason", map[bool]string{true: "activeDeleted", false: "tokenExpired"}[activeDeleted],
				)
				return &mpav1alpha1.MultidimPodAutoscalerStatus{
					ActiveAutoscaler:   newActive,
					LastTransitionTime: &t,
					AutoScalers:        autoScalers,
				}
			}
			klog.V(4).InfoS("Token expired but best VPA unchanged",
				"mpa", klog.KObj(mpa),
				"vpa", best.Name,
			)
		}
	}

	// AutoScalers list changed (new scalers appeared / InProgress state flipped)
	// even though the active autoscaler itself did not change. Return a status
	// that carries the previous active autoscaler forward with the updated list.
	if autoScalersChanged(prev.AutoScalers, autoScalers) {
		klog.V(4).InfoS("AutoScalers list changed — updating status",
			"mpa", klog.KObj(mpa),
			"prevCount", len(prev.AutoScalers),
			"newCount", len(autoScalers),
		)
		return &mpav1alpha1.MultidimPodAutoscalerStatus{
			ActiveAutoscaler:   prev.ActiveAutoscaler,
			LastTransitionTime: lastTransition,
			AutoScalers:        autoScalers,
		}
	}

	// No change at all.
	klog.V(5).InfoS("Decide: no status change", "mpa", klog.KObj(mpa))
	return nil
}

// vpaExists reports whether a VPA with the given name is present in the slice.
func vpaExists(vpas []*VPAInfo, name string) bool {
	for _, v := range vpas {
		if v.Name == name {
			return true
		}
	}
	return false
}

func passMoreThanDuration(lastTransition, now *metav1.Time, duration time.Duration) bool {
	return lastTransition == nil || (lastTransition != nil && now.Sub(lastTransition.Time) > duration)
}

func sameActiveAutoscaler(a, b *mpav1alpha1.AutoScalerRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name && a.ScalerType == b.ScalerType
}

// autoScalersChanged returns true when the two AutoScalerRef slices differ in
// membership or any field value, regardless of ordering.
func autoScalersChanged(prev, next []mpav1alpha1.AutoScalerRef) bool {
	if len(prev) != len(next) {
		return true
	}
	type key struct {
		name       string
		scalerType mpav1alpha1.ScalerType
	}
	prevMap := make(map[key]bool, len(prev))
	for _, r := range prev {
		prevMap[key{r.Name, r.ScalerType}] = r.InProgress
	}
	for _, r := range next {
		inProgress, ok := prevMap[key{r.Name, r.ScalerType}]
		if !ok || inProgress != r.InProgress {
			return true
		}
	}
	return false
}
