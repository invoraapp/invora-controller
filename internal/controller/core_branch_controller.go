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

type InvoraBranchReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraBranchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var branch corev1alpha1.InvoraBranch
	if err := r.Get(ctx, req.NamespacedName, &branch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraBranch", "name", branch.Spec.Name, "default", branch.Spec.IsDefault)

	// TODO: Resolve InvoraInstance, call branches gRPC service to create/update

	branch.Status.ObservedGeneration = branch.Generation
	now := metav1.Now()
	branch.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&branch.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: branch.Generation,
		Reason:             "Reconciled",
		Message:            "branch reconciled",
	})
	if err := r.Status().Update(ctx, &branch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraBranchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.InvoraBranch{}).
		Named("invorabranch").
		Complete(r)
}
