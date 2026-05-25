package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

const (
	DefaultRequeueInterval    = 5 * time.Minute
	DependencyRequeueInterval = 10 * time.Second
)

// BaseReconciler provides shared infrastructure for all billing controllers.
type BaseReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

// ResolveInstanceAdmin looks up the InvoraBillingInstance referenced by instanceRef and
// returns a cached super-admin billing client plus a gRPC connection to the gateway.
func (r *BaseReconciler) ResolveInstanceAdmin(
	ctx context.Context,
	instanceRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
) (*instanceAdminContext, error) {
	ns := instanceRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	instance := &billingv1alpha1.InvoraBillingInstance{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      instanceRef.Name,
	}, instance); err != nil {
		return nil, fmt.Errorf("getting InvoraBillingInstance %s/%s: %w", ns, instanceRef.Name, err)
	}

	readyCond := meta.FindStatusCondition(instance.Status.Conditions, billingv1alpha1.ConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("InvoraBillingInstance %s/%s is not Ready", ns, instanceRef.Name)
	}

	ref := instance.Spec.TokenRef
	tokenNS := ref.Namespace
	if tokenNS == "" {
		tokenNS = ns
	}
	token, err := billingclient.ResolveSecretValue(ctx, r.Client, ref.Name, tokenNS, ref.Key, instance.Namespace)
	if err != nil {
		return nil, fmt.Errorf("resolving super-admin token: %w", err)
	}

	admin, err := r.ClientCache.GetOrCreateInstanceAdmin(ns, instanceRef.Name, billingclient.AdminConfig{
		GatewayURL: instance.Spec.GatewayURL,
		Token:      token,
	})
	if err != nil {
		return nil, fmt.Errorf("creating billing admin client: %w", err)
	}

	conn, err := dialGateway(instance.Spec.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("dialing gateway: %w", err)
	}

	return &instanceAdminContext{
		instance: instance,
		admin:    admin,
		conn:     conn,
		token:    token,
	}, nil
}

func (r *BaseReconciler) getReadyBillingOrganization(
	ctx context.Context,
	orgRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
) (*billingv1alpha1.InvoraBillingOrganization, error) {
	ns := orgRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	org := &billingv1alpha1.InvoraBillingOrganization{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      orgRef.Name,
	}, org); err != nil {
		return nil, fmt.Errorf("getting InvoraBillingOrganization %s/%s: %w", ns, orgRef.Name, err)
	}

	readyCond := meta.FindStatusCondition(org.Status.Conditions, billingv1alpha1.ConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("InvoraBillingOrganization %s/%s is not Ready", ns, orgRef.Name)
	}

	return org, nil
}

// EnsureFinalizer adds the billing finalizer if not present.
func (r *BaseReconciler) EnsureFinalizer(ctx context.Context, obj client.Object) (bool, error) {
	if !controllerutil.ContainsFinalizer(obj, billingv1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(obj, billingv1alpha1.FinalizerName)
		if err := r.Update(ctx, obj); err != nil {
			return false, fmt.Errorf("adding finalizer: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// RemoveFinalizer removes the billing finalizer.
func (r *BaseReconciler) RemoveFinalizer(ctx context.Context, obj client.Object) error {
	controllerutil.RemoveFinalizer(obj, billingv1alpha1.FinalizerName)
	return r.Update(ctx, obj)
}

// WriteSecret creates or updates a Secret with the given data, setting owner reference.
func (r *BaseReconciler) WriteSecret(
	ctx context.Context,
	owner client.Object,
	ref billingv1alpha1.WriteSecretToRef,
	defaultNamespace string,
	data map[string][]byte,
) error {
	ns := ref.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	secret := &corev1.Secret{}
	secret.Name = ref.Name
	secret.Namespace = ns

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = ref.Labels
		secret.Annotations = ref.Annotations
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}
		for k, v := range data {
			secret.Data[k] = v
		}
		if owner.GetNamespace() == ns {
			return controllerutil.SetOwnerReference(owner, secret, r.Scheme)
		}
		return nil
	})
	return err
}

// GetImportID returns the import-id annotation value, if set.
func GetImportID(obj client.Object) string {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return ""
	}
	return annotations[billingv1alpha1.AnnotationImportID]
}

// GetRequeueInterval returns the reconcile interval from the annotation, or default.
func GetRequeueInterval(obj client.Object) time.Duration {
	annotations := obj.GetAnnotations()
	if annotations != nil {
		if v, ok := annotations[billingv1alpha1.AnnotationReconcileInterval]; ok {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
	}
	return DefaultRequeueInterval
}

// SetCondition is a helper to set a condition on a BillingResourceStatus.
func SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

// SuccessResult returns a ctrl.Result with the appropriate requeue interval.
func SuccessResult(obj client.Object) ctrl.Result {
	return ctrl.Result{RequeueAfter: GetRequeueInterval(obj)}
}
