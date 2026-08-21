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

// Package controller implements the MultidimPodAutoscaler controller.
package controller

import (
	"context"
	"sync"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	mpaclientset "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpaclientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
)

const (
	// controllerName is used in log messages and metrics labels.
	controllerName = "multidim-pod-autoscaler"

	// mpaFinalizer is placed on every MPA object to ensure we can clear
	// spec.paused on all managed VPAs before the object is garbage-collected.
	mpaFinalizer = "autoscaling.x-k8s.io/mpa-finalizer"

	// maxRetries is the number of times we retry on transient API errors.
	maxRetries = 5
)

type scalerRef struct {
	APIVersion string
	Kind       string
	Name       string
}

// targetRefKey returns the map key for a targetRef triplet.
// "|" cannot appear in an API group, kind, or object name (all follow DNS /
// identifier rules), so it is a safe separator even when apiVersion already
// contains "/" (e.g. "apps/v1|Deployment|my-app").
func targetRefKey(apiVersion, kind, name string) string {
	return apiVersion + "|" + kind + "|" + name
}

// MPAReconciler implements reconcile.Reconciler for MultidimPodAutoscaler.
//
// The controller-runtime Manager calls Reconcile for every change event on
// the watched types (MPA, HPA, VPA).  All business logic lives in Reconciler
// (reconciler.go); this file only handles the wiring.
type MPAReconciler struct {
	// client is the controller-runtime cached client, used for reads and status
	// updates inside the inner reconciler.
	client client.Client

	// scheme is required by controller-runtime to decode objects.
	scheme *runtime.Scheme

	// kubeClient / mpaClient / vpaClient are raw clientsets used for patching.
	kubeClient kubernetes.Interface
	mpaClient  mpaclientset.Interface
	vpaClient  vpaclientset.Interface

	// inner holds all business logic; tested independently.
	inner *Reconciler

	// mpaIndex is an in-process map from targetRefKey → set of "namespace/name"
	// MPA identifiers.  It is populated and maintained by Reconcile — every
	// successful reconcile of a live MPA adds an entry; deletion removes it.
	// HPA/VPA event handlers read this map to find the MPAs to re-enqueue.
	// All access is serialised by mu.
	mu       sync.RWMutex
	mpaIndex map[string]map[types.NamespacedName]struct{}
}

// NewMPAReconciler constructs an MPAReconciler ready to be registered with a
// ctrl.Manager via SetupWithManager.
func NewMPAReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	kubeClient kubernetes.Interface,
	mpaClient mpaclientset.Interface,
	vpaClient vpaclientset.Interface,
) *MPAReconciler {
	r := &MPAReconciler{
		client:     c,
		scheme:     scheme,
		kubeClient: kubeClient,
		mpaClient:  mpaClient,
		vpaClient:  vpaClient,
		mpaIndex:   make(map[string]map[types.NamespacedName]struct{}),
	}
	r.inner = newReconcilerFromClient(c, mpaClient, vpaClient)
	return r
}

// Reconcile is called by controller-runtime for every enqueued request.
func (r *MPAReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(4).Info("Reconciling MPA", "mpa", req.NamespacedName)

	// Fetch the current MPA to determine index action before delegating.
	mpa := &mpav1alpha1.MultidimPodAutoscaler{}
	if err := r.client.Get(ctx, req.NamespacedName, mpa); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// MPA was deleted — purge from index.
			r.indexDelete(req.NamespacedName)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Delegate all business logic; the returned duration is a timed requeue hint.
	requeue, err := r.inner.Reconcile(ctx, req.Namespace, req.Name)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Keep the index in sync:
	//   • fully deleted (no finalizer left)  → remove
	//   • being deleted (finalizer still present) → remove preemptively so
	//     new HPA/VPA events no longer trigger reconciles for a dying MPA
	//   • live → upsert with current targetRef
	if !mpa.DeletionTimestamp.IsZero() {
		r.indexDelete(req.NamespacedName)
	} else {
		ref := mpa.Spec.TargetRef
		r.indexUpsert(targetRefKey(ref.APIVersion, ref.Kind, ref.Name), req.NamespacedName)
	}

	return reconcile.Result{RequeueAfter: requeue}, nil
}

