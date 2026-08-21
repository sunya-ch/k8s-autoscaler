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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v2"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/autoscaler/multidimensional-pod-autoscaler/internal/lister"
	"k8s.io/autoscaler/multidimensional-pod-autoscaler/internal/multidim"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	mpaclientset "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	mpalisters "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1alpha1"
	vpaclientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	vpalisters "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1"
)

// Reconciler implements the MPA coordination logic.
// It is separated from the controller wiring so that it can be unit-tested
// independently from informer setup.
type Reconciler struct {
	kubeClient kubernetes.Interface
	mpaClient  mpaclientset.Interface
	vpaClient  vpaclientset.Interface

	mpaLister mpalisters.MultidimPodAutoscalerLister
	hpaLister autoscalinglisters.HorizontalPodAutoscalerLister
	vpaLister vpalisters.VerticalPodAutoscalerLister

	// clock allows tests to inject a fake time source.
	clock func() time.Time
}

func newReconciler(
	kubeClient kubernetes.Interface,
	mpaClient mpaclientset.Interface,
	vpaClient vpaclientset.Interface,
	mpaLister mpalisters.MultidimPodAutoscalerLister,
	hpaLister autoscalinglisters.HorizontalPodAutoscalerLister,
	vpaLister vpalisters.VerticalPodAutoscalerLister,
) *Reconciler {
	return &Reconciler{
		kubeClient: kubeClient,
		mpaClient:  mpaClient,
		vpaClient:  vpaClient,
		mpaLister:  mpaLister,
		hpaLister:  hpaLister,
		vpaLister:  vpaLister,
		clock:      time.Now,
	}
}

// newReconcilerFromClient constructs a Reconciler whose listers are backed by
// the controller-runtime cached client.  This is the constructor used in
// production; newReconciler (typed listers) is still available for unit tests
// that inject fakes directly.
func newReconcilerFromClient(
	c client.Client,
	mpaClient mpaclientset.Interface,
	vpaClient vpaclientset.Interface,
) *Reconciler {
	return &Reconciler{
		mpaClient: mpaClient,
		vpaClient: vpaClient,
		mpaLister: &lister.CtrlMPALister{Client: c},
		hpaLister: &lister.CtrlHPALister{Client: c},
		vpaLister: &lister.CtrlVPALister{Client: c},
		clock:     time.Now,
	}
}

