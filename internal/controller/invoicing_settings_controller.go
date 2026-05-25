package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	invoicingv1alpha1 "github.com/invoraapp/invora-controller/api/invoicing/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraInvoicingSettingsReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraInvoicingSettingsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var settings invoicingv1alpha1.InvoraInvoicingSettings
	if err := r.Get(ctx, req.NamespacedName, &settings); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraInvoicingSettings",
		"currency", settings.Spec.DefaultCurrency,
		"language", settings.Spec.DefaultLanguage)

	// TODO: Resolve InvoraInstance, call Settings gRPC service

	settings.Status.ObservedGeneration = settings.Generation
	now := metav1.Now()
	settings.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&settings.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: settings.Generation,
		Reason:             "Reconciled",
		Message:            "invoicing settings reconciled",
	})
	if err := r.Status().Update(ctx, &settings); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraInvoicingSettingsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&invoicingv1alpha1.InvoraInvoicingSettings{}).
		Named("invorainvoicingsettings").
		Complete(r)
}
