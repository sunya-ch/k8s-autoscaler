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

// Package e2e holds shared helpers for the MultidimPodAutoscaler e2e suite.
package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	mpaclientset "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpaclientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
)

const (
	// pollInterval is how often we re-check a condition.
	pollInterval = 5 * time.Second
	// pollTimeout is the maximum time we wait for a single condition.
	pollTimeout = 5 * time.Minute
	// testSuiteTimeout is the maximum total wall-clock time allowed for an
	// Ordered describe block (BeforeAll + all Its).  Tests that take longer
	// will have their context cancelled, causing pollers to return immediately
	// with an error rather than hanging.
	testSuiteTimeout = 15 * time.Minute

	// hamsterImage is the cpu/memory load generator used as the test workload.
	// The image's default ENTRYPOINT is /consumer, which listens on :8080 and
	// accepts ConsumeCPU / ConsumeMem HTTP commands. No Command/Args override is
	// needed — the VPA test suite relies on the same image the same way.
	hamsterImage = "registry.k8s.io/e2e-test-images/resource-consumer:1.14"
)

// clients is the set of API clients shared across all tests in the suite.
type clients struct {
	kube kubernetes.Interface
	mpa  mpaclientset.Interface
	vpa  vpaclientset.Interface
}

// newClients builds a clients instance from the suite-level kubeconfig flag.
func newClients() *clients {
	cfg, err := loadConfig(*flagKubeconfig)
	if err != nil {
		panic(fmt.Sprintf("loading kubeconfig: %v", err))
	}
	return &clients{
		kube: kubernetes.NewForConfigOrDie(cfg),
		mpa:  mpaclientset.NewForConfigOrDie(cfg),
		vpa:  vpaclientset.NewForConfigOrDie(cfg),
	}
}

// ── Workload helpers ──────────────────────────────────────────────────────────

// newHamsterDeployment returns a Deployment spec running the resource-consumer
// image with the given CPU/memory requests.
func newHamsterDeployment(namespace, name string, replicas int32, cpu, memory string) *appsv1.Deployment {
	cpuQ := resource.MustParse(cpu)
	memQ := resource.MustParse(memory)
	r := int32(replicas)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &r,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "hamster",
						Image: hamsterImage,
						// No Command/Args: the image's default ENTRYPOINT (/consumer)
						// already starts the HTTP load-generator on port 8080.
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    cpuQ,
								corev1.ResourceMemory: memQ,
							},
						},
					}},
				},
			},
		},
	}
}

// createDeploymentAndWait creates the deployment and blocks until all replicas are ready.
func createDeploymentAndWait(ctx context.Context, c *clients, d *appsv1.Deployment) {
	_, err := c.kube.AppsV1().Deployments(d.Namespace).Create(ctx, d, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "creating deployment %s", d.Name)

	ginkgo.By(fmt.Sprintf("waiting for deployment %s/%s to become ready", d.Namespace, d.Name))
	gomega.Eventually(func() bool {
		got, err := c.kube.AppsV1().Deployments(d.Namespace).Get(ctx, d.Name, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] deployment %s/%s: get error: %v\n", d.Namespace, d.Name, err)
			return false
		}
		fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] deployment %s/%s: ready=%d/%d\n",
			d.Namespace, d.Name, got.Status.ReadyReplicas, *d.Spec.Replicas)
		return got.Status.ReadyReplicas == *d.Spec.Replicas
	}, pollTimeout, pollInterval).Should(gomega.BeTrue(),
		"deployment %s/%s should become ready", d.Namespace, d.Name)
}

// ── VPA helpers ───────────────────────────────────────────────────────────────

// newVPA returns a VPA object targeting the given deployment.
func newVPA(namespace, name, deploymentName string, updateMode vpav1.UpdateMode, priority *int32) *vpav1.VerticalPodAutoscaler {
	return &vpav1.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: vpav1.VerticalPodAutoscalerSpec{
			TargetRef: &autoscalingv1.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			},
			UpdatePolicy: &vpav1.PodUpdatePolicy{
				UpdateMode: &updateMode,
			},
			Priority: priority,
		},
	}
}

// createVPA creates a VPA object and returns it.
func createVPA(ctx context.Context, c *clients, vpa *vpav1.VerticalPodAutoscaler) *vpav1.VerticalPodAutoscaler {
	got, err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(vpa.Namespace).Create(ctx, vpa, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "creating VPA %s", vpa.Name)
	return got
}

