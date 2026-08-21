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
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpaclientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	vpalisters "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1"
)

func DiscoverVPAs(vpaLister vpalisters.VerticalPodAutoscalerLister, targetRef autoscalingv1.CrossVersionObjectReference, namespace string) ([]*VPAInfo, error) {
	all, err := vpaLister.VerticalPodAutoscalers(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	var matched []*VPAInfo
	for _, v := range all {
		if v.Spec.TargetRef == nil {
			continue
		}
		ref := v.Spec.TargetRef
		if ref.Name == targetRef.Name &&
			ref.Kind == targetRef.Kind &&
			ref.APIVersion == targetRef.APIVersion {
			var priority int32
			if v.Spec.Priority != nil {
				priority = *v.Spec.Priority
			}
			info := &VPAInfo{
				Namespace:              v.Namespace,
				Name:                   v.Name,
				CreationTimestamp:      v.CreationTimestamp.Time,
				Priority:               priority,
				Paused:                 v.Spec.Paused != nil && *v.Spec.Paused,
				PausedByMPA:            v.Annotations[mpav1alpha1.AnnotationPausedByMPA],
				RecommendationProvided: vpaHasRecommendation(v),
			}
			klog.V(5).InfoS("VPA matched targetRef",
				"vpa", klog.KRef(namespace, v.Name),
				"priority", info.Priority,
				"paused", info.Paused,
				"pausedByMPA", info.PausedByMPA,
				"recommendationProvided", info.RecommendationProvided,
			)
			matched = append(matched, info)
		}
	}
	klog.V(4).InfoS("DiscoverVPAs complete",
		"namespace", namespace,
		"total", len(all),
		"matched", len(matched),
	)
	return matched, nil
}

// VPAInfo holds the fields we need from a VPA without pulling in the full type.
type VPAInfo struct {
	Namespace string
	Name      string
	// CreationTimestamp is used as a tie-breaker when two VPAs share the same
	// Priority value: the older VPA (earlier timestamp) wins.
	CreationTimestamp time.Time
	// Priority mirrors spec.priority on the VPA object. Lower value = higher
	// priority. Defaults to 0 when spec.priority is nil.
	Priority int32
	Paused   bool
	// PausedByMPA is the "namespace/name" value of AnnotationPausedByMPA, or
	// empty if the annotation is absent.
	PausedByMPA            string
	RecommendationProvided bool
}

// pickBestVPA returns the ready VPA (RecommendationProvided=true) with the
// lowest Priority value.  When two VPAs share the same priority the one with
// the earlier CreationTimestamp wins.  VPAs that have not yet produced a
// recommendation are skipped.  Returns nil when the slice is empty or all VPAs
// are unready.
func pickBestVPA(vpas []*VPAInfo) *VPAInfo {
	var best *VPAInfo
	for _, v := range vpas {
		if !v.RecommendationProvided {
			continue
		}
		if best == nil ||
			v.Priority < best.Priority ||
			(v.Priority == best.Priority && v.CreationTimestamp.Before(best.CreationTimestamp)) {
			klog.V(5).InfoS("pickBestVPA: new best candidate",
				"prev", func() string {
					if best == nil {
						return "<none>"
					}
					return best.Name
				}(),
				"next", v.Name,
				"prevPriority", func() int32 {
					if best == nil {
						return 0
					}
					return best.Priority
				}(),
				"nextPriority", v.Priority,
			)
			best = v
		}
	}
	if best != nil {
		klog.V(4).InfoS("pickBestVPA result",
			"vpa", best.Name,
			"priority", best.Priority,
			"recommendationProvided", best.RecommendationProvided,
		)
	} else {
		klog.V(4).InfoS("pickBestVPA result: no ready VPA found")
	}
	return best
}

// vpaHasRecommendation returns true if the VPA status carries a
// RecommendationProvided=True condition.
func vpaHasRecommendation(v *vpav1.VerticalPodAutoscaler) bool {
	for _, c := range v.Status.Conditions {
		if c.Type == vpav1.RecommendationProvided && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// ── Pause enforcement ────────────────────────────────────────────────────────

// EnforceVPAPauseState sets spec.paused (and the owner annotation) on every
// VPA that is not the active autoscaler.
//
// Ownership rules applied before any write:
//   - VPA annotated by a *different* MPA → always skip (foreign owner).
//   - VPA with no annotation and wantPaused=false → skip; we never wrote it so
//     we must not clear it (could have been paused manually or by another controller).
//   - VPA with no annotation and wantPaused=true  → we take ownership by writing
//     the annotation together with spec.paused in one atomic patch.
//   - VPA already annotated by *this* MPA → manage freely.
func EnforceVPAPauseState(
	ctx context.Context,
	vpaClient vpaclientset.Interface,
	mpa *mpav1alpha1.MultidimPodAutoscaler,
	vpas []*VPAInfo,
	active *mpav1alpha1.AutoScalerRef,
) error {
	mpaKey := mpa.Namespace + "/" + mpa.Name

	for _, vpa := range vpas {
		wantPaused := true
		if active != nil &&
			active.ScalerType == mpav1alpha1.VerticalPodAutoscalerScalerType &&
			active.Name == vpa.Name {
			wantPaused = false
		}

		switch {
		case vpa.PausedByMPA != "" && vpa.PausedByMPA != mpaKey:
			// Foreign owner — never touch.
			klog.V(4).InfoS("Skipping VPA owned by another MPA",
				"vpa", klog.KRef(mpa.Namespace, vpa.Name),
				"owner", vpa.PausedByMPA)
			continue

		case vpa.PausedByMPA == "" && !wantPaused:
			// We have no ownership claim and want to unpause — skip.
			// The VPA is either unpaused already (nothing to do) or was paused
			// by something outside MPA (must not touch).
			continue
		}

		// Skip if the state and annotation are already exactly right.
		alreadyCorrect := vpa.Paused == wantPaused &&
			(wantPaused && vpa.PausedByMPA == mpaKey || !wantPaused && vpa.PausedByMPA == "")
		if alreadyCorrect {
			continue
		}

		if err := SetVPAPaused(ctx, vpaClient, mpa.Namespace, vpa.Name, wantPaused, mpaKey); err != nil {
			return fmt.Errorf("setting paused=%v on VPA %s/%s: %w", wantPaused, mpa.Namespace, vpa.Name, err)
		}
		klog.V(3).InfoS("Set VPA paused state",
			"vpa", klog.KRef(mpa.Namespace, vpa.Name),
			"paused", wantPaused,
			"mpa", mpaKey)
	}
	return nil
}

// SetVPAPaused issues a single merge-patch that atomically sets spec.paused
// and stamps (or removes) AnnotationPausedByMPA on the VPA.
//
// When paused=true  → spec.paused=true  + annotation set to mpaKey.
// When paused=false → spec.paused=false + annotation removed (null in JSON).
//
// *bool is used in the patch struct so that false is serialised explicitly
// (a plain bool with omitempty would be dropped).
func SetVPAPaused(ctx context.Context, vpaClient vpaclientset.Interface, namespace, name string, paused bool, mpaKey string) error {
	// Annotation value: mpaKey when pausing, JSON null when clearing.
	// json.RawMessage("null") removes the key via merge-patch semantics.
	var annotationValue interface{}
	if paused {
		annotationValue = mpaKey
	} else {
		annotationValue = nil // serialises as JSON null → removes the key
	}

	type vpaSpecPatch struct {
		Paused *bool `json:"paused"` // omitempty intentionally absent
	}
	type metaPatch struct {
		Annotations map[string]interface{} `json:"annotations"`
	}
	type vpaPatch struct {
		Metadata metaPatch    `json:"metadata"`
		Spec     vpaSpecPatch `json:"spec"`
	}

	patch, err := json.Marshal(vpaPatch{
		Metadata: metaPatch{
			Annotations: map[string]interface{}{
				mpav1alpha1.AnnotationPausedByMPA: annotationValue,
			},
		},
		Spec: vpaSpecPatch{Paused: &paused},
	})
	if err != nil {
		return err
	}
	_, err = vpaClient.AutoscalingV1().VerticalPodAutoscalers(namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}
