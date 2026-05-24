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

type InvoraBillingTaxReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingtaxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingtaxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingtaxes/finalizers,verbs=update

func (r *InvoraBillingTaxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tax billingv1alpha1.InvoraBillingTax
	if err := r.Get(ctx, req.NamespacedName, &tax); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tax.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &tax,
			tax.Spec.OrganizationRef, tax.Spec.DeletionPolicy,
			tax.Status.ExternalID, &tax.Status.Conditions, tax.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeleteTax(ctx, tax.Spec.Code)
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &tax); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, tax.Spec.OrganizationRef, &tax,
		&tax.Status.Conditions, tax.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&tax); importID != "" {
		tax.Status.ExternalID = importID
		tax.Status.ID = importID
		annotations := tax.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		tax.SetAnnotations(annotations)
		_ = r.Update(ctx, &tax)
		setSuccessStatus(&tax.Status.Conditions, &tax.Status.LastSyncedAt, &tax.Status.ObservedGeneration, tax.Generation, "Imported")
		_ = r.Status().Update(ctx, &tax)
		return SuccessResult(&tax), nil
	}

	apiTax := billingclient.Tax{
		Code:        tax.Spec.Code,
		Name:        tax.Spec.Name,
		Rate:        tax.Spec.Rate,
		Description: tax.Spec.Description,
	}

	if tax.Status.ExternalID != "" {
		remote, err := orc.billingClient.GetTax(ctx, tax.Spec.Code)
		if err != nil {
			if billingclient.IsNotFound(err) {
				tax.Status.ExternalID = ""
			} else {
				SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), tax.Generation)
				_ = r.Status().Update(ctx, &tax)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}

		if tax.Status.ExternalID != "" {
			if remote.Name != tax.Spec.Name || remote.Rate != tax.Spec.Rate || remote.Description != tax.Spec.Description {
				if _, err := orc.billingClient.UpdateTax(ctx, tax.Spec.Code, apiTax); err != nil {
					SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), tax.Generation)
					_ = r.Status().Update(ctx, &tax)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
			setSuccessStatus(&tax.Status.Conditions, &tax.Status.LastSyncedAt, &tax.Status.ObservedGeneration, tax.Generation, "InSync")
			_ = r.Status().Update(ctx, &tax)
			return SuccessResult(&tax), nil
		}
	}

	logger.Info("creating tax", "code", tax.Spec.Code)
	created, err := orc.billingClient.CreateTax(ctx, apiTax)
	if err != nil {
		SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), tax.Generation)
		_ = r.Status().Update(ctx, &tax)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	tax.Status.ExternalID = created.ID
	tax.Status.ID = created.ID
	setSuccessStatus(&tax.Status.Conditions, &tax.Status.LastSyncedAt, &tax.Status.ObservedGeneration, tax.Generation, "Created")
	if err := r.Status().Update(ctx, &tax); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&tax), nil
}

func (r *InvoraBillingTaxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingTax{}).Named("tax").Complete(r)
}