// Reconcile is the main entry point called for each event.
//
// namespace and name identify the MultidimPodAutoscaler to reconcile.
// The returned duration is a hint to the caller: when > 0 the MPA should be
// re-enqueued after that interval even if no watch event fires.  Two cases
// require a timed requeue:
//
//  1. activeAutoscaler == nil (HPA just stabilised, waiting for stableDurationSeconds
//     to elapse before VPA can resume) — requeue when the window expires.
//  2. activeAutoscaler is a VPA and the token has not yet expired — requeue at
//     the moment the token expires so rotation is evaluated promptly.
func (r *Reconciler) Reconcile(ctx context.Context, namespace, name string) (time.Duration, error) {
	klog.V(4).InfoS("Reconcile started", "mpa", klog.KRef(namespace, name))

	mpa, err := r.mpaLister.MultidimPodAutoscalers(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(4).InfoS("MPA not found — already deleted", "mpa", klog.KRef(namespace, name))
			return 0, nil
		}
		return 0, fmt.Errorf("getting MPA %s/%s: %w", namespace, name, err)
	}

	// Handle deletion: remove finalizer after clearing spec.paused on all VPAs.
	if !mpa.DeletionTimestamp.IsZero() {
		klog.V(3).InfoS("MPA is being deleted — running deletion handler", "mpa", klog.KObj(mpa))
		return 0, r.handleDeletion(ctx, mpa)
	}
	// Ensure our finalizer is present.
	if err := r.ensureFinalizer(ctx, mpa); err != nil {
		return 0, err
	}

	// Only one MPA may coordinate a given targetRef in a namespace.
	// The winner is the oldest by CreationTimestamp; ties break on name.
	// If this MPA loses, stamp Conflicted=True and stop — no VPA writes.
	if winner, conflict := r.checkConflict(mpa); conflict {
		klog.V(2).InfoS("MPA conflicts with another MPA for the same targetRef",
			"mpa", klog.KObj(mpa), "winner", winner)
		newStatus := mpa.Status.DeepCopy()
		setCondition(newStatus, metav1.Condition{
			Type:               mpav1alpha1.ConditionTypeConflicted,
			Status:             metav1.ConditionTrue,
			Reason:             "TargetRefConflict",
			Message:            fmt.Sprintf("targetRef already claimed by MPA %q", winner),
			LastTransitionTime: metav1.Now(),
		})
		removeCondition(newStatus, mpav1alpha1.ConditionTypeActive)
		return 0, r.updateStatus(ctx, mpa, *newStatus)
	}

	// Discover scalers
	hpas, err := multidim.DiscoverHPAs(r.hpaLister, mpa)
	if err != nil {
		return 0, fmt.Errorf("discovering HPAs for MPA %s/%s: %w", namespace, name, err)
	}
	vpas, err := multidim.DiscoverVPAs(r.vpaLister, mpa.Spec.TargetRef, mpa.Namespace)
	if err != nil {
		return 0, fmt.Errorf("discovering VPAs for MPA %s/%s: %w", namespace, name, err)
	}
	klog.V(4).InfoS("Discovered scalers",
		"mpa", klog.KObj(mpa),
		"hpaCount", len(hpas),
		"vpaCount", len(vpas),
	)

	if len(hpas) == 0 && len(vpas) == 0 {
		klog.V(4).InfoS("No scalers found for MPA, skipping", "mpa", klog.KObj(mpa))
		noScalers := buildStatusNoScalers()
		setCondition(&noScalers, metav1.Condition{
			Type:               mpav1alpha1.ConditionTypeActive,
			Status:             metav1.ConditionFalse,
			Reason:             "NoScalersFound",
			Message:            "No HPA or VPA discovered for spec.targetRef",
			LastTransitionTime: metav1.Now(),
		})
		removeCondition(&noScalers, mpav1alpha1.ConditionTypeConflicted)
		return 0, r.updateStatus(ctx, mpa, noScalers)
	}

	// Decide active autoscaler
	now := r.clock()
	newStatus := multidim.Decide(now, mpa, hpas, vpas)

	// Determine the active autoscaler for pause enforcement.
	// When Decide returns nil (no status change) we use the existing status.
	activeAutoscaler := mpa.Status.ActiveAutoscaler
	lastTransitionTime := mpa.Status.LastTransitionTime
	if newStatus != nil {
		activeAutoscaler = newStatus.ActiveAutoscaler
		lastTransitionTime = newStatus.LastTransitionTime
	}

	// Always enforce VPA pause state, regardless of whether the status changed.
	// The informer cache may lag behind a previous patch, so we must re-apply on
	// every reconcile to converge correctly.
	if err := multidim.EnforceVPAPauseState(ctx, r.vpaClient, mpa, vpas, activeAutoscaler); err != nil {
		return 0, err
	}

	if newStatus != nil {
		activeStr := "<none>"
		if newStatus.ActiveAutoscaler != nil {
			activeStr = string(newStatus.ActiveAutoscaler.ScalerType) + "/" + newStatus.ActiveAutoscaler.Name
		}
		klog.V(3).InfoS("Status change decided",
			"mpa", klog.KObj(mpa),
			"newActive", activeStr,
		)
		// Mark this MPA as the active (non-conflicted) coordinator.
		setCondition(newStatus, metav1.Condition{
			Type:               mpav1alpha1.ConditionTypeActive,
			Status:             metav1.ConditionTrue,
			Reason:             "Coordinating",
			Message:            "MPA is the sole owner of spec.targetRef and is coordinating scalers",
			LastTransitionTime: metav1.Now(),
		})
		removeCondition(newStatus, mpav1alpha1.ConditionTypeConflicted)
		// Persist updated status
		if err := r.updateStatus(ctx, mpa, *newStatus); err != nil {
			return 0, err
		}
	} else {
		klog.V(5).InfoS("Reconcile: no status change needed", "mpa", klog.KObj(mpa))
	}

	// Compute a timed requeue so the controller wakes up at the right moment
	// even when no watch event fires in the interim.
	requeue := requeueAfter(now, mpa, activeAutoscaler, lastTransitionTime)
	if requeue > 0 {
		klog.V(4).InfoS("Scheduling timed requeue",
			"mpa", klog.KObj(mpa),
			"requeueAfter", requeue,
		)
	}
	return requeue, nil
}

