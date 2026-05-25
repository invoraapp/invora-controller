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
	taxespb "github.com/invoraapp/invora-controller/gen/invora/billing/taxes/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingTaxReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingtaxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingtaxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingtaxes/finalizers,verbs=update

func (r *InvoraBillingTaxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tax billingv1alpha1.InvoraBillingTax
	if err := r.Get(ctx, req.NamespacedName, &tax); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tax.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &tax,
			tax.Spec.OrganizationRef, tax.Spec.DeletionPolicy,
			tax.Status.ExternalID, &tax.Status.Conditions, tax.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := taxespb.NewTaxServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &taxespb.DeleteRequest{
					Input: &taxespb.DestroyTaxInput{Id: tax.Status.ExternalID},
				})
				return err
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

	svc := taxespb.NewTaxServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if tax.Status.ExternalID != "" {
		_, err := svc.Get(grpcCtx, &taxespb.GetRequest{Id: tax.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				tax.Status.ExternalID = ""
			} else {
				SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), tax.Generation)
				_ = r.Status().Update(ctx, &tax)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if tax.Status.ExternalID != "" {
			_, err := svc.Update(grpcCtx, &taxespb.UpdateRequest{
				Input: buildTaxUpdateInput(&tax),
			})
			if err != nil {
				SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), tax.Generation)
				_ = r.Status().Update(ctx, &tax)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&tax.Status.Conditions, &tax.Status.LastSyncedAt, &tax.Status.ObservedGeneration, tax.Generation, "InSync")
			_ = r.Status().Update(ctx, &tax)
			return SuccessResult(&tax), nil
		}
	}

	logger.Info("creating tax", "code", tax.Spec.Code)
	created, err := svc.Create(grpcCtx, &taxespb.CreateRequest{
		Input: buildTaxCreateInput(&tax),
	})
	if err != nil {
		SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), tax.Generation)
		_ = r.Status().Update(ctx, &tax)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	tax.Status.ExternalID = created.GetTax().GetId()
	tax.Status.ID = created.GetTax().GetId()
	setSuccessStatus(&tax.Status.Conditions, &tax.Status.LastSyncedAt, &tax.Status.ObservedGeneration, tax.Generation, "Created")
	if err := r.Status().Update(ctx, &tax); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&tax), nil
}

func buildTaxCreateInput(tax *billingv1alpha1.InvoraBillingTax) *taxespb.TaxCreateInput {
	in := &taxespb.TaxCreateInput{
		Code: tax.Spec.Code,
		Name: tax.Spec.Name,
		Rate: convert.TaxRate(tax.Spec.Rate),
	}
	if tax.Spec.Description != "" {
		in.Description = &tax.Spec.Description
	}
	return in
}

func buildTaxUpdateInput(tax *billingv1alpha1.InvoraBillingTax) *taxespb.TaxUpdateInput {
	rate := convert.TaxRate(tax.Spec.Rate)
	in := &taxespb.TaxUpdateInput{
		Id:   tax.Status.ExternalID,
		Code: &tax.Spec.Code,
		Name: &tax.Spec.Name,
		Rate: &rate,
	}
	if tax.Spec.Description != "" {
		in.Description = &tax.Spec.Description
	}
	return in
}

func (r *InvoraBillingTaxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingTax{}).Named("tax").Complete(r)
}
