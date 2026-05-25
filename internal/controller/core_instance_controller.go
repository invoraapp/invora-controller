package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *InvoraInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var instance corev1alpha1.InvoraInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraInstance", "gateway", instance.Spec.GatewayURL)

	ref := instance.Spec.TokenRef
	tokenNS := ref.Namespace
	if tokenNS == "" {
		tokenNS = instance.Namespace
	}
	if _, err := billingclient.ResolveSecretValue(ctx, r.Client, ref.Name, tokenNS, ref.Key, instance.Namespace); err != nil {
		meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: instance.Generation,
			Reason:             "TokenResolveFailed",
			Message:            err.Error(),
		})
		_ = r.Status().Update(ctx, &instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if err := checkGatewayConnectivity(ctx, instance.Spec.GatewayURL); err != nil {
		instance.Status.Connected = false
		meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: instance.Generation,
			Reason:             "ConnectivityFailed",
			Message:            fmt.Sprintf("gateway unreachable: %v", err),
		})
		_ = r.Status().Update(ctx, &instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	instance.Status.Connected = true
	instance.Status.ObservedGeneration = instance.Generation
	now := metav1.Now()
	instance.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: instance.Generation,
		Reason:             "Connected",
		Message:            fmt.Sprintf("connected to %s", instance.Spec.GatewayURL),
	})
	if err := r.Status().Update(ctx, &instance); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("InvoraInstance is Ready", "gateway", instance.Spec.GatewayURL)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.InvoraInstance{}).
		Named("invorainstance").
		Complete(r)
}