// ── In-process index ──────────────────────────────────────────────────────────

// indexUpsert records that the MPA nn is interested in key.
// If the MPA previously tracked a different key (targetRef was changed), the
// old entry is cleaned up first.
func (r *MPAReconciler) indexUpsert(key string, nn types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove the MPA from whatever key it was previously under.
	for k, set := range r.mpaIndex {
		if _, ok := set[nn]; ok && k != key {
			delete(set, nn)
			if len(set) == 0 {
				delete(r.mpaIndex, k)
			}
			break
		}
	}

	// Add under the current key.
	if r.mpaIndex[key] == nil {
		r.mpaIndex[key] = make(map[types.NamespacedName]struct{})
	}
	r.mpaIndex[key][nn] = struct{}{}
}

// indexDelete removes the MPA nn from all entries in the index.
func (r *MPAReconciler) indexDelete(nn types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, set := range r.mpaIndex {
		delete(set, nn)
		if len(set) == 0 {
			delete(r.mpaIndex, key)
		}
	}
}

// mpasByTargetRef returns reconcile.Requests for every MPA that tracks the
// given targetRef key.  This is a pure in-process map lookup under a read
// lock — no client call, no API server round-trip.
func (r *MPAReconciler) mpasByTargetRef(key string) []reconcile.Request {
	r.mu.RLock()
	defer r.mu.RUnlock()

	set := r.mpaIndex[key]
	if len(set) == 0 {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(set))
	for nn := range set {
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}
	return requests
}

// SetupWithManager registers MPAReconciler with the Manager and declares the
// watches that trigger reconciliation:
//
//   - MultidimPodAutoscaler (owned type) — direct watch.
//   - HorizontalPodAutoscaler — on every event, look up the in-process index
//     to find owning MPAs (pure map lookup, zero client calls).
//   - VerticalPodAutoscaler  — same.
func (r *MPAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// mapToOwningMPAs is called on every HPA/VPA watch event.
	// It extracts the scaler's targetRef and consults the in-process index —
	// a pure map lookup that requires no client call at all.
	mapToOwningMPAs := func(_ context.Context, obj client.Object) []reconcile.Request {
		ref := scalerTargetRefFromObject(obj)
		if ref == nil {
			return nil
		}
		return r.mpasByTargetRef(targetRefKey(ref.APIVersion, ref.Kind, ref.Name))
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		// Primary watch: reconcile whenever an MPA changes.
		For(&mpav1alpha1.MultidimPodAutoscaler{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Secondary watch: re-enqueue owning MPAs when an HPA changes.
		Watches(&autoscalingv2.HorizontalPodAutoscaler{},
			handler.EnqueueRequestsFromMapFunc(mapToOwningMPAs)).
		// Secondary watch: re-enqueue owning MPAs when a VPA changes.
		Watches(&vpav1.VerticalPodAutoscaler{},
			handler.EnqueueRequestsFromMapFunc(mapToOwningMPAs)).
		Complete(r)
}

// scalerTargetRefFromObject extracts the workload target reference from an
// HPA or VPA object.  Returns nil for unrecognised types.
func scalerTargetRefFromObject(obj client.Object) *scalerRef {
	switch o := obj.(type) {
	case *autoscalingv2.HorizontalPodAutoscaler:
		ref := o.Spec.ScaleTargetRef
		return &scalerRef{APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name}
	case *vpav1.VerticalPodAutoscaler:
		if o.Spec.TargetRef == nil {
			return nil
		}
		return &scalerRef{
			APIVersion: o.Spec.TargetRef.APIVersion,
			Kind:       o.Spec.TargetRef.Kind,
			Name:       o.Spec.TargetRef.Name,
		}
	}
	return nil
}
