/*
Copyright 2022 The Sonic_k8s Authors.

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

package main

import (
	"flag"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"
	_ "time/tzdata" // for AdvancedCronJob Time Zone support

	"github.com/sonic-net/sonic-k8s-operator/pkg/util/controllerfinder"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"k8s.io/kubernetes/pkg/capabilities"
	ctrl "sigs.k8s.io/controller-runtime"

	extclient "github.com/sonic-net/sonic-k8s-operator/pkg/client"
	utilclient "github.com/sonic-net/sonic-k8s-operator/pkg/util/client"
	"github.com/sonic-net/sonic-k8s-operator/pkg/util/fieldindex"

	"github.com/sonic-net/sonic-k8s-operator/pkg/controller"
	_ "github.com/sonic-net/sonic-k8s-operator/pkg/util/metrics/leadership"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	restConfigQPS   = flag.Int("rest-config-qps", 30, "QPS of rest config.")
	restConfigBurst = flag.Int("rest-config-burst", 50, "Burst of rest config.")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	scheme.AddUnversionedTypes(metav1.SchemeGroupVersion, &metav1.UpdateOptions{}, &metav1.DeleteOptions{}, &metav1.CreateOptions{})
	// +kubebuilder:scaffold:scheme
}

func main() {
	flag.Set("stderrthreshold", "10")
	var metricsAddr, pprofAddr string
	var healthProbeAddr string
	var enableLeaderElection, enablePprof, allowPrivileged bool
	var leaderElectionNamespace string
	var namespace string
	var syncPeriodStr string
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-addr", ":8000", "The address the healthz/readyz endpoint binds to.")
	flag.BoolVar(&allowPrivileged, "allow-privileged", true, "If true, allow privileged containers. It will only work if api-server is also"+
		"started with --allow-privileged=true.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", true, "Whether you need to enable leader election.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "sonic-k8s-system",
		"This determines the namespace in which the leader election configmap will be created, it will use in-cluster namespace if empty.")
	flag.StringVar(&namespace, "namespace", "",
		"Namespace if specified restricts the manager's cache to watch objects in the desired namespace. Defaults to all namespaces.")
	flag.BoolVar(&enablePprof, "enable-pprof", true, "Enable pprof for controller manager.")
	flag.StringVar(&pprofAddr, "pprof-addr", ":8090", "The address the pprof binds to.")
	flag.StringVar(&syncPeriodStr, "sync-period", "", "Determines the minimum frequency at which watched resources are reconciled.")

	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	rand.Seed(time.Now().UnixNano())
	ctrl.SetLogger(klogr.New())

	if enablePprof {
		go func() {
			if err := http.ListenAndServe(pprofAddr, nil); err != nil {
				setupLog.Error(err, "unable to start pprof")
			}
		}()
	}

	if allowPrivileged {
		capabilities.Initialize(capabilities.Capabilities{
			AllowPrivileged: allowPrivileged,
		})
	}

	ctx := ctrl.SetupSignalHandler()
	cfg := ctrl.GetConfigOrDie()
	setRestConfig(cfg)
	cfg.UserAgent = "sonic-k8s-operator"

	setupLog.Info("new clientset registry")
	err := extclient.NewRegistry(cfg)
	if err != nil {
		setupLog.Error(err, "unable to init clientset and informer")
		os.Exit(1)
	}

	var syncPeriod *time.Duration
	if syncPeriodStr != "" {
		d, err := time.ParseDuration(syncPeriodStr)
		if err != nil {
			setupLog.Error(err, "invalid sync period flag")
		} else {
			syncPeriod = &d
		}
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                     scheme,
		MetricsBindAddress:         metricsAddr,
		HealthProbeBindAddress:     healthProbeAddr,
		LeaderElection:             enableLeaderElection,
		LeaderElectionID:           "sonic-deamonset-manager",
		LeaderElectionNamespace:    leaderElectionNamespace,
		LeaderElectionResourceLock: resourcelock.ConfigMapsResourceLock,
		Namespace:                  namespace,
		SyncPeriod:                 syncPeriod,
		NewCache:                   utilclient.NewCache,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("default", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to create health check")
		os.Exit(1)
	}

	err = controllerfinder.InitControllerFinder(mgr)
	if err != nil {
		setupLog.Error(err, "unable to start ControllerFinder")
		os.Exit(1)
	}

	setupLog.Info("register field index")
	if err := fieldindex.RegisterFieldIndexes(mgr.GetCache()); err != nil {
		setupLog.Error(err, "failed to register field index")
		os.Exit(1)
	}

	go func() {
		setupLog.Info("setup controllers")
		if err = controller.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to setup controllers")
			os.Exit(1)
		}
	}()

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setRestConfig(c *rest.Config) {
	if *restConfigQPS > 0 {
		c.QPS = float32(*restConfigQPS)
	}
	if *restConfigBurst > 0 {
		c.Burst = *restConfigBurst
	}
}
