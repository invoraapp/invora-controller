package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/billing-controller/api/v1alpha1"
	"github.com/invoraapp/billing-controller/internal/billingclient"
)

type InvoraBillingSubscriptionReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingsubscriptions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingsubscriptions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingsubscriptions/finalizers,verbs=update
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingcustomers,verbs=get;list;watch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingplans,verbs=get;list;watch

func (r *InvoraBillingSubscriptionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var sub billingv1alpha1.InvoraBillingSubscription
	if err := r.Get(ctx, req.NamespacedName, &sub); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sub.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &sub,
			sub.Spec.OrganizationRef, sub.Spec.DeletionPolicy,
			sub.Status.ExternalID, &sub.Status.Conditions, sub.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.TerminateSubscription(ctx, sub.Spec.ExternalID)
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &sub); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, sub.Spec.OrganizationRef, &sub,
		&sub.Status.Conditions, sub.Generation)
	if result != nil {
		return *result, nil
	}

	// Resolve cross-references
	customerExternalID, err := r.resolveCustomerExternalID(ctx, &sub)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "CustomerNotReady", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	planCode, err := r.resolvePlanCode(ctx, &sub)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "PlanNotReady", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	sub.Status.ResolvedCustomerExternalID = customerExternalID
	sub.Status.ResolvedPlanCode = planCode

	if importID := GetImportID(&sub); importID != "" {
		sub.Status.ExternalID = importID
		sub.Status.ID = importID
		annotations := sub.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		sub.SetAnnotations(annotations)
		_ = r.Update(ctx, &sub)
		setSuccessStatus(&sub.Status.Conditions, &sub.Status.LastSyncedAt, &sub.Status.ObservedGeneration, sub.Generation, "Imported")
		_ = r.Status().Update(ctx, &sub)
		return SuccessResult(&sub), nil
	}

	apiSub := billingclient.Subscription{
		ExternalID:         sub.Spec.ExternalID,
		ExternalCustomerID: customerExternalID,
		PlanCode:           planCode,
		Name:               sub.Spec.Name,
		BillingTime:        sub.Spec.BillingTime,
	}

	if sub.Status.ExternalID != "" {
		remote, err := orc.billingClient.GetSubscription(ctx, sub.Spec.ExternalID)
		if err != nil {
			if billingclient.IsNotFound(err) {
				sub.Status.ExternalID = ""
			} else {
				SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), sub.Generation)
				_ = r.Status().Update(ctx, &sub)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if sub.Status.ExternalID != "" {
			sub.Status.SubscriptionStatus = remote.Status
			if remote.Name != sub.Spec.Name || remote.PlanCode != planCode {
				if _, err := orc.billingClient.UpdateSubscription(ctx, sub.Spec.ExternalID, apiSub); err != nil {
					SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), sub.Generation)
					_ = r.Status().Update(ctx, &sub)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
			setSuccessStatus(&sub.Status.Conditions, &sub.Status.LastSyncedAt, &sub.Status.ObservedGeneration, sub.Generation, "InSync")
			_ = r.Status().Update(ctx, &sub)
			return SuccessResult(&sub), nil
		}
	}

	logger.Info("creating subscription", "externalId", sub.Spec.ExternalID)
	created, err := orc.billingClient.CreateSubscription(ctx, apiSub)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	sub.Status.ExternalID = created.ID
	sub.Status.ID = created.ID
	sub.Status.SubscriptionStatus = created.Status
	setSuccessStatus(&sub.Status.Conditions, &sub.Status.LastSyncedAt, &sub.Status.ObservedGeneration, sub.Generation, "Created")
	if err := r.Status().Update(ctx, &sub); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&sub), nil
}

func (r *InvoraBillingSubscriptionReconciler) resolveCustomerExternalID(ctx context.Context, sub *billingv1alpha1.InvoraBillingSubscription) (string, error) {
	if sub.Spec.ExternalCustomerID != "" {
		return sub.Spec.ExternalCustomerID, nil
	}
	if sub.Spec.CustomerRef == nil {
		return "", fmt.Errorf("either externalCustomerId or customerRef must be set")
	}

	ns := sub.Spec.CustomerRef.Namespace
	if ns == "" {
		ns = sub.Namespace
	}
	var customer billingv1alpha1.InvoraBillingCustomer
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: sub.Spec.CustomerRef.Name}, &customer); err != nil {
		return "", fmt.Errorf("getting InvoraBillingCustomer %s/%s: %w", ns, sub.Spec.CustomerRef.Name, err)
	}
	return customer.Spec.ExternalID, nil
}

func (r *InvoraBillingSubscriptionReconciler) resolvePlanCode(ctx context.Context, sub *billingv1alpha1.InvoraBillingSubscription) (string, error) {
	if sub.Spec.PlanCode != "" {
		return sub.Spec.PlanCode, nil
	}
	if sub.Spec.PlanRef == nil {
		return "", fmt.Errorf("either planCode or planRef must be set")
	}

	ns := sub.Spec.PlanRef.Namespace
	if ns == "" {
		ns = sub.Namespace
	}
	var plan billingv1alpha1.InvoraBillingPlan
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: sub.Spec.PlanRef.Name}, &plan); err != nil {
		return "", fmt.Errorf("getting InvoraBillingPlan %s/%s: %w", ns, sub.Spec.PlanRef.Name, err)
	}
	return plan.Spec.Code, nil
}

func (r *InvoraBillingSubscriptionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingSubscription{}).Named("subscription").Complete(r)
}