// requeueAfter returns how long the controller should wait before re-checking
// this MPA, based on the active autoscaler and the last transition time.
//
// Two cases need a timed wake-up:
//   - activeAutoscaler == nil: we are in the stable window waiting to promote a
//     VPA.  Requeue when the window expires so VPA promotion is not delayed by
//     an absence of watch events.
//   - activeAutoscaler is a VPA and the token has not yet expired: requeue when
//     the token expires so VPA rotation is evaluated promptly.
//
// All other cases (HPA active, no lastTransitionTime, token already expired)
// return 0 — the next watch event is sufficient.
func requeueAfter(
	now time.Time,
	mpa *mpav1alpha1.MultidimPodAutoscaler,
	activeAutoscaler *mpav1alpha1.AutoScalerRef,
	lastTransitionTime *metav1.Time,
) time.Duration {
	if lastTransitionTime == nil {
		return 0
	}
	switch {
	case activeAutoscaler == nil:
		// Waiting for stableDurationSeconds to elapse.
		stableDur := multidim.StableDuration(mpa)
		remaining := stableDur - now.Sub(lastTransitionTime.Time)
		if remaining > 0 {
			return remaining
		}
	case activeAutoscaler.ScalerType == mpav1alpha1.VerticalPodAutoscalerScalerType:
		// Waiting for the token to expire.
		tokenDur := multidim.TokenDuration(mpa)
		remaining := tokenDur - now.Sub(lastTransitionTime.Time)
		if remaining > 0 {
			return remaining
		}
	}
	return 0
}

// ── Finalizer ────────────────────────────────────────────────────────────────

func (r *Reconciler) ensureFinalizer(ctx context.Context, mpa *mpav1alpha1.MultidimPodAutoscaler) error {
	for _, f := range mpa.Finalizers {
		if f == mpaFinalizer {
			return nil
		}
	}
	klog.V(4).InfoS("Adding finalizer to MPA", "mpa", klog.KObj(mpa), "finalizer", mpaFinalizer)
	patch := []byte(`{"metadata":{"finalizers":["` + mpaFinalizer + `"]}}`)
	_, err := r.mpaClient.AutoscalingV1alpha1().MultidimPodAutoscalers(mpa.Namespace).
		Patch(ctx, mpa.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (r *Reconciler) handleDeletion(ctx context.Context, mpa *mpav1alpha1.MultidimPodAutoscaler) error {
	hasFinalizer := false
	for _, f := range mpa.Finalizers {
		if f == mpaFinalizer {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		klog.V(4).InfoS("MPA has no finalizer — deletion already clean", "mpa", klog.KObj(mpa))
		return nil
	}

	// Unpause every VPA that was managed by this MPA (annotation matches).
	mpaKey := mpa.Namespace + "/" + mpa.Name
	vpas, err := multidim.DiscoverVPAs(r.vpaLister, mpa.Spec.TargetRef, mpa.Namespace)
	if err != nil {
		return fmt.Errorf("listing VPAs during MPA deletion: %w", err)
	}
	klog.V(4).InfoS("Unpausing owned VPAs before MPA deletion",
		"mpa", klog.KObj(mpa),
		"vpaCount", len(vpas),
	)
	for _, vpa := range vpas {
		// Only clear VPAs we own — never touch VPAs paused by a different MPA.
		if vpa.PausedByMPA != mpaKey {
			continue
		}
		klog.V(4).InfoS("Clearing paused state on owned VPA",
			"mpa", klog.KObj(mpa),
			"vpa", klog.KRef(vpa.Namespace, vpa.Name),
		)
		if err := multidim.SetVPAPaused(ctx, r.vpaClient, vpa.Namespace, vpa.Name, false, mpaKey); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("clearing paused on VPA %s/%s: %w", vpa.Namespace, vpa.Name, err)
		}
	}

	// Remove our finalizer.
	klog.V(4).InfoS("Removing finalizer from MPA", "mpa", klog.KObj(mpa), "finalizer", mpaFinalizer)
	patchData, err := buildRemoveFinalizerPatch(mpa.Finalizers)
	if err != nil {
		return err
	}
	_, err = r.mpaClient.AutoscalingV1alpha1().MultidimPodAutoscalers(mpa.Namespace).
		Patch(ctx, mpa.Name, types.MergePatchType, patchData, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		// The MPA was already fully removed — finalizer is gone, nothing to do.
		klog.V(4).InfoS("MPA already deleted when removing finalizer — skipping", "mpa", klog.KObj(mpa))
		return nil
	}
	return err
}

// ── Conflict detection ───────────────────────────────────────────────────────

// checkConflict returns (winnerKey, true) when another non-deleting MPA in the
// same namespace already claims the same targetRef and wins the tiebreak.
//
// Tiebreak order (first criterion that differs decides):
//  1. Older CreationTimestamp wins.
//  2. Lexicographically smaller name wins (stable across controller restarts).
//
// Returns ("", false) when this MPA is the rightful owner.
func (r *Reconciler) checkConflict(mpa *mpav1alpha1.MultidimPodAutoscaler) (winner string, conflict bool) {
	all, err := r.mpaLister.MultidimPodAutoscalers(mpa.Namespace).List(labels.Everything())
	if err != nil {
		// On lister error be conservative: assume no conflict so we don't
		// accidentally freeze all MPAs in the namespace.
		return "", false
	}

	ref := mpa.Spec.TargetRef
	for _, other := range all {
		if other.Name == mpa.Name {
			continue // skip self
		}
		if other.DeletionTimestamp != nil {
			continue // deleting MPAs do not count
		}
		oref := other.Spec.TargetRef
		if oref.Name != ref.Name || oref.Kind != ref.Kind || oref.APIVersion != ref.APIVersion {
			continue // different workload
		}

		// Same targetRef — apply tiebreak: older / lower-name wins.
		otherWins := other.CreationTimestamp.Before(&mpa.CreationTimestamp) ||
			(other.CreationTimestamp.Equal(&mpa.CreationTimestamp) && other.Name < mpa.Name)

		if otherWins {
			return other.Namespace + "/" + other.Name, true
		}
	}
	return "", false
}

// ── Status update ────────────────────────────────────────────────────────────

// updateStatus retries on resource-version conflict so transient API server
// rejections don't drop the MPA's status update on the floor.
func (r *Reconciler) updateStatus(
	ctx context.Context,
	mpa *mpav1alpha1.MultidimPodAutoscaler,
	status mpav1alpha1.MultidimPodAutoscalerStatus,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mpaLister.MultidimPodAutoscalers(mpa.Namespace).Get(mpa.Name)
		if err != nil {
			return err
		}
		updated := current.DeepCopy()
		updated.Status = status
		_, err = r.mpaClient.AutoscalingV1alpha1().MultidimPodAutoscalers(mpa.Namespace).
			UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		return err
	})
}

