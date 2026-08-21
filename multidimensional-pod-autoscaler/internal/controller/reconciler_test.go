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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v2"
	"k8s.io/client-go/rest"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	mpaclientset "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	mpatyped "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned/typed/autoscaling.k8s.io/v1alpha1"
	mpalisters "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1alpha1"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpaclientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	vpafake "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/fake"
	vpalisters "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1"
)

func TestReconciler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reconciler Suite")
}

// ── In-package lister fakes ───────────────────────────────────────────────────

// fakeMPALister is an in-memory MultidimPodAutoscalerLister.
type fakeMPALister struct {
	items []*mpav1alpha1.MultidimPodAutoscaler
}

func (f *fakeMPALister) List(_ labels.Selector) ([]*mpav1alpha1.MultidimPodAutoscaler, error) {
	return f.items, nil
}
func (f *fakeMPALister) MultidimPodAutoscalers(ns string) mpalisters.MultidimPodAutoscalerNamespaceLister {
	return &fakeMPANamespaceLister{ns: ns, all: f.items}
}

type fakeMPANamespaceLister struct {
	ns  string
	all []*mpav1alpha1.MultidimPodAutoscaler
}

func (f *fakeMPANamespaceLister) List(_ labels.Selector) ([]*mpav1alpha1.MultidimPodAutoscaler, error) {
	var out []*mpav1alpha1.MultidimPodAutoscaler
	for _, m := range f.all {
		if m.Namespace == f.ns {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeMPANamespaceLister) Get(name string) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	for _, m := range f.all {
		if m.Namespace == f.ns && m.Name == name {
			return m, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "multidimpodautoscalers"}, name)
}

// fakeHPALister is an in-memory HorizontalPodAutoscalerLister.
type fakeHPALister struct {
	items []*autoscalingv2.HorizontalPodAutoscaler
}

func (f *fakeHPALister) List(_ labels.Selector) ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return f.items, nil
}
func (f *fakeHPALister) HorizontalPodAutoscalers(ns string) autoscalinglisters.HorizontalPodAutoscalerNamespaceLister {
	return &fakeHPANamespaceLister{ns: ns, all: f.items}
}

type fakeHPANamespaceLister struct {
	ns  string
	all []*autoscalingv2.HorizontalPodAutoscaler
}

func (f *fakeHPANamespaceLister) List(_ labels.Selector) ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	var out []*autoscalingv2.HorizontalPodAutoscaler
	for _, h := range f.all {
		if h.Namespace == f.ns {
			out = append(out, h)
		}
	}
	return out, nil
}
func (f *fakeHPANamespaceLister) Get(name string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	for _, h := range f.all {
		if h.Namespace == f.ns && h.Name == name {
			return h, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "horizontalpodautoscalers"}, name)
}

// fakeVPALister is an in-memory VerticalPodAutoscalerLister.
type fakeVPALister struct {
	items []*vpav1.VerticalPodAutoscaler
}

func (f *fakeVPALister) List(_ labels.Selector) ([]*vpav1.VerticalPodAutoscaler, error) {
	return f.items, nil
}
func (f *fakeVPALister) VerticalPodAutoscalers(ns string) vpalisters.VerticalPodAutoscalerNamespaceLister {
	return &fakeVPANamespaceLister{ns: ns, all: f.items}
}

type fakeVPANamespaceLister struct {
	ns  string
	all []*vpav1.VerticalPodAutoscaler
}