// simulateVPARecommendation patches the VPA status to make it look like the
// recommender has produced a recommendation.  It sets:
//   - status.recommendation with a minimal container entry so the field is non-nil.
//   - status.conditions with RecommendationProvided=True, which is what the MPA
//     controller inspects via vpaHasRecommendation.
//
// This avoids depending on a live metrics pipeline in e2e tests.
func simulateVPARecommendation(ctx context.Context, c *clients, namespace, name string) {
	ginkgo.By(fmt.Sprintf("simulating VPA recommendation on %s/%s", namespace, name))
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true,
		func(ctx context.Context) (bool, error) {
			v, err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: get error: %v\n", namespace, name, err)
				return false, nil
			}
			// Idempotency: already has the condition set.
			for _, cond := range v.Status.Conditions {
				if cond.Type == vpav1.RecommendationProvided && cond.Status == corev1.ConditionTrue {
					fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: RecommendationProvided already True\n", namespace, name)
					return true, nil
				}
			}
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: patching status with simulated recommendation\n", namespace, name)
			v.Status.Recommendation = &vpav1.RecommendedPodResources{
				ContainerRecommendations: []vpav1.RecommendedContainerResources{{
					ContainerName: "hamster",
					Target: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("100Mi"),
					},
				}},
			}
			v.Status.Conditions = append(v.Status.Conditions, vpav1.VerticalPodAutoscalerCondition{
				Type:               vpav1.RecommendationProvided,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "Simulated",
				Message:            "Simulated by e2e test",
			})
			_, err = c.vpa.AutoscalingV1().VerticalPodAutoscalers(namespace).UpdateStatus(ctx, v, metav1.UpdateOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: UpdateStatus error: %v\n", namespace, name, err)
			}
			return err == nil, nil
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"simulating VPA recommendation on %s/%s", namespace, name)
}

// isVPAPaused returns whether spec.paused is set to true on the named VPA.
func isVPAPaused(ctx context.Context, c *clients, namespace, name string) bool {
	v, err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return v.Spec.Paused != nil && *v.Spec.Paused
}

// ── HPA helpers ───────────────────────────────────────────────────────────────

// newHPA returns a CPU-based HPA targeting the given deployment.
func newHPA(namespace, name, deploymentName string, minReplicas, maxReplicas int32, cpuUtilization int32) *autoscalingv2.HorizontalPodAutoscaler {
	min := minReplicas
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			},
			MinReplicas: &min,
			MaxReplicas: maxReplicas,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: &cpuUtilization,
					},
				},
			}},
		},
	}
}

// createHPA creates an HPA object and returns it.
func createHPA(ctx context.Context, c *clients, hpa *autoscalingv2.HorizontalPodAutoscaler) *autoscalingv2.HorizontalPodAutoscaler {
	got, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(hpa.Namespace).Create(ctx, hpa, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "creating HPA %s", hpa.Name)
	return got
}

// simulateHPAScaling patches the HPA status to make it appear actively scaling
// (desiredReplicas != currentReplicas), which causes the MPA to hand priority to it.
func simulateHPAScaling(ctx context.Context, c *clients, namespace, name string, current, desired int32) {
	hpa, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	hpa.Status.CurrentReplicas = current
	hpa.Status.DesiredReplicas = desired
	_, err = c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).UpdateStatus(ctx, hpa, metav1.UpdateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"patching HPA %s/%s status to simulate scaling", namespace, name)
}

