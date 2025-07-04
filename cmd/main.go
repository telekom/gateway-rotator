// SPDX-FileCopyrightText: 2025 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package main //nolint:cyclop // high complexity because of setup

import (
	"crypto/tls"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"gw.ei.telekom.de/rotator/internal/controller"
	// +kubebuilder:scaffold:imports
)

// constants for the controller managers capable environment variables.
const (
	// EnvVarNamespaces defines the environment variable to set the namespaces to watch.
	EnvVarNamespaces = "ROTATOR_NAMESPACES"
)

//nolint:funlen
func main() {
	setupLog := ctrl.Log.WithName("setup")
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var namespacesCli string
	var tlsOpts []func(*tls.Config)
	flag.StringVar(
		&metricsAddr,
		"metrics-bind-address",
		"0",
		"The address the metrics endpoint binds to. "+
			"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.",
	)
	flag.StringVar(
		&probeAddr,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.",
	)
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(
		&secureMetrics,
		"metrics-secure",
		true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.",
	)
	flag.StringVar(
		&webhookCertPath,
		"webhook-cert-path",
		"",
		"The directory that contains the webhook certificate.",
	)
	flag.StringVar(
		&webhookCertName,
		"webhook-cert-name",
		"tls.crt",
		"The name of the webhook certificate file.",
	)
	flag.StringVar(
		&webhookCertKey,
		"webhook-cert-key",
		"tls.key",
		"The name of the webhook key file.",
	)
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(
		&metricsCertName,
		"metrics-cert-name",
		"tls.crt",
		"The name of the metrics server certificate file.",
	)
	flag.StringVar(
		&metricsCertKey,
		"metrics-cert-key",
		"tls.key",
		"The name of the metrics server key file.",
	)
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(
		&namespacesCli,
		"namespaces",
		"",
		"Comma separated list of namespaces to watch. If not set, all namespaces will be watched.",
	)

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	namespacesMap := applyNamespacesFromCliOrEnv(namespacesCli, setupLog)

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watchers for metrics and webhooks certificates
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts

	if len(webhookCertPath) > 0 {
		setupLog.Info(
			"Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path",
			webhookCertPath,
			"webhook-cert-name",
			webhookCertName,
			"webhook-cert-key",
			webhookCertKey,
		)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertPath, webhookCertName),
			filepath.Join(webhookCertPath, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	if len(metricsCertPath) > 0 {
		setupLog.Info(
			"Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path",
			metricsCertPath,
			"metrics-cert-name",
			metricsCertName,
			"metrics-cert-key",
			metricsCertKey,
		)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(
			metricsServerOptions.TLSOpts,
			func(config *tls.Config) {
				config.GetCertificate = metricsCertWatcher.GetCertificate
			},
		)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "62f83323.rotator.gw.ei.telekom.de",
		Cache: cache.Options{
			DefaultNamespaces: namespacesMap,
		},
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.SecretReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		SourceAnnotation:     "rotator.gw.ei.telekom.de/source-secret",
		TargetNameAnnotation: "rotator.gw.ei.telekom.de/destination-secret-name",
		Finalizer:            "rotator.gw.ei.telekom.de/finalizer",
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Secret")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err = mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err = mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err = mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func applyNamespacesFromCliOrEnv(namespacesCli string, setupLog logr.Logger) map[string]cache.Config {
	// Parse the namespaces string into a slice
	var namespaces []string
	if namespacesCli != "" {
		namespaces = strings.Split(namespacesCli, ",")
	}

	// We also check if the env var ROTATOR_NAMESPACES is set and overwrite the flag value
	if namespacesEnv, ok := os.LookupEnv(EnvVarNamespaces); ok {
		namespaces = strings.Split(namespacesEnv, ",")
	}

	// Based on namespaces slice, we create a DefaultNamespaces map[string]Config object.
	// If the slice is empty, we set the DefaultNamespaces to nil.
	// If the slice is not empty, we set the DefaultNamespaces to a map with the namespaces
	// and a Config object with the namespaces.
	namespacesMap := make(map[string]cache.Config)
	if len(namespaces) > 0 && namespaces[0] != "" {
		setupLog.Info("listening to namespaces from cli param or environment variable", "namespaces", namespaces)

		for _, ns := range namespaces {
			namespacesMap[ns] = cache.Config{}
		}
	} else {
		setupLog.Info("listening to all namespaces, no namespaces provided in cli param or environment variable")
		namespacesMap = nil
	}

	return namespacesMap
}