func (f *fakeVPANamespaceLister) List(_ labels.Selector) ([]*vpav1.VerticalPodAutoscaler, error) {
	var out []*vpav1.VerticalPodAutoscaler
	for _, v := range f.all {
		if v.Namespace == f.ns {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *fakeVPANamespaceLister) Get(name string) (*vpav1.VerticalPodAutoscaler, error) {
	for _, v := range f.all {
		if v.Namespace == f.ns && v.Name == name {
			return v, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "verticalpodautoscalers"}, name)
}

// ── fakeMPAClient ─────────────────────────────────────────────────────────────
//
// Implements mpaclientset.Interface and records Patch / UpdateStatus calls so
// tests can assert on them without a real API server.

type fakePatchCall struct {
	namespace string
	name      string
	data      []byte
}

type fakeMPAClient struct {
	patchCalls        []fakePatchCall
	updateStatusCalls []*mpav1alpha1.MultidimPodAutoscaler
	// lister is shared with the Reconciler so Get() inside UpdateStatus returns
	// the same object the lister already knows about.
	lister *fakeMPALister
}

var _ mpaclientset.Interface = (*fakeMPAClient)(nil)

func (c *fakeMPAClient) Discovery() discovery.DiscoveryInterface { return nil }
func (c *fakeMPAClient) AutoscalingV1alpha1() mpatyped.AutoscalingV1alpha1Interface {
	return &fakeMPAV1alpha1Client{parent: c}
}

type fakeMPAV1alpha1Client struct{ parent *fakeMPAClient }

func (c *fakeMPAV1alpha1Client) RESTClient() rest.Interface { return nil }
func (c *fakeMPAV1alpha1Client) MultidimPodAutoscalers(ns string) mpatyped.MultidimPodAutoscalerInterface {
	return &fakeMPAInterface{ns: ns, parent: c.parent}
}

type fakeMPAInterface struct {
	ns     string
	parent *fakeMPAClient
}

func (f *fakeMPAInterface) Create(_ context.Context, m *mpav1alpha1.MultidimPodAutoscaler, _ metav1.CreateOptions) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	return m, nil
}
func (f *fakeMPAInterface) Update(_ context.Context, m *mpav1alpha1.MultidimPodAutoscaler, _ metav1.UpdateOptions) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	return m, nil
}
func (f *fakeMPAInterface) UpdateStatus(_ context.Context, m *mpav1alpha1.MultidimPodAutoscaler, _ metav1.UpdateOptions) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	f.parent.updateStatusCalls = append(f.parent.updateStatusCalls, m.DeepCopy())
	return m, nil
}
func (f *fakeMPAInterface) Delete(_ context.Context, _ string, _ metav1.DeleteOptions) error {
	return nil
}
func (f *fakeMPAInterface) DeleteCollection(_ context.Context, _ metav1.DeleteOptions, _ metav1.ListOptions) error {
	return nil
}
func (f *fakeMPAInterface) Get(_ context.Context, name string, _ metav1.GetOptions) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	return f.parent.lister.MultidimPodAutoscalers(f.ns).Get(name)
}
func (f *fakeMPAInterface) List(_ context.Context, _ metav1.ListOptions) (*mpav1alpha1.MultidimPodAutoscalerList, error) {
	items, _ := f.parent.lister.MultidimPodAutoscalers(f.ns).List(labels.Everything())
	list := &mpav1alpha1.MultidimPodAutoscalerList{}
	for _, m := range items {
		list.Items = append(list.Items, *m)
	}
	return list, nil
}
func (f *fakeMPAInterface) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	return watch.NewFake(), nil
}
func (f *fakeMPAInterface) Patch(_ context.Context, name string, _ types.PatchType, data []byte, _ metav1.PatchOptions, _ ...string) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	f.parent.patchCalls = append(f.parent.patchCalls, fakePatchCall{namespace: f.ns, name: name, data: data})
	obj, _ := f.parent.lister.MultidimPodAutoscalers(f.ns).Get(name)
	if obj == nil {
		return &mpav1alpha1.MultidimPodAutoscaler{}, nil
	}
	return obj, nil
}

// ── Test helpers ──────────────────────────────────────────────────────────────

const testNS = "default"

func targetRef() autoscalingv1.CrossVersionObjectReference {
	return autoscalingv1.CrossVersionObjectReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "my-app",
	}
}

func newMPA(name string, opts ...func(*mpav1alpha1.MultidimPodAutoscaler)) *mpav1alpha1.MultidimPodAutoscaler {
	m := &mpav1alpha1.MultidimPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNS,
			CreationTimestamp: metav1.Now(),
			Finalizers:        []string{mpaFinalizer},
		},
		Spec: mpav1alpha1.MultidimPodAutoscalerSpec{TargetRef: targetRef()},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func withDeletionTimestamp(m *mpav1alpha1.MultidimPodAutoscaler) {
	now := metav1.Now()
	m.DeletionTimestamp = &now
}

