package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraConnectedBusinessReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraConnectedBusinessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var cb corev1alpha1.InvoraConnectedBusiness
	if err := r.Get(ctx, req.NamespacedName, &cb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraConnectedBusiness", "name", cb.Spec.Name, "suspended", cb.Spec.Suspended)

	// TODO: Resolve InvoraInstance, call ConnectedBusiness gRPC service

	cb.Status.ObservedGeneration = cb.Generation
	now := metav1.Now()
	cb.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&cb.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: cb.Generation,
		Reason:             "Reconciled",
		Message:            "connected business reconciled",
	})
	if err := r.Status().Update(ctx, &cb); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraConnectedBusinessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.InvoraConnectedBusiness{}).
		Named("invoraconnectedbusiness").
		Complete(r)
}
