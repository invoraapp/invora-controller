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

type InvoraBillingAddonReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingaddons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingaddons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingaddons/finalizers,verbs=update

func (r *InvoraBillingAddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var addon billingv1alpha1.InvoraBillingAddon
	if err := r.Get(ctx, req.NamespacedName, &addon); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !addon.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &addon,
			addon.Spec.OrganizationRef, addon.Spec.DeletionPolicy,
			addon.Status.ExternalID, &addon.Status.Conditions, addon.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeleteAddOn(ctx, addon.Spec.Code)
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &addon); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, addon.Spec.OrganizationRef, &addon,
		&addon.Status.Conditions, addon.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&addon); importID != "" {
		addon.Status.ExternalID = importID
		addon.Status.ID = importID
		annotations := addon.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		addon.SetAnnotations(annotations)
		_ = r.Update(ctx, &addon)
		setSuccessStatus(&addon.Status.Conditions, &addon.Status.LastSyncedAt, &addon.Status.ObservedGeneration, addon.Generation, "Imported")
		_ = r.Status().Update(ctx, &addon)
		return SuccessResult(&addon), nil
	}

	apiAddOn := billingclient.AddOn{
		Code:           addon.Spec.Code,
		Name:           addon.Spec.Name,
		Description:    addon.Spec.Description,
		AmountCents:    addon.Spec.AmountCents,
		AmountCurrency: addon.Spec.AmountCurrency,
		TaxCodes:       addon.Spec.TaxCodes,
	}

	if addon.Status.ExternalID != "" {
		remote, err := orc.billingClient.GetAddOn(ctx, addon.Spec.Code)
		if err != nil {
			if billingclient.IsNotFound(err) {
				addon.Status.ExternalID = ""
			} else {
				SetCondition(&addon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), addon.Generation)
				_ = r.Status().Update(ctx, &addon)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if addon.Status.ExternalID != "" {
			if remote.Name != addon.Spec.Name || remote.AmountCents != addon.Spec.AmountCents || remote.Description != addon.Spec.Description {
				if _, err := orc.billingClient.UpdateAddOn(ctx, addon.Spec.Code, apiAddOn); err != nil {
					SetCondition(&addon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), addon.Generation)
					_ = r.Status().Update(ctx, &addon)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
			setSuccessStatus(&addon.Status.Conditions, &addon.Status.LastSyncedAt, &addon.Status.ObservedGeneration, addon.Generation, "InSync")
			_ = r.Status().Update(ctx, &addon)
			return SuccessResult(&addon), nil
		}
	}

	logger.Info("creating add-on", "code", addon.Spec.Code)
	created, err := orc.billingClient.CreateAddOn(ctx, apiAddOn)
	if err != nil {
		SetCondition(&addon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), addon.Generation)
		_ = r.Status().Update(ctx, &addon)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	addon.Status.ExternalID = created.ID
	addon.Status.ID = created.ID
	setSuccessStatus(&addon.Status.Conditions, &addon.Status.LastSyncedAt, &addon.Status.ObservedGeneration, addon.Generation, "Created")
	if err := r.Status().Update(ctx, &addon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&addon), nil
}

func (r *InvoraBillingAddonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingAddon{}).Named("addon").Complete(r)
}