// simulateHPAStable pins the HPA spec so that the real HPA controller targets
// exactly replicas pods, then patches the status to current==desired.  Pinning
// spec.minReplicas (and maxReplicas when necessary) prevents the real HPA
// controller from computing a different desired count and overwriting the status
// patch before the MPA reconciler reads it.
func simulateHPAStable(ctx context.Context, c *clients, namespace, name string, replicas int32) {
	// First, pin spec so the real HPA controller is anchored to this replica
	// count.  The real controller will then compute desired==current==replicas
	// and will not overwrite our status patch.
	hpa, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	hpa.Spec.MinReplicas = &replicas
	hpa.Spec.MaxReplicas = replicas
	_, err = c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Update(ctx, hpa, metav1.UpdateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"pinning HPA %s/%s spec to %d replicas", namespace, name, replicas)

	// Now patch the status to reflect stable state.
	simulateHPAScaling(ctx, c, namespace, name, replicas, replicas)

	// Poll until the HPA status is stable (current==desired) — the real HPA
	// controller may briefly overwrite the status patch; retrying ensures the
	// MPA reconciler eventually observes a stable HPA.
	gomega.Eventually(func() bool {
		h, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		if h.Status.CurrentReplicas != h.Status.DesiredReplicas {
			// Re-apply the stable status patch to win the race.
			h.Status.CurrentReplicas = replicas
			h.Status.DesiredReplicas = replicas
			_, _ = c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).UpdateStatus(ctx, h, metav1.UpdateOptions{})
			return false
		}
		return true
	}, pollTimeout, pollInterval).Should(gomega.BeTrue(),
		"HPA %s/%s should show stable status (current==desired==%d) within %s",
		namespace, name, replicas, pollTimeout)
}

// ── MPA helpers ───────────────────────────────────────────────────────────────

// newMPA returns a MPA targeting the given deployment.
func newMPA(namespace, name, deploymentName string, stableSecs, tokenSecs int32) *mpav1alpha1.MultidimPodAutoscaler {
	return &mpav1alpha1.MultidimPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mpav1alpha1.MultidimPodAutoscalerSpec{
			TargetRef: autoscalingv1.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			},
			StableDurationSeconds:    &stableSecs,
			TokenHoldDurationSeconds: &tokenSecs,
		},
	}
}

// createMPA creates an MPA object and returns it.
func createMPA(ctx context.Context, c *clients, mpa *mpav1alpha1.MultidimPodAutoscaler) *mpav1alpha1.MultidimPodAutoscaler {
	got, err := c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(mpa.Namespace).Create(ctx, mpa, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "creating MPA %s", mpa.Name)
	return got
}

// waitForMPAActive polls until the MPA reports Active=True in its conditions.
func waitForMPAActive(ctx context.Context, c *clients, namespace, name string) {
	ginkgo.By(fmt.Sprintf("waiting for MPA %s/%s to become Active", namespace, name))
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true,
		func(ctx context.Context) (bool, error) {
			m, err := c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: get error: %v\n", namespace, name, err)
				return false, nil
			}
			for _, cond := range m.Status.Conditions {
				if cond.Type == mpav1alpha1.ConditionTypeActive && cond.Status == metav1.ConditionTrue {
					fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: Active=True ✓\n", namespace, name)
					return true, nil
				}
			}
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: not yet Active (conditions: %v)\n",
				namespace, name, m.Status.Conditions)
			return false, nil
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"MPA %s/%s should become Active within %s", namespace, name, pollTimeout)
}

// waitForMPAActiveAutoscaler polls until status.activeAutoscaler matches the
// expected scaler name and type.
func waitForMPAActiveAutoscaler(ctx context.Context, c *clients, namespace, name string,
	wantName string, wantType mpav1alpha1.ScalerType) {
	ginkgo.By(fmt.Sprintf("waiting for MPA %s/%s activeAutoscaler to be %s (%s)", namespace, name, wantName, wantType))
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true,
		func(ctx context.Context) (bool, error) {
			m, err := c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: get error: %v\n", namespace, name, err)
				return false, nil
			}
			if m.Status.ActiveAutoscaler == nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: activeAutoscaler=nil (want %s/%s)\n",
					namespace, name, wantName, wantType)
				return false, nil
			}
			got := m.Status.ActiveAutoscaler
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: activeAutoscaler=%s (%s) (want %s/%s)\n",
				namespace, name, got.Name, got.ScalerType, wantName, wantType)
			return got.Name == wantName && got.ScalerType == wantType, nil
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"MPA %s/%s activeAutoscaler should be %s (%s) within %s",
		namespace, name, wantName, wantType, pollTimeout)
}

