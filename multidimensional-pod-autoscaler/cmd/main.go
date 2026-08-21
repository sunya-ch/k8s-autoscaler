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

// main is the entry point for the MultidimPodAutoscaler controller binary.
//
// The controller is wired with controller-runtime which provides:
//   - Leader election (--leader-elect)
//   - Healthz / readyz HTTP endpoints (--health-probe-bind-address)
//   - Prometheus metrics endpoint (--metrics-bind-address)
//   - Graceful signal handling (SIGTERM / SIGINT)
package main

import (
	"flag"
	"os"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"k8s.io/autoscaler/multidimensional-pod-autoscaler/internal/controller"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	mpaclientset "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpaclientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
)

var scheme = runtime.NewScheme()

func init() {
	// Register all types the controller reads or writes via ctrl.Client.
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(autoscalingv2.AddToScheme(scheme))
	utilruntime.Must(mpav1alpha1.AddToScheme(scheme))
	utilruntime.Must(vpav1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr      string
		probeAddr        string
		leaderElect      bool
		leaderElectionID string
		workers          int
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"Address the Prometheus metrics endpoint binds to. Use :0 to disable.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"Address the health probe endpoint binds to.")
	flag.BoolVar(&leaderElect, "leader-elect", false,
		"Enable leader election for the controller. Recommended for HA deployments.")
	flag.StringVar(&leaderElectionID, "leader-election-id", "mpa-controller-leader",
		"Name of the Lease object used for leader election.")
	flag.IntVar(&workers, "workers", 2,
		"Number of concurrent reconcile workers.")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	klog.InitFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	// Build the controller-runtime Manager.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                leaderElect,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionResourceLock:    "leases",
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 durationPtr(30 * time.Second),
		RenewDeadline:                 durationPtr(20 * time.Second),
		RetryPeriod:                   durationPtr(8 * time.Second),
	})
	if err != nil {
		log.Error(err, "Unable to create Manager")
		os.Exit(1)
	}

	// Build typed clientsets for operations that go through the raw API
	// (VPA spec.paused patches and MPA status updates with retry).
	restCfg := mgr.GetConfig()

	mpaClient, err := mpaclientset.NewForConfig(restCfg)
	if err != nil {
		log.Error(err, "Unable to build MPA clientset")
		os.Exit(1)
	}
	vpaClient, err := vpaclientset.NewForConfig(restCfg)
	if err != nil {
		log.Error(err, "Unable to build VPA clientset")
		os.Exit(1)
	}

	// Register the MPA reconciler.
	reconciler := controller.NewMPAReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		nil, // kubeClient not needed; HPA reads go through ctrl.Client
		mpaClient,
		vpaClient,
	)
	if err := reconciler.SetupWithManager(mgr); err != nil {
		log.Error(err, "Unable to set up MPA controller")
		os.Exit(1)
	}

	// Health endpoints.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "Unable to set up healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "Unable to set up readyz check")
		os.Exit(1)
	}

	log.Info("Starting MultidimPodAutoscaler controller")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited with error")
		os.Exit(1)
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }
