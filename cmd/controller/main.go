package main

import (
	"context"
	"flag"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
	"github.com/michelfeldheim/gateway-orchestrator/internal/controller"
	"github.com/michelfeldheim/gateway-orchestrator/internal/gateway"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gwapiv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var gatewayNamespace string
	var gatewayClassName string
	var httpPort int
	var httpsPort int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&gatewayNamespace, "gateway-namespace", "edge", "Namespace where Gateway resources are managed.")
	flag.StringVar(&gatewayClassName, "gateway-class", "aws-alb", "GatewayClass name to use for new Gateways.")
	flag.IntVar(&httpPort, "http-port", 80, "HTTP listener port for created Gateways.")
	flag.IntVar(&httpsPort, "https-port", 443, "HTTPS listener port for created Gateways.")

	opts := zap.Options{
		Development: true,
	}
	opts.StacktraceLevel = zapcore.FatalLevel
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Load AWS configuration
	awsCfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		setupLog.Error(err, "unable to load AWS config")
		os.Exit(1)
	}

	// Create AWS clients
	acmClient := aws.NewSDKACMClient(awsCfg)
	route53Client := aws.NewSDKRoute53Client(awsCfg)

	setupLog.Info("AWS clients initialized", "region", awsCfg.Region)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "gateway-orchestrator.opendi.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create Gateway pool
	gatewayPool := gateway.NewPool(mgr.GetClient(), gatewayNamespace, gatewayClassName, int32(httpPort), int32(httpsPort))

	// Setup GatewayHostnameRequest controller
	if err = (&controller.GatewayHostnameRequestReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		Recorder:      mgr.GetEventRecorderFor("gateway-orchestrator"),
		ACMClient:     acmClient,
		Route53Client: route53Client,
		GatewayPool:   gatewayPool,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GatewayHostnameRequest")
		os.Exit(1)
	}

	setupLog.Info("Controller registered",
		"gatewayNamespace", gatewayNamespace,
		"gatewayClassName", gatewayClassName,
		"httpPort", httpPort,
		"httpsPort", httpsPort)

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
