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
				svc := taxespb.NewTaxesServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &taxespb.DeleteRequest{
					Id: tax.Status.ExternalID,
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

	svc := taxespb.NewTaxesServiceClient(orc.Conn())
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
			_, err := svc.Update(grpcCtx, buildTaxUpdateRequest(&tax))
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

	// Adopt-by-code before Create — see adopt_by_code.go for the full rationale.
	// Reached only when tax.Status.ExternalID == "" (structurally guaranteed by
	// the branch above). The probe runs on orc's org-scoped grpcCtx
	// (x-zitadel-orgid), so results are inherently same-org-only, and the match
	// is exact string equality on code, so adoption is deterministic.
	//
	// The probe FAILS CLOSED: on any error the reconciler refuses to Create,
	// because a failed probe cannot distinguish "no such tax" from "the tax
	// exists but I could not see it", and only the second is catastrophic
	// (invora/devops#109).
	adoptedID, err := scanPagesForCode(grpcCtx, tax.Spec.Code,
		func(ctx context.Context, cursor string) (string, string, int, error) {
			resp, err := svc.List(ctx, &taxespb.ListRequest{Pagination: adoptPagination(cursor)})
			if err != nil {
				return "", "", 0, err
			}
			for _, existing := range resp.GetItems() {
				if existing.GetCode() == tax.Spec.Code {
					return existing.GetId(), "", 0, nil
				}
			}
			return "", resp.GetNextPageCursor(), len(resp.GetItems()), nil
		})
	if err != nil {
		logger.Error(err, "adopt-by-code probe failed; not creating", "code", tax.Spec.Code)
		SetCondition(&tax.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse,
			"AdoptProbeFailed",
			fmt.Sprintf("listing existing taxes to adopt code %q failed; refusing to Create to avoid a duplicate: %v", tax.Spec.Code, err),
			tax.Generation)
		_ = r.Status().Update(ctx, &tax)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if adoptedID != "" {
		logger.Info("found existing tax by code, adopting", "code", tax.Spec.Code, "externalId", adoptedID)
		tax.Status.ExternalID = adoptedID
		tax.Status.ID = adoptedID
		setSuccessStatus(&tax.Status.Conditions, &tax.Status.LastSyncedAt, &tax.Status.ObservedGeneration, tax.Generation, "Adopted")
		if err := r.Status().Update(ctx, &tax); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return SuccessResult(&tax), nil
	}

	logger.Info("creating tax", "code", tax.Spec.Code)
	created, err := svc.Create(grpcCtx, buildTaxCreateRequest(&tax))
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

func buildTaxCreateRequest(tax *billingv1alpha1.InvoraBillingTax) *taxespb.CreateRequest {
	in := &taxespb.CreateRequest{
		Code: tax.Spec.Code,
		Name: tax.Spec.Name,
		Rate: convert.TaxRate(tax.Spec.Rate),
	}
	if tax.Spec.Description != "" {
		in.Description = &tax.Spec.Description
	}
	return in
}

func buildTaxUpdateRequest(tax *billingv1alpha1.InvoraBillingTax) *taxespb.UpdateRequest {
	in := &taxespb.UpdateRequest{
		Id:   tax.Status.ExternalID,
		Code: &tax.Spec.Code,
		Name: &tax.Spec.Name,
		Rate: convert.TaxRate(tax.Spec.Rate),
	}
	if tax.Spec.Description != "" {
		in.Description = &tax.Spec.Description
	}
	return in
}

func (r *InvoraBillingTaxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingTax{}).Named("tax").Complete(r)
}
