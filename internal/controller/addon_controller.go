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
	addonspb "github.com/invoraapp/invora-controller/gen/invora/billing/add_ons/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingAddonReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingaddons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingaddons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingaddons/finalizers,verbs=update

func (r *InvoraBillingAddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var addon billingv1alpha1.InvoraBillingAddon
	if err := r.Get(ctx, req.NamespacedName, &addon); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !addon.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &addon,
			addon.Spec.OrganizationRef, addon.Spec.DeletionPolicy,
			addon.Status.ExternalID, &addon.Status.Conditions, addon.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := addonspb.NewAddOnServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &addonspb.DeleteRequest{
					Input: &addonspb.DestroyAddOnInput{Id: addon.Status.ExternalID},
				})
				return err
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

	svc := addonspb.NewAddOnServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if addon.Status.ExternalID != "" {
		_, err := svc.Get(grpcCtx, &addonspb.GetRequest{Id: addon.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				addon.Status.ExternalID = ""
			} else {
				SetCondition(&addon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), addon.Generation)
				_ = r.Status().Update(ctx, &addon)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if addon.Status.ExternalID != "" {
			_, err := svc.Update(grpcCtx, &addonspb.UpdateRequest{
				Input: buildUpdateAddOnInput(&addon),
			})
			if err != nil {
				SetCondition(&addon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), addon.Generation)
				_ = r.Status().Update(ctx, &addon)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&addon.Status.Conditions, &addon.Status.LastSyncedAt, &addon.Status.ObservedGeneration, addon.Generation, "InSync")
			_ = r.Status().Update(ctx, &addon)
			return SuccessResult(&addon), nil
		}
	}

	logger.Info("creating add-on", "code", addon.Spec.Code)
	created, err := svc.Create(grpcCtx, &addonspb.CreateRequest{
		Input: buildCreateAddOnInput(&addon),
	})
	if err != nil {
		SetCondition(&addon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), addon.Generation)
		_ = r.Status().Update(ctx, &addon)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	addon.Status.ExternalID = created.GetAddOn().GetId()
	addon.Status.ID = created.GetAddOn().GetId()
	setSuccessStatus(&addon.Status.Conditions, &addon.Status.LastSyncedAt, &addon.Status.ObservedGeneration, addon.Generation, "Created")
	if err := r.Status().Update(ctx, &addon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&addon), nil
}

func buildCreateAddOnInput(addon *billingv1alpha1.InvoraBillingAddon) *addonspb.CreateAddOnInput {
	in := &addonspb.CreateAddOnInput{
		Code:           addon.Spec.Code,
		Name:           addon.Spec.Name,
		AmountCents:    addon.Spec.AmountCents,
		AmountCurrency: convert.Currency(addon.Spec.AmountCurrency),
		TaxCodes:       addon.Spec.TaxCodes,
	}
	if addon.Spec.Description != "" {
		in.Description = &addon.Spec.Description
	}
	return in
}

func buildUpdateAddOnInput(addon *billingv1alpha1.InvoraBillingAddon) *addonspb.UpdateAddOnInput {
	in := &addonspb.UpdateAddOnInput{
		Id:             addon.Status.ExternalID,
		Code:           addon.Spec.Code,
		Name:           addon.Spec.Name,
		AmountCents:    addon.Spec.AmountCents,
		AmountCurrency: convert.Currency(addon.Spec.AmountCurrency),
		TaxCodes:       addon.Spec.TaxCodes,
	}
	if addon.Spec.Description != "" {
		in.Description = &addon.Spec.Description
	}
	return in
}

func (r *InvoraBillingAddonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingAddon{}).Named("addon").Complete(r)
}
