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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
)

func TestMultidim(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multidim Suite")
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mpaWith(status mpav1alpha1.MultidimPodAutoscalerStatus) *mpav1alpha1.MultidimPodAutoscaler {
	return &mpav1alpha1.MultidimPodAutoscaler{Status: status}
}

func stableHPA(name string) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2, DesiredReplicas: 2},
	}
}

func scalingHPA(name string) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 1, DesiredReplicas: 3},
	}
}

func vpa(name string, recommended bool) *VPAInfo {
	return &VPAInfo{Name: name, RecommendationProvided: recommended}
}

func vpaWithPriority(name string, recommended bool, priority int32, createdAt time.Time) *VPAInfo {
	return &VPAInfo{Name: name, RecommendationProvided: recommended, Priority: priority, CreationTimestamp: createdAt}
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

// ── Decide ────────────────────────────────────────────────────────────────────

var _ = Describe("Decide", func() {

	var (
		now        = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
		longAgo    = metav1.NewTime(now.Add(-11 * time.Minute))
		justNow    = metav1.NewTime(now.Add(-5 * time.Second))
	)

	// ── HPA takes priority ────────────────────────────────────────────────────
	Describe("HPA scaling takes unconditional priority", func() {
		DescribeTable("returns HPA as active autoscaler",
			func(prevActive *mpav1alpha1.AutoScalerRef, expectTransitionUpdated bool) {
				mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
					ActiveAutoscaler:   prevActive,
					LastTransitionTime: ptrTime(longAgo),
				})
				result := Decide(now, mpa, []*autoscalingv2.HorizontalPodAutoscaler{scalingHPA("hpa-1")}, nil)

				Expect(result).NotTo(BeNil())
				Expect(result.ActiveAutoscaler).NotTo(BeNil())
				Expect(result.ActiveAutoscaler.Name).To(Equal("hpa-1"))
				Expect(result.ActiveAutoscaler.ScalerType).To(Equal(mpav1alpha1.HorizontalPodAutoscalerScalerType))
				Expect(result.ActiveAutoscaler.InProgress).To(BeTrue())

				if expectTransitionUpdated {
					Expect(result.LastTransitionTime.Time).To(BeTemporally("~", now, time.Second))
				} else {
					Expect(result.LastTransitionTime).To(Equal(ptrTime(longAgo)))
				}
			},
			Entry("no previous active → transition time updated",
				nil, true),
			Entry("previous active was a different scaler → transition time updated",
				&mpav1alpha1.AutoScalerRef{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType}, true),
			Entry("same HPA already active → transition time kept",
				&mpav1alpha1.AutoScalerRef{Name: "hpa-1", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType, InProgress: true}, false),
		)
	})

	// ── HPA stabilised → clear active ────────────────────────────────────────
	Describe("HPA stabilises", func() {
		It("clears active autoscaler and resets transition time", func() {
			mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
				ActiveAutoscaler: &mpav1alpha1.AutoScalerRef{
					Name:       "hpa-1",
					ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType,
					InProgress: true,
				},
				LastTransitionTime: ptrTime(longAgo),
			})
			result := Decide(now, mpa, []*autoscalingv2.HorizontalPodAutoscaler{stableHPA("hpa-1")}, nil)

			Expect(result).NotTo(BeNil())
			Expect(result.ActiveAutoscaler).To(BeNil())
			Expect(result.LastTransitionTime.Time).To(BeTemporally("~", now, time.Second))
		})
	})

	// ── No active autoscaler → promote VPA after stable window ───────────────
	Describe("no active autoscaler", func() {
		DescribeTable("VPA promotion behaviour",
			func(prevAutoScalers []mpav1alpha1.AutoScalerRef, lastTransition *metav1.Time, vpas []*VPAInfo, expectNil bool, expectVPAName string) {
				mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
					LastTransitionTime: lastTransition,
					AutoScalers:        prevAutoScalers,
				})
				result := Decide(now, mpa, nil, vpas)

				if expectNil {
					Expect(result).To(BeNil())
					return
				}
				Expect(result).NotTo(BeNil())
				Expect(result.ActiveAutoscaler).NotTo(BeNil())
				Expect(result.ActiveAutoscaler.Name).To(Equal(expectVPAName))
				Expect(result.ActiveAutoscaler.ScalerType).To(Equal(mpav1alpha1.VerticalPodAutoscalerScalerType))
				Expect(result.LastTransitionTime.Time).To(BeTemporally("~", now, time.Second))
			},
			Entry("stable window elapsed → promote best ready VPA",
				nil,
				ptrTime(longAgo), []*VPAInfo{vpa("vpa-1", true)}, false, "vpa-1"),
			Entry("stable window not elapsed → no change",
				// prev.AutoScalers must match current to avoid triggering autoScalersChanged.
				[]mpav1alpha1.AutoScalerRef{{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true}},
				ptrTime(justNow), []*VPAInfo{vpa("vpa-1", true)}, true, ""),
			Entry("stable window elapsed but no VPAs → no change",
				nil,
				ptrTime(longAgo), nil, true, ""),
			Entry("stable window elapsed but only unready VPAs exist → no change",
				// prev.AutoScalers must match current to avoid triggering autoScalersChanged.
				[]mpav1alpha1.AutoScalerRef{{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: false}},
				ptrTime(longAgo), []*VPAInfo{vpa("vpa-1", false)}, true, ""),
			Entry("nil last transition (first reconcile) but VPA unready → no change",
				// prev.AutoScalers must match current to avoid triggering autoScalersChanged.
				[]mpav1alpha1.AutoScalerRef{{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: false}},
				nil, []*VPAInfo{vpa("vpa-1", false)}, true, ""),
			Entry("nil last transition (first reconcile) with ready VPA → promote immediately",
				nil,
				nil, []*VPAInfo{vpa("vpa-1", true)}, false, "vpa-1"),
		)
	})

	// ── Active VPA token rotation ─────────────────────────────────────────────
	Describe("active VPA token hold", func() {
		DescribeTable("token rotation behaviour",
			func(prevAutoScalers []mpav1alpha1.AutoScalerRef, lastTransition *metav1.Time, vpas []*VPAInfo, expectNil bool, expectVPAName string) {
				mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
					ActiveAutoscaler: &mpav1alpha1.AutoScalerRef{
						Name:       "vpa-1",
						ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
					},
					LastTransitionTime: lastTransition,
					AutoScalers:        prevAutoScalers,
				})
				result := Decide(now, mpa, nil, vpas)

				if expectNil {
					Expect(result).To(BeNil())
					return
				}
				Expect(result).NotTo(BeNil())
				Expect(result.ActiveAutoscaler).NotTo(BeNil())
				Expect(result.ActiveAutoscaler.Name).To(Equal(expectVPAName))
			},
			Entry("token not expired → no change",
				// prev.AutoScalers matches current exactly so only the token check matters.
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
					{Name: "vpa-2", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				ptrTime(justNow), []*VPAInfo{vpa("vpa-1", true), vpa("vpa-2", true)}, true, ""),
			Entry("token expired, same winner → no change",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				ptrTime(longAgo), []*VPAInfo{vpa("vpa-1", true)}, true, ""),
			Entry("token expired, new winner → rotate to vpa-2",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-2", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				ptrTime(longAgo), []*VPAInfo{vpa("vpa-2", true), vpa("vpa-1", true)}, false, "vpa-2"),
		)
	})

	// ── Active VPA deleted ────────────────────────────────────────────────────
	Describe("active VPA is deleted", func() {
		It("switches to next best VPA immediately when active VPA is deleted", func() {
			mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
				ActiveAutoscaler: &mpav1alpha1.AutoScalerRef{
					Name:       "vpa-1",
					ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
				},
				LastTransitionTime: ptrTime(justNow), // token not expired
				AutoScalers: []mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
					{Name: "vpa-2", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
			})
			// vpa-1 (the active one) has been deleted; only vpa-2 remains.
			result := Decide(now, mpa, nil, []*VPAInfo{vpa("vpa-2", true)})

			Expect(result).NotTo(BeNil())
			Expect(result.ActiveAutoscaler).NotTo(BeNil())
			Expect(result.ActiveAutoscaler.Name).To(Equal("vpa-2"), "should switch to next best VPA")
			Expect(result.LastTransitionTime.Time).To(BeTemporally("~", now, time.Second),
				"transition time should be reset")
		})

		It("clears activeAutoscaler when no other VPA remains", func() {
			mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
				ActiveAutoscaler: &mpav1alpha1.AutoScalerRef{
					Name:       "vpa-1",
					ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
				},
				LastTransitionTime: ptrTime(justNow),
				AutoScalers: []mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
			})
			// vpa-1 deleted, nothing left.
			result := Decide(now, mpa, nil, nil)

			Expect(result).NotTo(BeNil())
			Expect(result.ActiveAutoscaler).To(BeNil(), "active autoscaler should be cleared")
			Expect(result.LastTransitionTime.Time).To(BeTemporally("~", now, time.Second))
		})
	})

	// ── AutoScalers list change detection (new logic) ─────────────────────────
	Describe("autoScalers list changes while active autoscaler is unchanged", func() {
		DescribeTable("returns update only when list actually changed",
			func(
				prevAutoScalers []mpav1alpha1.AutoScalerRef,
				hpas []*autoscalingv2.HorizontalPodAutoscaler,
				vpas []*VPAInfo,
				expectNil bool,
				expectAutoScalerCount int,
			) {
				prevActive := &mpav1alpha1.AutoScalerRef{
					Name:       "vpa-1",
					ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
				}
				mpa := mpaWith(mpav1alpha1.MultidimPodAutoscalerStatus{
					ActiveAutoscaler:   prevActive,
					LastTransitionTime: ptrTime(justNow), // token not expired
					AutoScalers:        prevAutoScalers,
				})

				result := Decide(now, mpa, hpas, vpas)

				if expectNil {
					Expect(result).To(BeNil())
				} else {
					Expect(result).NotTo(BeNil())
					// Active autoscaler must be carried forward unchanged.
					Expect(result.ActiveAutoscaler).To(Equal(prevActive))
					// Transition time must be unchanged.
					Expect(result.LastTransitionTime).To(Equal(ptrTime(justNow)))
					// Updated list must have the expected length.
					Expect(result.AutoScalers).To(HaveLen(expectAutoScalerCount))
				}
			},
			Entry("identical list → nil (no update)",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				nil,
				[]*VPAInfo{vpa("vpa-1", true)},
				true, 0,
			),
			Entry("new VPA appears → update returned",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				nil,
				[]*VPAInfo{vpa("vpa-1", true), vpa("vpa-2", false)},
				false, 2,
			),
			Entry("VPA disappears → update returned",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
					{Name: "vpa-2", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: false},
				},
				nil,
				[]*VPAInfo{vpa("vpa-1", true)},
				false, 1,
			),
			Entry("InProgress flips on existing VPA → update returned",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: false},
				},
				nil,
				[]*VPAInfo{vpa("vpa-1", true)},
				false, 1,
			),
			Entry("new HPA appears → update returned",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				[]*autoscalingv2.HorizontalPodAutoscaler{stableHPA("hpa-1")},
				[]*VPAInfo{vpa("vpa-1", true)},
				false, 2,
			),
			Entry("order differs but content identical → nil (no update)",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "vpa-2", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: false},
					{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				nil,
				// Decide always appends HPAs then VPAs; here we only pass VPAs but
				// in reversed order vs prev — the set comparison must ignore order.
				[]*VPAInfo{vpa("vpa-1", true), vpa("vpa-2", false)},
				true, 0,
			),
		)
	})

	// ── autoScalersChanged unit tests ─────────────────────────────────────────
	Describe("autoScalersChanged", func() {
		DescribeTable("correctly detects differences",
			func(prev, next []mpav1alpha1.AutoScalerRef, want bool) {
				Expect(autoScalersChanged(prev, next)).To(Equal(want))
			},
			Entry("both empty → false",
				[]mpav1alpha1.AutoScalerRef{},
				[]mpav1alpha1.AutoScalerRef{},
				false,
			),
			Entry("same single entry → false",
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType, InProgress: true}},
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType, InProgress: true}},
				false,
			),
			Entry("different lengths → true",
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType}},
				[]mpav1alpha1.AutoScalerRef{},
				true,
			),
			Entry("InProgress differs → true",
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: false}},
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true}},
				true,
			),
			Entry("name differs → true",
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType}},
				[]mpav1alpha1.AutoScalerRef{{Name: "b", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType}},
				true,
			),
			Entry("scaler type differs → true",
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType}},
				[]mpav1alpha1.AutoScalerRef{{Name: "a", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType}},
				true,
			),
			Entry("same entries in different order → false",
				[]mpav1alpha1.AutoScalerRef{
					{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType, InProgress: false},
					{Name: "b", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
				},
				[]mpav1alpha1.AutoScalerRef{
					{Name: "b", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
					{Name: "a", ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType, InProgress: false},
				},
				false,
			),
		)
	})

	// ── pickBestVPA priority selection ────────────────────────────────────────
	Describe("pickBestVPA", func() {
		older := now.Add(-2 * time.Hour)
		newer := now.Add(-1 * time.Hour)

		DescribeTable("selects the VPA with the lowest priority value",
			func(vpas []*VPAInfo, wantName string, wantNil bool) {
				got := pickBestVPA(vpas)
				if wantNil {
					Expect(got).To(BeNil())
					return
				}
				Expect(got).NotTo(BeNil())
				Expect(got.Name).To(Equal(wantName))
			},
			Entry("empty list → nil",
				[]*VPAInfo{}, "", true),
			Entry("single ready VPA → returned",
				[]*VPAInfo{vpaWithPriority("vpa-a", true, 2, older)}, "vpa-a", false),
			Entry("lower priority number wins",
				[]*VPAInfo{
					vpaWithPriority("vpa-low", true, 5, older),
					vpaWithPriority("vpa-high", true, 1, newer),
				}, "vpa-high", false),
			Entry("equal priority → older creation timestamp wins",
				[]*VPAInfo{
					vpaWithPriority("vpa-newer", true, 0, newer),
					vpaWithPriority("vpa-older", true, 0, older),
				}, "vpa-older", false),
			Entry("nil-priority VPAs (both priority=0) use creation timestamp",
				[]*VPAInfo{
					vpaWithPriority("vpa-newer", true, 0, newer),
					vpaWithPriority("vpa-oldest", true, 0, older),
					vpaWithPriority("vpa-mid", true, 0, now.Add(-90*time.Minute)),
				}, "vpa-oldest", false),
			Entry("unready VPA is ignored",
				[]*VPAInfo{
					vpaWithPriority("vpa-unready", false, 0, older),
					vpaWithPriority("vpa-ready", true, 5, newer),
				}, "vpa-ready", false),
			Entry("all VPAs unready → nil",
				[]*VPAInfo{
					vpaWithPriority("vpa-a", false, 0, older),
					vpaWithPriority("vpa-b", false, 1, newer),
				}, "", true),
		)
	})
})
