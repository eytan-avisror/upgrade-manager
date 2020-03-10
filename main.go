/*

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
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/keikoproj/aws-sdk-go-cache/cache"
	upgrademgrv1alpha1 "github.com/keikoproj/upgrade-manager/api/v1alpha1"
	"github.com/keikoproj/upgrade-manager/controllers"
	"github.com/keikoproj/upgrade-manager/pkg/log"
	"k8s.io/apimachinery/pkg/runtime"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

var (
	CacheDefaultTTL              time.Duration = time.Second * 0
	DescribeAutoScalingGroupsTTL time.Duration = 60 * time.Second
	CacheMaxItems                int64         = 5000
	CacheItemsToPrune            uint32        = 500
	cacheCfg                                   = cache.NewConfig(CacheDefaultTTL, CacheMaxItems, CacheItemsToPrune)
)

var DefaultRetryer = client.DefaultRetryer{
	NumMaxRetries:    250,
	MinThrottleDelay: time.Second * 5,
	MaxThrottleDelay: time.Second * 20,
	MinRetryDelay:    time.Second * 1,
	MaxRetryDelay:    time.Second * 5,
}

func init() {

	err := upgrademgrv1alpha1.AddToScheme(scheme)
	if err != nil {
		panic(err)
	}
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var namespace string
	var maxParallel int
	var region string
	var debugMode bool
	flag.BoolVar(&debugMode, "debug", false, "enable debug logging")
	flag.StringVar(&region, "region", "", "the AWS region to operate in")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&namespace, "namespace", "", "The namespace in which to watch objects")
	flag.IntVar(&maxParallel, "max-parallel", 10, "The max number of parallel rolling upgrades")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))

	mgo := ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		LeaderElection:     enableLeaderElection,
	}
	if namespace != "" {
		mgo.Namespace = namespace
		setupLog.Info("Watch RollingUpgrade objects only in namespace " + namespace)
	} else {
		setupLog.Info("Watch RollingUpgrade objects only in all namespaces")
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgo)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if region == "" {
		setupLog.Error(err, "--region flag not provided")
		os.Exit(1)
	}

	if debugMode {
		log.SetLevel("debug")
	}

	config := aws.NewConfig().WithRegion(region)
	config = config.WithCredentialsChainVerboseErrors(true)
	config = request.WithRetryer(config, log.NewRetryLogger(DefaultRetryer))
	sess, err := session.NewSession(config)
	if err != nil {
		log.Fatalf("failed to create asg client, %v", err)
	}

	cache.AddCaching(sess, cacheCfg)
	cacheCfg.SetCacheTTL("autoscaling", "DescribeAutoScalingGroups", DescribeAutoScalingGroupsTTL)
	sess.Handlers.Complete.PushFront(func(r *request.Request) {
		ctx := r.HTTPRequest.Context()
		log.Debugf("cache hit => %v, service => %s.%s",
			cache.IsCacheHit(ctx),
			r.ClientInfo.ServiceName,
			r.Operation.Name,
		)
	})

	reconciler := &controllers.RollingUpgradeReconciler{
		Client:       mgr.GetClient(),
		Log:          ctrl.Log.WithName("controllers").WithName("RollingUpgrade"),
		ClusterState: controllers.NewClusterState(),
		ASGClient:    autoscaling.New(sess),
		EC2Client:    ec2.New(sess),
	}

	reconciler.SetMaxParallel(maxParallel)

	err = (reconciler).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RollingUpgrade")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
