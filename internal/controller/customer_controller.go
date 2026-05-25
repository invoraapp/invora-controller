package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraBillingCustomerReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers/finalizers,verbs=update

func (r *InvoraBillingCustomerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var customer billingv1alpha1.InvoraBillingCustomer
	if err := r.Get(ctx, req.NamespacedName, &customer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !customer.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &customer,
			customer.Spec.OrganizationRef, customer.Spec.DeletionPolicy,
			customer.Status.ExternalID, &customer.Status.Conditions, customer.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeleteCustomer(ctx, customer.Spec.ExternalID)
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &customer); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, customer.Spec.OrganizationRef, &customer,
		&customer.Status.Conditions, customer.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&customer); importID != "" {
		customer.Status.ExternalID = importID
		customer.Status.ID = importID
		annotations := customer.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		customer.SetAnnotations(annotations)
		_ = r.Update(ctx, &customer)
		setSuccessStatus(&customer.Status.Conditions, &customer.Status.LastSyncedAt, &customer.Status.ObservedGeneration, customer.Generation, "Imported")
		_ = r.Status().Update(ctx, &customer)
		return SuccessResult(&customer), nil
	}

	apiCustomer := billingclient.Customer{
		ExternalID:   customer.Spec.ExternalID,
		Name:         customer.Spec.Name,
		Email:        customer.Spec.Email,
		Currency:     customer.Spec.Currency,
		AddressLine1: customer.Spec.AddressLine1,
		AddressLine2: customer.Spec.AddressLine2,
		City:         customer.Spec.City,
		Country:      customer.Spec.Country,
		State:        customer.Spec.State,
		Zipcode:      customer.Spec.Zipcode,
		LegalName:    customer.Spec.LegalName,
		LegalNumber:  customer.Spec.LegalNumber,
		Phone:        customer.Spec.Phone,
		Timezone:     customer.Spec.Timezone,
		TaxCodes:     customer.Spec.TaxCodes,
	}

	// billing's POST /customers is an upsert — it creates or updates by external_id.
	// We always call CreateOrUpdate regardless of whether we have a ExternalID.
	if customer.Status.ExternalID != "" {
		_, err := orc.billingClient.GetCustomer(ctx, customer.Spec.ExternalID)
		if err != nil {
			if billingclient.IsNotFound(err) {
				customer.Status.ExternalID = ""
			} else {
				SetCondition(&customer.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), customer.Generation)
				_ = r.Status().Update(ctx, &customer)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if customer.Status.ExternalID != "" {
			// Upsert to sync any drift
			if _, err := orc.billingClient.CreateOrUpdateCustomer(ctx, apiCustomer); err != nil {
				SetCondition(&customer.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), customer.Generation)
				_ = r.Status().Update(ctx, &customer)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&customer.Status.Conditions, &customer.Status.LastSyncedAt, &customer.Status.ObservedGeneration, customer.Generation, "InSync")
			_ = r.Status().Update(ctx, &customer)
			return SuccessResult(&customer), nil
		}
	}

	logger.Info("creating customer", "externalId", customer.Spec.ExternalID)
	created, err := orc.billingClient.CreateOrUpdateCustomer(ctx, apiCustomer)
	if err != nil {
		SetCondition(&customer.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), customer.Generation)
		_ = r.Status().Update(ctx, &customer)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	customer.Status.ExternalID = created.ID
	customer.Status.ID = created.ID
	setSuccessStatus(&customer.Status.Conditions, &customer.Status.LastSyncedAt, &customer.Status.ObservedGeneration, customer.Generation, "Created")
	if err := r.Status().Update(ctx, &customer); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&customer), nil
}

func (r *InvoraBillingCustomerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingCustomer{}).Named("customer").Complete(r)
}
