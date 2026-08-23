package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingPlanReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingplans/finalizers,verbs=update

func (r *InvoraBillingPlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var plan billingv1alpha1.InvoraBillingPlan
	if err := r.Get(ctx, req.NamespacedName, &plan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !plan.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &plan,
			plan.Spec.OrganizationRef, plan.Spec.DeletionPolicy,
			plan.Status.ExternalID, &plan.Status.Conditions, plan.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := planspb.NewPlansServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &planspb.DeleteRequest{
					Id: plan.Status.ExternalID,
				})
				return err
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &plan); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, plan.Spec.OrganizationRef, &plan,
		&plan.Status.Conditions, plan.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&plan); importID != "" {
		plan.Status.ExternalID = importID
		plan.Status.ID = importID
		annotations := plan.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		plan.SetAnnotations(annotations)
		_ = r.Update(ctx, &plan)
		setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "Imported")
		_ = r.Status().Update(ctx, &plan)
		return SuccessResult(&plan), nil
	}

	svc := planspb.NewPlansServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if plan.Status.ExternalID != "" {
		_, err := svc.Get(grpcCtx, &planspb.GetRequest{Id: plan.Status.ExternalID})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				plan.Status.ExternalID = ""
			} else {
				SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), plan.Generation)
				_ = r.Status().Update(ctx, &plan)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if plan.Status.ExternalID != "" {
			trialPeriod := parseTrialPeriod(plan.Spec.TrialPeriod)
			_, err := svc.Update(grpcCtx, &planspb.UpdateRequest{
				Id:             plan.Status.ExternalID,
				Code:           plan.Spec.Code,
				Name:           plan.Spec.Name,
				Description:    strPtr(plan.Spec.Description),
				AmountCents:    plan.Spec.AmountCents,
				AmountCurrency: convert.Currency(plan.Spec.AmountCurrency),
				Interval:       convert.PlanInterval(plan.Spec.Interval),
				PayInAdvance:   plan.Spec.PayInAdvance,
				TrialPeriod:    &trialPeriod,
				TaxCodes:       plan.Spec.TaxCodes,
				Charges:        r.buildChargeInputs(&plan),
				Entitlements:   r.buildEntitlementInputs(&plan),
			})
			if err != nil {
				SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), plan.Generation)
				_ = r.Status().Update(ctx, &plan)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "InSync")
			_ = r.Status().Update(ctx, &plan)
			return SuccessResult(&plan), nil
		}
	}

	// Adopt-by-code: check for a pre-existing plan with this code in the same
	// org before attempting Create. Guards: only reached when
	// plan.Status.ExternalID == "" (structurally guaranteed by the branch this
	// sits in); List is called through orc's org-scoped grpcCtx (x-zitadel-orgid
	// header), so results are inherently same-org-only; match is an exact
	// string-equality on code, so adoption is deterministic. Mirrors the
	// List-first adopt pattern already used by organization_controller.go and
	// webhookendpoint_controller.go.
	if listResp, err := svc.List(grpcCtx, &planspb.ListRequest{}); err == nil {
		for _, existing := range listResp.GetItems() {
			if existing.GetCode() == plan.Spec.Code {
				logger.Info("found existing plan by code, adopting", "externalId", existing.GetId())
				plan.Status.ExternalID = existing.GetId()
				plan.Status.ID = existing.GetId()
				setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "Adopted")
				if err := r.Status().Update(ctx, &plan); err != nil {
					return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
				}
				return SuccessResult(&plan), nil
			}
		}
	}

	logger.Info("creating plan", "code", plan.Spec.Code)
	trialPeriod := parseTrialPeriod(plan.Spec.TrialPeriod)
	created, err := svc.Create(grpcCtx, &planspb.CreateRequest{
		Code:           plan.Spec.Code,
		Name:           plan.Spec.Name,
		Description:    strPtr(plan.Spec.Description),
		AmountCents:    plan.Spec.AmountCents,
		AmountCurrency: convert.Currency(plan.Spec.AmountCurrency),
		Interval:       convert.PlanInterval(plan.Spec.Interval),
		PayInAdvance:   plan.Spec.PayInAdvance,
		TrialPeriod:    &trialPeriod,
		TaxCodes:       plan.Spec.TaxCodes,
		Charges:        r.buildChargeInputs(&plan),
		Entitlements:   r.buildEntitlementInputs(&plan),
	})
	if err != nil {
		SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), plan.Generation)
		_ = r.Status().Update(ctx, &plan)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	plan.Status.ExternalID = created.GetPlan().GetId()
	plan.Status.ID = created.GetPlan().GetId()
	setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "Created")
	if err := r.Status().Update(ctx, &plan); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&plan), nil
}