// waitForMPANoActiveAutoscaler polls until status.activeAutoscaler is nil,
// meaning no scaler currently holds the token.
func waitForMPANoActiveAutoscaler(ctx context.Context, c *clients, namespace, name string) {
	ginkgo.By(fmt.Sprintf("waiting for MPA %s/%s activeAutoscaler to be cleared", namespace, name))
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true,
		func(ctx context.Context) (bool, error) {
			m, err := c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: get error: %v\n", namespace, name, err)
				return false, nil
			}
			if m.Status.ActiveAutoscaler != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: activeAutoscaler still=%s (%s)\n",
					namespace, name, m.Status.ActiveAutoscaler.Name, m.Status.ActiveAutoscaler.ScalerType)
				return false, nil
			}
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] MPA %s/%s: activeAutoscaler=nil ✓\n", namespace, name)
			return true, nil
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"MPA %s/%s activeAutoscaler should become nil within %s", namespace, name, pollTimeout)
}

// waitForVPAPausedState polls until the named VPA's spec.paused matches wantPaused.
func waitForVPAPausedState(ctx context.Context, c *clients, namespace, name string, wantPaused bool) {
	ginkgo.By(fmt.Sprintf("waiting for VPA %s/%s spec.paused=%v", namespace, name, wantPaused))
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true,
		func(ctx context.Context) (bool, error) {
			v, err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: get error: %v\n", namespace, name, err)
				return false, nil
			}
			got := v.Spec.Paused != nil && *v.Spec.Paused
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: spec.paused=%v (want %v)\n", namespace, name, got, wantPaused)
			return got == wantPaused, nil
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"VPA %s/%s spec.paused should be %v within %s", namespace, name, wantPaused, pollTimeout)
}

// waitForVPAAnnotation polls until the named VPA's AnnotationPausedByMPA equals wantValue.
// Pass "" for wantValue to wait for the annotation to be absent.
func waitForVPAAnnotation(ctx context.Context, c *clients, namespace, name, wantValue string) {
	wantDesc := wantValue
	if wantDesc == "" {
		wantDesc = "<absent>"
	}
	ginkgo.By(fmt.Sprintf("waiting for VPA %s/%s annotation %s=%s", namespace, name, mpav1alpha1.AnnotationPausedByMPA, wantDesc))
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true,
		func(ctx context.Context) (bool, error) {
			v, err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: get error: %v\n", namespace, name, err)
				return false, nil
			}
			got := v.Annotations[mpav1alpha1.AnnotationPausedByMPA]
			gotDesc := got
			if gotDesc == "" {
				gotDesc = "<absent>"
			}
			fmt.Fprintf(ginkgo.GinkgoWriter, "  [poll] VPA %s/%s: annotation %s=%s (want %s)\n",
				namespace, name, mpav1alpha1.AnnotationPausedByMPA, gotDesc, wantDesc)
			return got == wantValue, nil
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"VPA %s/%s annotation %q should be %q within %s",
		namespace, name, mpav1alpha1.AnnotationPausedByMPA, wantValue, pollTimeout)
}

// cleanupAll deletes all objects created by a test case. Errors are logged but
// not asserted — cleanup runs in AfterEach and must not mask test failures.
func cleanupAll(ctx context.Context, c *clients, namespace string,
	deploymentNames, vpaNames, hpaNames, mpaNames []string) {
	deleteOpts := metav1.DeleteOptions{}
	for _, n := range mpaNames {
		if n == "" {
			continue
		}
		if err := c.mpa.AutoscalingV1alpha1().MultidimPodAutoscalers(namespace).Delete(ctx, n, deleteOpts); err != nil {
			klog.InfoS("cleanup: delete MPA", "name", n, "err", err)
		}
	}
	for _, n := range vpaNames {
		if n == "" {
			continue
		}
		if err := c.vpa.AutoscalingV1().VerticalPodAutoscalers(namespace).Delete(ctx, n, deleteOpts); err != nil {
			klog.InfoS("cleanup: delete VPA", "name", n, "err", err)
		}
	}
	for _, n := range hpaNames {
		if n == "" {
			continue
		}
		if err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Delete(ctx, n, deleteOpts); err != nil {
			klog.InfoS("cleanup: delete HPA", "name", n, "err", err)
		}
	}
	for _, n := range deploymentNames {
		if n == "" {
			continue
		}
		if err := c.kube.AppsV1().Deployments(namespace).Delete(ctx, n, deleteOpts); err != nil {
			klog.InfoS("cleanup: delete Deployment", "name", n, "err", err)
		}
	}
}

// uniqueName returns a name unique enough for a single test run by appending
// a short suffix derived from the current Unix nanosecond timestamp.
func uniqueName(base string) string {
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano()%1_000_000)
}
