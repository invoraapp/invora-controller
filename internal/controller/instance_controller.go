package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraBillingInstanceReconciler struct {
	BaseReconciler
}

// +kubebuilder:rbac:groups=billing.invora.app,resources=billinginstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billinginstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billinginstances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *InvoraBillingInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	instance := &billingv1alpha1.InvoraBillingInstance{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraBillingInstance", "host", instance.Spec.GatewayURL)

	if !instance.DeletionTimestamp.IsZero() {
		r.ClientCache.InvalidateInstance(instance.Namespace, instance.Name)
		if err := r.RemoveFinalizer(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if added, err := r.EnsureFinalizer(ctx, instance); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	// Resolve super-admin token from Secret
	ref := instance.Spec.TokenRef
	tokenNS := ref.Namespace
	if tokenNS == "" {
		tokenNS = instance.Namespace
	}
	token, err := billingclient.ResolveSecretValue(ctx, r.Client, ref.Name, tokenNS, ref.Key, instance.Namespace)
	if err != nil {
		SetCondition(&instance.Status.Conditions, billingv1alpha1.ConditionReady,
			metav1.ConditionFalse, "AuthSecretError", err.Error(), instance.Generation)
		_ = r.Status().Update(ctx, instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Invalidate old client and create new one
	r.ClientCache.InvalidateInstance(instance.Namespace, instance.Name)
	client, err := r.ClientCache.GetOrCreateInstanceClient(instance.Namespace, instance.Name, billingclient.Config{
		GatewayURL: instance.Spec.GatewayURL,
		Token: token,
	})
	if err != nil {
		SetCondition(&instance.Status.Conditions, billingv1alpha1.ConditionReady,
			metav1.ConditionFalse, "ClientCreationFailed", err.Error(), instance.Generation)
		_ = r.Status().Update(ctx, instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Verify connectivity
	if err := client.CheckConnectivity(ctx); err != nil {
		SetCondition(&instance.Status.Conditions, billingv1alpha1.ConditionReady,
			metav1.ConditionFalse, "ConnectivityCheckFailed", err.Error(), instance.Generation)
		_ = r.Status().Update(ctx, instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	now := metav1.Now()
	instance.Status.LastConnectedAt = &now
	instance.Status.ObservedGeneration = instance.Generation
	instance.Status.LastSyncedAt = &now
	SetCondition(&instance.Status.Conditions, billingv1alpha1.ConditionReady,
		metav1.ConditionTrue, "Connected",
		fmt.Sprintf("Successfully connected to %s", instance.Spec.GatewayURL),
		instance.Generation)

	if err := r.Status().Update(ctx, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	logger.Info("InvoraBillingInstance is Ready", "host", instance.Spec.GatewayURL)
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *InvoraBillingInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingInstance{}).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findInstancesForSecret),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

func (r *InvoraBillingInstanceReconciler) findInstancesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var instances billingv1alpha1.InvoraBillingInstanceList
	if err := r.List(ctx, &instances); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, inst := range instances.Items {
		ref := inst.Spec.TokenRef
		ns := ref.Namespace
		if ns == "" {
			ns = inst.Namespace
		}
		if ns == secret.Namespace && ref.Name == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: inst.Namespace,
					Name:      inst.Name,
				},
			})
		}
	}
	return requests
}
