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

type InvoraInvoicingRegulationReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraInvoicingRegulationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var reg invoicingv1alpha1.InvoraInvoicingRegulation
	if err := r.Get(ctx, req.NamespacedName, &reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraInvoicingRegulation",
		"type", reg.Spec.RegulationType,
		"model", reg.Spec.SubmissionModel,
		"enabled", reg.Spec.Enabled)

	// TODO: Resolve InvoraInstance, call Regulation enrollment gRPC service

	reg.Status.ObservedGeneration = reg.Generation
	now := metav1.Now()
	reg.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: reg.Generation,
		Reason:             "Reconciled",
		Message:            "regulation config reconciled",
	})
	if err := r.Status().Update(ctx, &reg); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraInvoicingRegulationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&invoicingv1alpha1.InvoraInvoicingRegulation{}).
		Named("invorainvoicingregulation").
		Complete(r)
}