func (r *InvoraBillingPlanReconciler) buildChargeInputs(plan *billingv1alpha1.InvoraBillingPlan) []*planspb.ChargeInput {
	if len(plan.Spec.Charges) == 0 {
		return nil
	}

	charges := make([]*planspb.ChargeInput, len(plan.Spec.Charges))
	for i, c := range plan.Spec.Charges {
		charge := &planspb.ChargeInput{
			BillableMetricId: c.BillableMetricCode, // gateway resolves code as ID
			ChargeModel:      chargeModelEnum(c.ChargeModel),
			PayInAdvance:     boolPtr(c.PayInAdvance),
			Prorated:         boolPtr(c.Prorated),
		}
		if c.InvoiceDisplayName != "" {
			charge.InvoiceDisplayName = &c.InvoiceDisplayName
		}
		if c.Properties != nil {
			var props commonpb.PropertiesInput
			_ = json.Unmarshal(c.Properties.Raw, &props)
			charge.Properties = &props
		}
		charges[i] = charge
	}
	return charges
}

// buildEntitlementInputs converts plan.Spec.Entitlements into the wire
// EntitlementInput slice. Returns nil for a nil spec.Entitlements — kept
// distinct from a non-nil empty slice at the Go layer for clarity, though
// proto3 gives repeated fields no on-wire presence, so nil and an empty
// slice serialize IDENTICALLY: the billing backend cannot tell "field
// omitted" from "field present but empty" either way. The real gate that
// keeps a plan CR which never declares a non-empty spec.entitlements (e.g.
// the Salla plans) from ever touching that plan's remote entitlements
// lives entirely on the backend — invora/lago/lago-api's
// update_params_from_proto only sets params[:entitlements] when
// `input.entitlements.any?` — NOT here. Do not rely on this function's
// nil-vs-empty distinction as a safety mechanism.
func (r *InvoraBillingPlanReconciler) buildEntitlementInputs(plan *billingv1alpha1.InvoraBillingPlan) []*commonpb.EntitlementInput {
	if plan.Spec.Entitlements == nil {
		return nil
	}

	entitlements := make([]*commonpb.EntitlementInput, len(plan.Spec.Entitlements))
	for i, e := range plan.Spec.Entitlements {
		privileges := make([]*commonpb.EntitlementPrivilegeInput, len(e.Privileges))
		for j, p := range e.Privileges {
			privileges[j] = &commonpb.EntitlementPrivilegeInput{
				PrivilegeCode: p.PrivilegeCode,
				Value:         p.Value,
			}
		}
		entitlements[i] = &commonpb.EntitlementInput{
			FeatureCode: e.FeatureCode,
			Privileges:  privileges,
		}
	}
	return entitlements
}

func chargeModelEnum(s string) commonpb.ChargeModel {
	key := "CHARGE_MODEL_" + s
	if v, ok := commonpb.ChargeModel_value[key]; ok {
		return commonpb.ChargeModel(v)
	}
	return commonpb.ChargeModel_CHARGE_MODEL_UNSPECIFIED
}

func parseTrialPeriod(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func (r *InvoraBillingPlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingPlan{}).Named("plan").Complete(r)
}
