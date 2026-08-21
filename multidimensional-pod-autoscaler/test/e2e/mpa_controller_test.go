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

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

// All tests share one clients instance, constructed in BeforeSuite.
var c *clients

var _ = BeforeSuite(func() {
	c = newClients()
})

// ── VPA pause/unpause coordination ─────────────────────────────────────────────

var _ = Describe("MultidimPodAutoscaler controller", func() {

	Describe("VPA pause coordination", Ordered, func() {
		var (
			ctx           context.Context
			cancelCtx     context.CancelFunc
			ns            string
			deployN       string
			vpaName       string
			lowPriVPAName string
			mpaName       string
		)

		BeforeAll(func() {
			// Give the entire ordered block a hard deadline so that any
			// individual polling call that gets stuck will be cancelled rather
			// than hanging indefinitely.
			ctx, cancelCtx = context.WithTimeout(context.Background(), testSuiteTimeout)
			ns = *flagNamespace
			deployN = uniqueName("hamster")
			vpaName = uniqueName("vpa")
			mpaName = uniqueName("mpa")

			By("creating the hamster deployment")
			createDeploymentAndWait(ctx, c, newHamsterDeployment(ns, deployN, 2, "100m", "100Mi"))

			By("creating the VPA in Recreate mode")
			createVPA(ctx, c, newVPA(ns, vpaName, deployN, vpav1.UpdateModeRecreate, nil))

			By("creating the MPA with a 2 s stable window and 10 s token duration")
			stableSecs := int32(2)
			tokenSecs := int32(10)
			createMPA(ctx, c, newMPA(ns, mpaName, deployN, stableSecs, tokenSecs))
		})

		AfterAll(func() {
			// Use a fresh background context — the suite ctx may already be
			// cancelled if a step timed out or the ordered block was aborted.
			cleanupAll(context.Background(), c, ns,
				[]string{deployN},
				[]string{vpaName, lowPriVPAName},
				nil,
				[]string{mpaName},
			)
			cancelCtx()
		})

		It("becomes Active once it discovers the VPA", NodeTimeout(pollTimeout), func(ctx context.Context) {
			waitForMPAActive(ctx, c, ns, mpaName)
		})

		It("activates the VPA and unpauses it after stableDurationSeconds", NodeTimeout(pollTimeout), func(ctx context.Context) {
			// After the stable window elapses the MPA should select the only VPA
			// as its active autoscaler and clear spec.paused on it.
			// The MPA only promotes a VPA once it has produced a recommendation,
			// so simulate the recommender first.
			simulateVPARecommendation(ctx, c, ns, vpaName)
			waitForMPAActiveAutoscaler(ctx, c, ns, mpaName,
				vpaName, mpav1alpha1.VerticalPodAutoscalerScalerType)
			waitForVPAPausedState(ctx, c, ns, vpaName, false)
		})

		It("pauses a lower-priority VPA and stamps it with the owner annotation", NodeTimeout(pollTimeout), func(ctx context.Context) {
			// A second VPA with priority=99 should lose the selection and be paused
			// by the MPA. The MPA key annotation must be set on it.
			lowPriVPAName = uniqueName("vpa-low")
			lowPri := int32(99)
			createVPA(ctx, c, newVPA(ns, lowPriVPAName, deployN, vpav1.UpdateModeRecreate, &lowPri))

			By("simulating a recommendation on the low-priority VPA")
			simulateVPARecommendation(ctx, c, ns, lowPriVPAName)

			waitForVPAPausedState(ctx, c, ns, lowPriVPAName, true)
			waitForVPAAnnotation(ctx, c, ns, lowPriVPAName, ns+"/"+mpaName)
		})

		It("never annotates the winning VPA with the paused-by-mpa annotation", NodeTimeout(30*time.Second), func(ctx context.Context) {
			// The high-priority (winner) VPA should never carry the owner annotation.
			Consistently(func() string {
				v, err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(ns).
					Get(ctx, vpaName, metav1.GetOptions{})
				if err != nil {
					return "error"
				}
				return v.Annotations[mpav1alpha1.AnnotationPausedByMPA]
			}, 10*time.Second, 2*time.Second).Should(BeEmpty(),
				"winning VPA should never carry the paused-by-mpa annotation")
		})

		It("clears the active autoscaler and promotes the lower-priority VPA when the winner is deleted", NodeTimeout(pollTimeout), func(ctx context.Context) {
			// Confirm the low-priority VPA is still paused before we start, so
			// the test is not sensitive to state drift from previous steps.
			By("confirming the low-priority VPA is paused before deletion")
			waitForVPAPausedState(ctx, c, ns, lowPriVPAName, true)

			// Re-simulate the recommendation now so it is guaranteed to be
			// present in the VPA status when the stable window elapses after
			// the winner is deleted — a live VPA recommender may have
			// overwritten our previous simulated condition.
			By("refreshing the VPA recommendation on the low-priority VPA")
			simulateVPARecommendation(ctx, c, ns, lowPriVPAName)

			By("deleting the winning (high-priority) VPA")
			Expect(c.vpa.AutoscalingV1().VerticalPodAutoscalers(ns).
				Delete(ctx, vpaName, metav1.DeleteOptions{})).To(Succeed())
			// AfterAll still references vpaName in the cleanup list; mark it
			// empty so cleanupAll's delete is a no-op for an already-deleted VPA.
			vpaName = ""

			By("MPA should promote the low-priority VPA as the new active autoscaler")
			waitForMPAActiveAutoscaler(ctx, c, ns, mpaName,
				lowPriVPAName, mpav1alpha1.VerticalPodAutoscalerScalerType)

			By("the formerly-paused VPA should now be unpaused")
			waitForVPAPausedState(ctx, c, ns, lowPriVPAName, false)
		})
	})

	// ── HPA preemption ──────────────────────────────────────────────────────────
	// Run the same three-step scenario for each VPA update mode that is relevant
	// when HPA and VPA co-exist: Recreate (pod-replacement) and
	// InPlaceOrRecreate (resize-in-place, falling back to recreation).

	for _, updateMode := range []vpav1.UpdateMode{
		vpav1.UpdateModeRecreate,
		vpav1.UpdateModeInPlaceOrRecreate,
	} {
		updateMode := updateMode // capture for closure

		Describe("HPA preemption with VPA updateMode="+string(updateMode), Ordered, func() {
			var (
				ctx       context.Context
				cancelCtx context.CancelFunc
				ns        string
				deployN   string
				vpaName   string
				hpaName   string
				mpaName   string
			)

			BeforeAll(func() {
				// Hard deadline for the entire ordered block.
				ctx, cancelCtx = context.WithTimeout(context.Background(), testSuiteTimeout)
				ns = *flagNamespace
				deployN = uniqueName("hamster")
				vpaName = uniqueName("vpa")
				hpaName = uniqueName("hpa")
				mpaName = uniqueName("mpa")

				By("creating the hamster deployment")
				createDeploymentAndWait(ctx, c, newHamsterDeployment(ns, deployN, 2, "100m", "100Mi"))

				By("creating the VPA with updateMode=" + string(updateMode))
				createVPA(ctx, c, newVPA(ns, vpaName, deployN, updateMode, nil))

				By("creating the HPA targeting the same deployment")
				createHPA(ctx, c, newHPA(ns, hpaName, deployN, 2, 10, 60))

				By("creating the MPA with a 5 s stable window")
				stableSecs := int32(5)
				tokenSecs := int32(30)
				createMPA(ctx, c, newMPA(ns, mpaName, deployN, stableSecs, tokenSecs))
			})

			AfterAll(func() {
				cleanupAll(context.Background(), c, ns,
					[]string{deployN},
					[]string{vpaName},
					[]string{hpaName},
					[]string{mpaName},
				)
				cancelCtx()
			})

			It("gives the VPA the token when the HPA is stable", NodeTimeout(pollTimeout), func(ctx context.Context) {
				// The MPA only promotes the VPA after it has a recommendation.
				simulateVPARecommendation(ctx, c, ns, vpaName)
				simulateHPAStable(ctx, c, ns, hpaName, 2)
				waitForMPAActiveAutoscaler(ctx, c, ns, mpaName,
					vpaName, mpav1alpha1.VerticalPodAutoscalerScalerType)
				waitForVPAPausedState(ctx, c, ns, vpaName, false)
			})

			It("gives the HPA the token and pauses the VPA when the HPA starts scaling", NodeTimeout(pollTimeout), func(ctx context.Context) {
				By("simulating a scale-out on the HPA")
				simulateHPAScaling(ctx, c, ns, hpaName, 2, 5)

				By("MPA should switch the active autoscaler to the HPA")
				waitForMPAActiveAutoscaler(ctx, c, ns, mpaName,
					hpaName, mpav1alpha1.HorizontalPodAutoscalerScalerType)

				By("VPA should be paused while the HPA holds the token")
				waitForVPAPausedState(ctx, c, ns, vpaName, true)
			})

			It("returns the token to the VPA once the HPA stabilises", NodeTimeout(2*pollTimeout), func(ctx context.Context) {
				By("marking the HPA as stable")
				simulateHPAStable(ctx, c, ns, hpaName, 5)

				By("MPA should clear the active autoscaler during the settling window")
				waitForMPANoActiveAutoscaler(ctx, c, ns, mpaName)

				By("simulating a VPA recommendation before re-activation")
				simulateVPARecommendation(ctx, c, ns, vpaName)

				By("MPA should re-activate the VPA after the stable window")
				waitForMPAActiveAutoscaler(ctx, c, ns, mpaName,
					vpaName, mpav1alpha1.VerticalPodAutoscalerScalerType)
				waitForVPAPausedState(ctx, c, ns, vpaName, false)
			})
		})
	}

	// ── Conflict detection ──────────────────────────────────────────────────────

	Describe("conflict detection", Ordered, func() {
		var (
			ctx       context.Context
			cancelCtx context.CancelFunc
			ns        string
			deployN   string
			vpaName   string
			mpa1      string
			mpa2      string
		)

		BeforeAll(func() {
			ctx, cancelCtx = context.WithTimeout(context.Background(), testSuiteTimeout)
			ns = *flagNamespace
			deployN = uniqueName("hamster")
			vpaName = uniqueName("vpa")
			mpa1 = uniqueName("mpa-first")
			mpa2 = uniqueName("mpa-second")

			By("creating the workload")
			createDeploymentAndWait(ctx, c, newHamsterDeployment(ns, deployN, 1, "50m", "64Mi"))

			By("creating a VPA so the winning MPA can discover at least one scaler")
			createVPA(ctx, c, newVPA(ns, vpaName, deployN, vpav1.UpdateModeRecreate, nil))
		})

		AfterAll(func() {
			cleanupAll(context.Background(), c, ns,
				[]string{deployN},
				[]string{vpaName}, nil,
				[]string{mpa1, mpa2},
			)
			cancelCtx()
		})

		It("marks the later MPA as Conflicted when two MPAs share the same targetRef", NodeTimeout(pollTimeout), func(ctx context.Context) {
			stableSecs := int32(0)
			tokenSecs := int32(30)

			By("creating the first (winning) MPA")
			createMPA(ctx, c, newMPA(ns, mpa1, deployN, stableSecs, tokenSecs))
			// Small sleep so mpa2 gets a clearly later creation timestamp.
			time.Sleep(100 * time.Millisecond)

			By("creating the conflicting MPA")
			createMPA(ctx, c, newMPA(ns, mpa2, deployN, stableSecs, tokenSecs))

			By("second MPA should report Conflicted=True")
			Eventually(func() bool {
				m, err := c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(ns).
					Get(ctx, mpa2, metav1.GetOptions{})
				if err != nil {
					return false
				}
				for _, cond := range m.Status.Conditions {
					if cond.Type == mpav1alpha1.ConditionTypeConflicted && cond.Status == "True" {
						return true
					}
				}
				return false
			}, pollTimeout, pollInterval).Should(BeTrue(),
				"second MPA should be marked Conflicted")
		})

		It("leaves the first MPA as Active", NodeTimeout(pollTimeout), func(ctx context.Context) {
			By("simulating a VPA recommendation so the winning MPA can activate")
			simulateVPARecommendation(ctx, c, ns, vpaName)

			waitForMPAActive(ctx, c, ns, mpa1)
		})
	})

	// ── Deletion finalizer ──────────────────────────────────────────────────────

	Describe("deletion finalizer", func() {
		It("unpauses managed VPAs when the MPA is deleted", NodeTimeout(testSuiteTimeout), func(ctx context.Context) {
			ns := *flagNamespace

			deployN := uniqueName("hamster")
			vpaWinner := uniqueName("vpa-winner")
			vpaPaused := uniqueName("vpa-paused")
			mpaName := uniqueName("mpa")

			By("creating the workload")
			createDeploymentAndWait(ctx, c, newHamsterDeployment(ns, deployN, 1, "50m", "64Mi"))
			// Clean up everything regardless of test outcome. The MPA must be
			// deleted first so its finalizer releases the VPAs before we attempt
			// to remove them. Errors are intentionally ignored — cleanup must not
			// mask a real test failure.
			DeferCleanup(func() {
				// Use a fresh context — the It node's ctx is already done at
				// this point, and cleanup must succeed regardless of test outcome.
				cleanCtx := context.Background()
				_ = c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(ns).Delete(cleanCtx, mpaName, metav1.DeleteOptions{})
				_ = c.vpa.AutoscalingV1().VerticalPodAutoscalers(ns).Delete(cleanCtx, vpaWinner, metav1.DeleteOptions{})
				_ = c.vpa.AutoscalingV1().VerticalPodAutoscalers(ns).Delete(cleanCtx, vpaPaused, metav1.DeleteOptions{})
				_ = c.kube.AppsV1().Deployments(ns).Delete(cleanCtx, deployN, metav1.DeleteOptions{})
			})

			By("creating the winning VPA (priority 0)")
			createVPA(ctx, c, newVPA(ns, vpaWinner, deployN, vpav1.UpdateModeRecreate, nil))

			By("creating the losing VPA (priority 99) — MPA will pause this one")
			lowPri := int32(99)
			createVPA(ctx, c, newVPA(ns, vpaPaused, deployN, vpav1.UpdateModeRecreate, &lowPri))

			By("creating the MPA with stableDuration=0 s")
			stableSecs := int32(0)
			tokenSecs := int32(30)
			createMPA(ctx, c, newMPA(ns, mpaName, deployN, stableSecs, tokenSecs))

			By("simulating a recommendation on the winning VPA so the MPA can activate it")
			simulateVPARecommendation(ctx, c, ns, vpaWinner)

			By("waiting for the losing VPA to be paused and annotated")
			waitForVPAPausedState(ctx, c, ns, vpaPaused, true)
			waitForVPAAnnotation(ctx, c, ns, vpaPaused, ns+"/"+mpaName)

			By("deleting the MPA")
			Expect(c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(ns).
				Delete(ctx, mpaName, metav1.DeleteOptions{})).To(Succeed())

			By("the losing VPA should be unpaused and its annotation removed after MPA deletion")
			waitForVPAPausedState(ctx, c, ns, vpaPaused, false)
			waitForVPAAnnotation(ctx, c, ns, vpaPaused, "")
		})
	})
})
