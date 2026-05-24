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

	billingv1alpha1 "github.com/invoraapp/billing-controller/api/v1alpha1"
	"github.com/invoraapp/billing-controller/internal/billingclient"
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

// ResolveInstance looks up the InvoraBillingInstance referenced by instanceRef and
// returns a cached super-admin billing API client.
func (r *BaseReconciler) ResolveInstance(
	ctx context.Context,
	instanceRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
) (*billingclient.Client, *billingv1alpha1.InvoraBillingInstance, error) {
	ns := instanceRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	instance := &billingv1alpha1.InvoraBillingInstance{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      instanceRef.Name,
	}, instance); err != nil {
		return nil, nil, fmt.Errorf("getting InvoraBillingInstance %s/%s: %w", ns, instanceRef.Name, err)
	}

	readyCond := meta.FindStatusCondition(instance.Status.Conditions, billingv1alpha1.ConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		return nil, instance, fmt.Errorf("InvoraBillingInstance %s/%s is not Ready", ns, instanceRef.Name)
	}

	// Resolve super-admin token from Secret
	ref := instance.Spec.TokenRef
	tokenNS := ref.Namespace
	if tokenNS == "" {
		tokenNS = ns
	}
	token, err := billingclient.ResolveSecretValue(ctx, r.Client, ref.Name, tokenNS, ref.Key, instance.Namespace)
	if err != nil {
		return nil, instance, fmt.Errorf("resolving super-admin token: %w", err)
	}

	client, err := r.ClientCache.GetOrCreateInstanceClient(ns, instanceRef.Name, billingclient.Config{
		GatewayURL: instance.Spec.GatewayURL,
		Token:      token,
	})
	if err != nil {
		return nil, instance, fmt.Errorf("creating billing client: %w", err)
	}

	return client, instance, nil
}

// ResolveOrganization looks up a InvoraBillingOrganization CR, checks it is Ready,
// reads its API key from the Secret, and returns an org-scoped REST client.
func (r *BaseReconciler) ResolveOrganization(
	ctx context.Context,
	orgRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
) (*billingclient.Client, *billingv1alpha1.InvoraBillingOrganization, error) {
	ns := orgRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	org := &billingv1alpha1.InvoraBillingOrganization{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      orgRef.Name,
	}, org); err != nil {
		return nil, nil, fmt.Errorf("getting InvoraBillingOrganization %s/%s: %w", ns, orgRef.Name, err)
	}

	readyCond := meta.FindStatusCondition(org.Status.Conditions, billingv1alpha1.ConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		return nil, org, fmt.Errorf("InvoraBillingOrganization %s/%s is not Ready", ns, orgRef.Name)
	}

	// Read the org's API key from the Secret written by the org controller
	secretRef := org.Spec.WriteSecretToRef
	secretNS := secretRef.Namespace
	if secretNS == "" {
		secretNS = ns
	}
	apiKey, err := billingclient.ResolveSecretValue(ctx, r.Client, secretRef.Name, secretNS, "apiKey", org.Namespace)
	if err != nil {
		return nil, org, fmt.Errorf("resolving org API key: %w", err)
	}

	// We need the instance host to construct the client
	instance := &billingv1alpha1.InvoraBillingInstance{}
	instRef := org.Spec.InstanceRef
	instNS := instRef.Namespace
	if instNS == "" {
		instNS = ns
	}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: instNS,
		Name:      instRef.Name,
	}, instance); err != nil {
		return nil, org, fmt.Errorf("getting InvoraBillingInstance for org: %w", err)
	}

	client, err := r.ClientCache.GetOrCreateOrgClient(ns, orgRef.Name, billingclient.Config{
		GatewayURL: instance.Spec.GatewayURL,
		Token:      apiKey,
		OrgID:      string(org.Status.OrganizationID),
	})
	if err != nil {
		return nil, org, fmt.Errorf("creating org billing client: %w", err)
	}

	return client, org, nil
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
		// SetOwnerReference requires owner and owned object in same namespace.
		// For cross-namespace writes (e.g. InvoraBillingOrganization in billing-controller
		// writing to invora-dev), skip the ownerRef — GC won't apply anyway
		// across namespaces. Labels carry the lineage instead.
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