// ── Helpers ─────────────────────────────────────────────────────────

// setCondition upserts cond into status.Conditions.
// When an existing condition of the same type already has identical
// Status/Reason/Message, LastTransitionTime is preserved to avoid
// spurious status writes.
func setCondition(status *mpav1alpha1.MultidimPodAutoscalerStatus, cond metav1.Condition) {
	for i, existing := range status.Conditions {
		if existing.Type != cond.Type {
			continue
		}
		if existing.Status == cond.Status &&
			existing.Reason == cond.Reason &&
			existing.Message == cond.Message {
			return // nothing changed — keep existing LastTransitionTime
		}
		status.Conditions[i] = cond
		return
	}
	status.Conditions = append(status.Conditions, cond)
}

// removeCondition removes the condition with the given type from
// status.Conditions, no-op if absent.
func removeCondition(status *mpav1alpha1.MultidimPodAutoscalerStatus, condType string) {
	out := status.Conditions[:0]
	for _, c := range status.Conditions {
		if c.Type != condType {
			out = append(out, c)
		}
	}
	status.Conditions = out
}

// buildRemoveFinalizerPatch returns a merge-patch that removes mpaFinalizer
// from the existing finalizer list.
func buildRemoveFinalizerPatch(current []string) ([]byte, error) {
	remaining := make([]string, 0, len(current))
	for _, f := range current {
		if f != mpaFinalizer {
			remaining = append(remaining, f)
		}
	}
	type metadata struct {
		Finalizers []string `json:"finalizers"`
	}
	type patch struct {
		Metadata metadata `json:"metadata"`
	}
	return json.Marshal(patch{Metadata: metadata{Finalizers: remaining}})
}

func buildStatusNoScalers() mpav1alpha1.MultidimPodAutoscalerStatus {
	return mpav1alpha1.MultidimPodAutoscalerStatus{}
}