func withNoFinalizer(m *mpav1alpha1.MultidimPodAutoscaler) {
	m.Finalizers = nil
}

func withCreationTime(t time.Time) func(*mpav1alpha1.MultidimPodAutoscaler) {
	return func(m *mpav1alpha1.MultidimPodAutoscaler) {
		m.CreationTimestamp = metav1.NewTime(t)
	}
}

func newHPA(name string, current, desired int32) *autoscalingv2.HorizontalPodAutoscaler {
	ref := targetRef()
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: ref.APIVersion,
				Kind:       ref.Kind,
				Name:       ref.Name,
			},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: current,
			DesiredReplicas: desired,
		},
	}
}

func newVPA(name string) *vpav1.VerticalPodAutoscaler {
	ref := targetRef()
	return &vpav1.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: vpav1.VerticalPodAutoscalerSpec{
			TargetRef: &autoscalingv1.CrossVersionObjectReference{
				APIVersion: ref.APIVersion,
				Kind:       ref.Kind,
				Name:       ref.Name,
			},
		},
		// Mark the VPA as ready so pickBestVPA includes it in selection.
		// In a real cluster this condition is set by the VPA recommender once
		// it has produced at least one recommendation.
		Status: vpav1.VerticalPodAutoscalerStatus{
			Conditions: []vpav1.VerticalPodAutoscalerCondition{
				{
					Type:   vpav1.RecommendationProvided,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

// newReconcilerUnderTest wires up a Reconciler with in-memory fakes and a
// fixed clock so tests are deterministic.
func newReconcilerUnderTest(
	mpas []*mpav1alpha1.MultidimPodAutoscaler,
	hpas []*autoscalingv2.HorizontalPodAutoscaler,
	vpas []*vpav1.VerticalPodAutoscaler,
	mpaClient *fakeMPAClient,
	vpaClient vpaclientset.Interface,
	fixedNow time.Time,
) *Reconciler {
	mpaLister := &fakeMPALister{items: mpas}
	mpaClient.lister = mpaLister
	r := newReconciler(
		nil, // kubeClient – not exercised by Reconciler logic
		mpaClient,
		vpaClient,
		mpaLister,
		&fakeHPALister{items: hpas},
		&fakeVPALister{items: vpas},
	)
	r.clock = func() time.Time { return fixedNow }
	return r
}

// findCondition returns the first condition of the given type from status, or nil.
func findCondition(status mpav1alpha1.MultidimPodAutoscalerStatus, condType string) *metav1.Condition {
	for i := range status.Conditions {
		if status.Conditions[i].Type == condType {
			return &status.Conditions[i]
		}
	}
	return nil
}

// ── Specs ─────────────────────────────────────────────────────────────────────

var _ = Describe("Reconciler", func() {
	var (
		ctx      = context.Background()
		fixedNow = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	)

	// ── MPA not found ─────────────────────────────────────────────────────────
	Describe("MPA not found", func() {
		It("returns nil without touching any client", func() {
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest(nil, nil, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

			_, err := r.Reconcile(ctx, testNS, "nonexistent")
			Expect(err).NotTo(HaveOccurred())
			Expect(mpaClient.patchCalls).To(BeEmpty())
			Expect(mpaClient.updateStatusCalls).To(BeEmpty())
		})
	})

	// ── Finalizer ─────────────────────────────────────────────────────────────
	Describe("ensureFinalizer", func() {
		DescribeTable("adds the finalizer when it is absent",
			func(existingFinalizers []string, expectFinalizerPatch bool) {
				mpa := newMPA("mpa-1")
				mpa.Finalizers = existingFinalizers
				mpaClient := &fakeMPAClient{}
				// No HPAs/VPAs so the reconcile proceeds to finalizer check then stops at "no scalers".
				r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, nil, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

				_, _ = r.Reconcile(ctx, testNS, "mpa-1")

				finalizerPatched := false
				for _, p := range mpaClient.patchCalls {
					if p.name == "mpa-1" && containsString(string(p.data), mpaFinalizer) {
						finalizerPatched = true
					}
				}
				Expect(finalizerPatched).To(Equal(expectFinalizerPatch))
			},
			Entry("no finalizers → patch adds finalizer",
				[]string{}, true),
			Entry("finalizer already present → no finalizer patch",
				[]string{mpaFinalizer}, false),
		)
	})

	// ── Deletion handling ─────────────────────────────────────────────────────
	Describe("handleDeletion", func() {
		DescribeTable("finalizer and VPA unpause behaviour on deletion",
			func(hasFinalizer bool, vpasForLister []*vpav1.VerticalPodAutoscaler, expectFinalizerPatch bool) {
				mpa := newMPA("mpa-1")
				withDeletionTimestamp(mpa)
				if !hasFinalizer {
					withNoFinalizer(mpa)
				}
				// Mark VPAs as owned by this MPA so handleDeletion unpauses them.
				for _, v := range vpasForLister {
					if v.Annotations == nil {
						v.Annotations = map[string]string{}
					}
					v.Annotations[mpav1alpha1.AnnotationPausedByMPA] = testNS + "/mpa-1"
					paused := true
					v.Spec.Paused = &paused
				}

				mpaClient := &fakeMPAClient{}
				vpaFakeClient := vpafake.NewSimpleClientset()
				r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, nil, vpasForLister, mpaClient, vpaFakeClient, fixedNow)

				_, err := r.Reconcile(ctx, testNS, "mpa-1")
				Expect(err).NotTo(HaveOccurred())

				if expectFinalizerPatch {
					found := false
					for _, p := range mpaClient.patchCalls {
						if p.name == "mpa-1" {
							found = true
						}
					}
					Expect(found).To(BeTrue(), "expected a patch call to remove finalizer")
				} else {
					Expect(mpaClient.patchCalls).To(BeEmpty())
				}
			},
			Entry("has finalizer, no owned VPAs → removes finalizer",
				true, []*vpav1.VerticalPodAutoscaler{}, true),
			Entry("has finalizer, owned VPA → removes finalizer (VPA unpause attempted via vpaClient)",
				true, []*vpav1.VerticalPodAutoscaler{newVPA("vpa-1")}, true),
			Entry("no finalizer → no-op, no patch",
				false, []*vpav1.VerticalPodAutoscaler{}, false),
		)
	})

	// ── Conflict detection ────────────────────────────────────────────────────
	Describe("conflict detection", func() {
		DescribeTable("stamps Conflicted=True on the losing MPA",
			func(winnerName, loserName string, sameTimestamp bool) {
				older := fixedNow.Add(-1 * time.Minute)
				winner := newMPA(winnerName, withCreationTime(older))
				var loserTime time.Time
				if sameTimestamp {
					loserTime = older // same time → name order decides
				} else {
					loserTime = fixedNow // winner is strictly older
				}
				loser := newMPA(loserName, withCreationTime(loserTime))

				mpaClient := &fakeMPAClient{}
				r := newReconcilerUnderTest(
					[]*mpav1alpha1.MultidimPodAutoscaler{winner, loser},
					nil, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow,
				)

				_, err := r.Reconcile(ctx, testNS, loserName)
				Expect(err).NotTo(HaveOccurred())

				Expect(mpaClient.updateStatusCalls).NotTo(BeEmpty())
				cond := findCondition(mpaClient.updateStatusCalls[0].Status, mpav1alpha1.ConditionTypeConflicted)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				Expect(cond.Reason).To(Equal("TargetRefConflict"))
			},
			Entry("winner is strictly older",
				"mpa-alpha", "mpa-beta", false),
			Entry("same creation timestamp, winner has lexicographically smaller name",
				"mpa-alpha", "mpa-beta", true),
		)

		It("does not set Conflicted when only one MPA targets the workload", func() {
			mpa := newMPA("mpa-1")
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, nil, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

			_, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())

			for _, s := range mpaClient.updateStatusCalls {
				cond := findCondition(s.Status, mpav1alpha1.ConditionTypeConflicted)
				Expect(cond).To(BeNil(), "unexpected Conflicted condition")
			}
		})
	})

	// ── No scalers found ──────────────────────────────────────────────────────
	Describe("no scalers found", func() {
		It("sets Active=False with NoScalersFound reason and no active autoscaler", func() {
			mpa := newMPA("mpa-1")
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, nil, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

			_, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())

			Expect(mpaClient.updateStatusCalls).NotTo(BeEmpty())
			status := mpaClient.updateStatusCalls[0].Status
			Expect(status.ActiveAutoscaler).To(BeNil())
			cond := findCondition(status, mpav1alpha1.ConditionTypeActive)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("NoScalersFound"))
		})
	})

	// ── Active condition after successful scaler coordination ─────────────────
	Describe("Active=True after successful coordination", func() {
		DescribeTable("sets Active=True with Coordinating reason",
			func(hpas []*autoscalingv2.HorizontalPodAutoscaler, vpas []*vpav1.VerticalPodAutoscaler, expectActiveScalerType mpav1alpha1.ScalerType) {
				mpa := newMPA("mpa-1")
				// Push LastTransitionTime far enough in the past so the stable window has elapsed.
				longAgo := metav1.NewTime(fixedNow.Add(-5 * time.Minute))
				mpa.Status.LastTransitionTime = &longAgo

				mpaClient := &fakeMPAClient{}
				r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, hpas, vpas, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

				_, err := r.Reconcile(ctx, testNS, "mpa-1")
				Expect(err).NotTo(HaveOccurred())

				Expect(mpaClient.updateStatusCalls).NotTo(BeEmpty())
				status := mpaClient.updateStatusCalls[0].Status
				cond := findCondition(status, mpav1alpha1.ConditionTypeActive)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				Expect(cond.Reason).To(Equal("Coordinating"))
				Expect(status.ActiveAutoscaler).NotTo(BeNil())
				Expect(status.ActiveAutoscaler.ScalerType).To(Equal(expectActiveScalerType))
			},
			Entry("scaling HPA present → HPA is active",
				[]*autoscalingv2.HorizontalPodAutoscaler{newHPA("hpa-1", 1, 3)},
				nil,
				mpav1alpha1.HorizontalPodAutoscalerScalerType),
			Entry("stable VPA only, stable window elapsed → VPA is active",
				nil,
				[]*vpav1.VerticalPodAutoscaler{newVPA("vpa-1")},
				mpav1alpha1.VerticalPodAutoscalerScalerType),
		)
	})

	// ── Conflicted condition is cleared when winner reconciles ────────────────
	Describe("Conflicted condition cleared on winning MPA", func() {
		It("does not set Conflicted on the MPA that wins the tiebreak", func() {
			winner := newMPA("mpa-alpha", withCreationTime(fixedNow.Add(-2*time.Minute)))
			loser := newMPA("mpa-beta", withCreationTime(fixedNow.Add(-1*time.Minute)))

			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest(
				[]*mpav1alpha1.MultidimPodAutoscaler{winner, loser},
				nil, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow,
			)

			_, err := r.Reconcile(ctx, testNS, "mpa-alpha")
			Expect(err).NotTo(HaveOccurred())

			for _, s := range mpaClient.updateStatusCalls {
				cond := findCondition(s.Status, mpav1alpha1.ConditionTypeConflicted)
				Expect(cond).To(BeNil(), "winner must not have Conflicted condition")
			}
		})
	})

	// ── VPA pause enforcement ─────────────────────────────────────────────────
	Describe("VPA pause enforcement", func() {
		DescribeTable("pauses non-active VPAs and leaves the active VPA unpaused",
			func(activeVPAName string, vpaNames []string, expectVPAPatchCount int) {
				mpa := newMPA("mpa-1")
				// Token expired (>600s) so Decide will re-confirm the active VPA and
				// EnforceVPAPauseState will be called, which is the code path under test.
				longAgo := metav1.NewTime(fixedNow.Add(-20 * time.Minute))
				mpa.Status.ActiveAutoscaler = &mpav1alpha1.AutoScalerRef{
					Name:       activeVPAName,
					ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
				}
				mpa.Status.LastTransitionTime = &longAgo
	
				var vpas []*vpav1.VerticalPodAutoscaler
				for _, n := range vpaNames {
					vpas = append(vpas, newVPA(n))
				}
				// No prior AutoScalers list so autoScalersChanged will always fire.
	
				// Pre-seed VPA objects into the fake clientset so Patch can find them.
				vpaObjects := make([]runtime.Object, len(vpas))
				for i, v := range vpas {
					vpaObjects[i] = v
				}
				vpaFakeClient := vpafake.NewSimpleClientset(vpaObjects...)
				mpaClient := &fakeMPAClient{}
				r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, nil, vpas, mpaClient, vpaFakeClient, fixedNow)

				_, err := r.Reconcile(ctx, testNS, "mpa-1")
				Expect(err).NotTo(HaveOccurred())

				// Count Patch calls on the VPA fake tracker.
				actions := vpaFakeClient.Actions()
				patchCount := 0
				for _, a := range actions {
					if a.GetVerb() == "patch" {
						patchCount++
					}
				}
				Expect(patchCount).To(Equal(expectVPAPatchCount))
			},
			Entry("single VPA and it is active → no pause patches",
				"vpa-1", []string{"vpa-1"}, 0),
			Entry("active vpa-1 plus unowned vpa-2 → vpa-2 gets a pause patch",
				"vpa-1", []string{"vpa-1", "vpa-2"}, 1),
			Entry("active vpa-1 plus two unowned VPAs → both get pause patches",
				"vpa-1", []string{"vpa-1", "vpa-2", "vpa-3"}, 2),
		)
	})

	// ── desiredReplicas=0 and missing refs are safe ───────────────────────────
	Describe("desiredReplicas=0 and missing HPA/VPA refs", func() {
		It("treats desiredReplicas=0 as stable and does not pause any VPA", func() {
			// An HPA that the Kubernetes controller has not yet evaluated has
			// currentReplicas=0 and desiredReplicas=0. hpaIsScaling returns false
			// (0==0), so MPA must treat this as a stable HPA and must not pause
			// the co-targeting VPA.
			hpa := newHPA("hpa-1", 0, 0)
			vpaObj := newVPA("vpa-1")
			vpaObjects := []runtime.Object{vpaObj}
			vpaFakeClient := vpafake.NewSimpleClientset(vpaObjects...)

			mpa := newMPA("mpa-1")
			longAgo := metav1.NewTime(fixedNow.Add(-5 * time.Minute))
			mpa.Status.LastTransitionTime = &longAgo

			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest(
				[]*mpav1alpha1.MultidimPodAutoscaler{mpa},
				[]*autoscalingv2.HorizontalPodAutoscaler{hpa},
				[]*vpav1.VerticalPodAutoscaler{vpaObj},
				mpaClient, vpaFakeClient, fixedNow,
			)

			_, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())

			// No VPA patch should have been issued.
			for _, a := range vpaFakeClient.Actions() {
				Expect(a.GetVerb()).NotTo(Equal("patch"),
					"unexpected VPA patch when HPA has desiredReplicas=0")
			}
		})

		It("does not touch VPAs that target a different workload", func() {
			// The MPA targets "my-app"; the VPA in the same namespace targets
			// "other-app". The reconcile loop must not issue any patch on it.
			otherRef := autoscalingv1.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "other-app",
			}
			unrelatedVPA := &vpav1.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: "vpa-other", Namespace: testNS},
				Spec: vpav1.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: otherRef.APIVersion,
						Kind:       otherRef.Kind,
						Name:       otherRef.Name,
					},
				},
			}
			vpaFakeClient := vpafake.NewSimpleClientset(unrelatedVPA)

			// MPA has no matching HPAs or VPAs → "no scalers found" path.
			mpa := newMPA("mpa-1")
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest(
				[]*mpav1alpha1.MultidimPodAutoscaler{mpa},
				nil,
				[]*vpav1.VerticalPodAutoscaler{unrelatedVPA},
				mpaClient, vpaFakeClient, fixedNow,
			)

			_, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())

			// The unrelated VPA must never have been patched.
			for _, a := range vpaFakeClient.Actions() {
				Expect(a.GetVerb()).NotTo(Equal("patch"),
					"unexpected patch on VPA targeting a different workload")
			}
		})
	})

	// ── Timed requeue ─────────────────────────────────────────────────────────
	Describe("timed requeue after state transitions", func() {
		It("requeues after the remaining stableDuration when activeAutoscaler becomes nil", func() {
			// Simulate the HPA stabilising: the MPA's active autoscaler was an HPA
			// that is now stable.  Decide will clear activeAutoscaler and set
			// LastTransitionTime = now.  The controller should requeue after the
			// stableDuration (10 s) because no watch event will fire for
			// the stable-window countdown.
			stableSec := int32(10)
			mpa := newMPA("mpa-1")
			mpa.Spec.StableDurationSeconds = &stableSec
			mpa.Status.ActiveAutoscaler = &mpav1alpha1.AutoScalerRef{
				Name:       "hpa-1",
				ScalerType: mpav1alpha1.HorizontalPodAutoscalerScalerType,
				InProgress: true,
			}
			longAgo := metav1.NewTime(fixedNow.Add(-5 * time.Minute))
			mpa.Status.LastTransitionTime = &longAgo

			// HPA is now stable (current == desired) so Decide clears activeAutoscaler.
			hpa := newHPA("hpa-1", 2, 2)
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, []*autoscalingv2.HorizontalPodAutoscaler{hpa}, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

			requeue, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())
			// activeAutoscaler is now nil, lastTransitionTime = fixedNow.
			// Remaining stable window = 10 s − 0 s elapsed ≈ 10 s.
			Expect(requeue).To(BeNumerically("~", 10*time.Second, time.Second),
				"should requeue after stableDurationSeconds")
		})

		It("requeues after the remaining token hold duration when VPA is active and token unexpired", func() {
			// VPA was activated tokenHoldDuration/2 ago — token has half life left.
			tokenSec := int32(20)
			halfToken := time.Duration(tokenSec/2) * time.Second
			mpa := newMPA("mpa-1")
			mpa.Spec.TokenHoldDurationSeconds = &tokenSec
			activatedAt := metav1.NewTime(fixedNow.Add(-halfToken))
			mpa.Status.ActiveAutoscaler = &mpav1alpha1.AutoScalerRef{
				Name:       "vpa-1",
				ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType,
			}
			mpa.Status.LastTransitionTime = &activatedAt

			vpaObj := newVPA("vpa-1")
			mpa.Status.AutoScalers = []mpav1alpha1.AutoScalerRef{
				{Name: "vpa-1", ScalerType: mpav1alpha1.VerticalPodAutoscalerScalerType, InProgress: true},
			}

			vpaFakeClient := vpafake.NewSimpleClientset(vpaObj)
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest(
				[]*mpav1alpha1.MultidimPodAutoscaler{mpa},
				nil,
				[]*vpav1.VerticalPodAutoscaler{vpaObj},
				mpaClient, vpaFakeClient, fixedNow,
			)

			requeue, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())
			// Token expires at activatedAt + 20 s; elapsed = 10 s; remaining ≈ 10 s.
			Expect(requeue).To(BeNumerically("~", halfToken, time.Second),
				"should requeue when the token expires")
		})

		It("returns zero requeue when the HPA is actively scaling", func() {
			// While an HPA is scaling there is no pending deadline — the next
			// watch event on the HPA is sufficient.
			mpa := newMPA("mpa-1")
			longAgo := metav1.NewTime(fixedNow.Add(-5 * time.Minute))
			mpa.Status.LastTransitionTime = &longAgo

			hpa := newHPA("hpa-1", 1, 3) // currently scaling
			mpaClient := &fakeMPAClient{}
			r := newReconcilerUnderTest([]*mpav1alpha1.MultidimPodAutoscaler{mpa}, []*autoscalingv2.HorizontalPodAutoscaler{hpa}, nil, mpaClient, vpafake.NewSimpleClientset(), fixedNow)

			requeue, err := r.Reconcile(ctx, testNS, "mpa-1")
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).To(BeZero(), "no timed requeue needed while HPA is scaling")
		})
	})
})

// containsString is a small helper used in finalizer-patch assertions.
func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
