package main

import (
	"flag"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	invoicingv1alpha1 "github.com/invoraapp/invora-controller/api/invoicing/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/controller"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(billingv1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	utilruntime.Must(invoicingv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))

	// WATCH_NAMESPACE may be a comma-separated list. The subscriber writes
	// tenant CRs into INVORA_ENV's namespace, and reads/writes its state
	// ConfigMap from the controller's own namespace; both must be cached so
	// client.Reader hits don't bypass into uncached lists.
	watchNamespaces := splitNamespaces(os.Getenv("WATCH_NAMESPACE"))

	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "invora-billing-controller",
	}
	if len(watchNamespaces) > 0 {
		defaults := make(map[string]cache.Config, len(watchNamespaces))
		for _, ns := range watchNamespaces {
			defaults[ns] = cache.Config{}
		}
		options.Cache = cache.Options{DefaultNamespaces: defaults}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	clientCache := billingclient.NewCache()

	base := controller.BaseReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		ClientCache: clientCache,
	}

	controllers := []struct {
		name       string
		reconciler interface {
			SetupWithManager(ctrl.Manager) error
		}
	}{
		{"InvoraBillingInstance", &controller.InvoraBillingInstanceReconciler{BaseReconciler: base}},
		{"InvoraBillingOrganization", &controller.InvoraBillingOrganizationReconciler{BaseReconciler: base}},
		{"InvoraBillingMetric", &controller.InvoraBillingMetricReconciler{BaseReconciler: base}},
		{"InvoraBillingPlan", &controller.InvoraBillingPlanReconciler{BaseReconciler: base}},
		{"InvoraBillingTax", &controller.InvoraBillingTaxReconciler{BaseReconciler: base}},
		{"InvoraBillingAddon", &controller.InvoraBillingAddonReconciler{BaseReconciler: base}},
		{"InvoraBillingCoupon", &controller.InvoraBillingCouponReconciler{BaseReconciler: base}},
		{"InvoraBillingFeature", &controller.InvoraBillingFeatureReconciler{BaseReconciler: base}},
		{"InvoraBillingCustomer", &controller.InvoraBillingCustomerReconciler{BaseReconciler: base}},
		{"InvoraBillingSubscription", &controller.InvoraBillingSubscriptionReconciler{BaseReconciler: base}},
		{"InvoraBillingWebhookEndpoint", &controller.InvoraBillingWebhookEndpointReconciler{BaseReconciler: base}},
		{"InvoraBillingTapProvider", &controller.InvoraBillingTapProviderReconciler{BaseReconciler: base}},
		// Core controllers
		{"InvoraInstance", &controller.InvoraInstanceReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}},
		{"InvoraBranch", &controller.InvoraBranchReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ClientCache: clientCache}},
		{"InvoraConnectedBusiness", &controller.InvoraConnectedBusinessReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ClientCache: clientCache}},
		// Invoicing controllers
		{"InvoraInvoicingRegulation", &controller.InvoraInvoicingRegulationReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ClientCache: clientCache}},
		{"InvoraInvoicingSettings", &controller.InvoraInvoicingSettingsReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ClientCache: clientCache}},
	}

	for _, c := range controllers {
		if err := c.reconciler.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", c.name)
			os.Exit(1)
		}
	}

	// Phase 4 — Zitadel event subscriber. Activated only when
	// ZITADEL_DOMAIN + BILLING_INSTANCE_NAME are set. The subscriber polls the
	// Zitadel Admin API and materialises tenant InvoraBillingOrganization CRs.
	if err := setupZitadelSubscriber(mgr); err != nil {
		setupLog.Error(err, "unable to create zitadel event subscriber")
		os.Exit(1)
	}

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

// setupZitadelSubscriber wires the Phase 4 event subscriber when the env
// variables that point at a Zitadel instance + InvoraBillingInstance are set. Silently
// no-ops otherwise so the controller still starts in clusters where the
// subscriber is not desired (e.g. Bdaya shared infra).
func setupZitadelSubscriber(mgr ctrl.Manager) error {
	cfg := controller.ZitadelOrgEventSubscriberConfig{
		ZitadelDomain:         os.Getenv("ZITADEL_DOMAIN"),
		ZitadelPort:           os.Getenv("ZITADEL_PORT"),
		ZitadelInsecure:       strings.EqualFold(os.Getenv("ZITADEL_INSECURE"), "true"),
		SystemAPIUser:         os.Getenv("ZITADEL_SYSTEM_API_USER"),
		AccessToken:           os.Getenv("ZITADEL_ACCESS_TOKEN"),
		InvoraBillingInstanceName:      os.Getenv("BILLING_INSTANCE_NAME"),
		InvoraBillingInstanceNamespace: os.Getenv("BILLING_INSTANCE_NAMESPACE"),
		TenantNamespace:       tenantNamespaceFromEnv(),
		StateNamespace:        os.Getenv("POD_NAMESPACE"),
	}

	// Resolve the System API private key from a file path if set. Mounted
	// as a Secret volume.
	if path := os.Getenv("ZITADEL_SYSTEM_API_KEY_FILE"); path != "" {
		key, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		cfg.SystemAPIPrivateKey = key
	}

	// Optional poll interval override (Go duration string, e.g. "1m").
	if v := os.Getenv("ZITADEL_SUBSCRIBER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Interval = d
		}
	}

	sub := &controller.ZitadelOrgEventSubscriber{
		Client: mgr.GetClient(),
		Config: cfg,
	}
	return sub.SetupWithManager(mgr)
}

// tenantNamespaceFromEnv computes the tenant namespace the subscriber writes
// InvoraBillingOrganization CRs to. Prefers TENANT_NAMESPACE override; falls back to
// "invora-{INVORA_ENV}" for the standard Invora deployment.
func tenantNamespaceFromEnv() string {
	if ns := os.Getenv("TENANT_NAMESPACE"); ns != "" {
		return ns
	}
	if env := os.Getenv("INVORA_ENV"); env != "" {
		return "invora-" + env
	}
	return ""
}

// splitNamespaces parses a comma-separated namespace list. Empty entries
// are dropped; whitespace is trimmed.
func splitNamespaces(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
