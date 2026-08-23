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

	// Snapshot the status so the terminal writes can be skipped when they would
	// be a no-op — see StatusUnchanged (invora/devops#68).
	statusSnapshot := *plan.Status.BillingResourceStatus.DeepCopy()
	externalIDBefore := plan.Status.ExternalID

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
		getResp, err := svc.Get(grpcCtx, &planspb.GetRequest{Id: plan.Status.ExternalID})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				plan.Status.ExternalID = ""
			} else {
				SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), plan.Generation)
				_ = r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if plan.Status.ExternalID != "" {
			// invora/devops#68 — change detection. Steady state on dev was
			// 330,415 PlansService/Update calls in 14 days (~16 WRITES/min)
			// because this Update fired on EVERY reconcile pass with no
			// desired-vs-observed comparison. Skip it when the live plan
			// already matches the CR AND the CR's generation is already
			// observed; on any doubt fall through to the Update, so the
			// worst case degrades to the previous (always-write) behaviour
			// rather than freezing drift.
			if plan.Status.ObservedGeneration != plan.Generation || !planInSync(&plan, getResp.GetPlan()) {
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
				})
				if err != nil {
					SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), plan.Generation)
					_ = r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
			setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "InSync")
			_ = r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID)
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
	//
	// invora/devops#109 (second site) — the probe FAILS CLOSED. This used to
	// read `if listResp, err := ...; err == nil { ... }`, which swallowed the
	// error and fell straight through to Create: a transient gateway failure
	// of the adoption probe turned the idempotent adopt-by-code path into an
	// unconditional create. That is exactly how one bayader webhook CR minted
	// 9 live endpoint records; the plan catalog had the identical shape.
	listResp, err := svc.List(grpcCtx, &planspb.ListRequest{})
	if err != nil {
		logger.Error(err, "adopt-by-code probe failed; not creating", "code", plan.Spec.Code)
		SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse,
			"AdoptProbeFailed",
			fmt.Sprintf("listing existing plans to adopt code %q failed; refusing to Create to avoid a duplicate: %v", plan.Spec.Code, err),
			plan.Generation)
		_ = r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	for _, existing := range listResp.GetItems() {
		if existing.GetCode() == plan.Spec.Code {
			logger.Info("found existing plan by code, adopting", "externalId", existing.GetId())
			plan.Status.ExternalID = existing.GetId()
			plan.Status.ID = existing.GetId()
			setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "Adopted")
			if err := r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
			}
			return SuccessResult(&plan), nil
		}
	}

	logger.Info("creating plan", "code", plan.Spec.Code)
	trialPeriod := parseTrialPeriod(plan.Spec.TrialPeriod)
	created, createErr := svc.Create(grpcCtx, &planspb.CreateRequest{
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
	})
	if createErr != nil {
		SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", createErr.Error(), plan.Generation)
		_ = r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	plan.Status.ExternalID = created.GetPlan().GetId()
	plan.Status.ID = created.GetPlan().GetId()
	setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "Created")
	if err := r.writeStatusIfChanged(ctx, &plan, &statusSnapshot, &plan.Status.BillingResourceStatus, externalIDBefore, plan.Status.ExternalID); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&plan), nil
}

// planInSync reports whether the live plan returned by PlansService/Get already
// matches the desired state the CR declares, so the reconcile can skip the
// Update RPC (invora/devops#68).
//
// It is deliberately CONSERVATIVE: it returns false (i.e. "issue the Update")
// on anything it cannot compare with confidence, so a normalisation mismatch
// degrades to the previous always-write behaviour rather than freezing real
// drift. In particular a plan that declares charges always takes the Update
// path — the desired shape is ChargeInput and the observed shape is Charge, and
// the two are not comparable field-for-field.
func planInSync(plan *billingv1alpha1.InvoraBillingPlan, live *commonpb.BillingPlan) bool {
	if live == nil {
		return false
	}
	if len(plan.Spec.Charges) > 0 || len(live.GetCharges()) > 0 {
		return false
	}
	if live.GetCode() != plan.Spec.Code ||
		live.GetName() != plan.Spec.Name ||
		live.GetDescription() != plan.Spec.Description ||
		live.GetAmountCents() != plan.Spec.AmountCents ||
		live.GetAmountCurrency() != convert.Currency(plan.Spec.AmountCurrency) ||
		live.GetInterval() != convert.PlanInterval(plan.Spec.Interval) ||
		live.GetPayInAdvance() != plan.Spec.PayInAdvance ||
		live.GetTrialPeriod() != parseTrialPeriod(plan.Spec.TrialPeriod) {
		return false
	}
	return taxCodesEqual(plan.Spec.TaxCodes, live.GetTaxes())
}

// taxCodesEqual compares the CR's declared tax codes against the codes of the
// taxes the backend reports on the plan, order-insensitively.
func taxCodesEqual(desired []string, live []*commonpb.BillingTax) bool {
	if len(desired) != len(live) {
		return false
	}
	have := make(map[string]struct{}, len(live))
	for _, t := range live {
		have[t.GetCode()] = struct{}{}
	}
	for _, code := range desired {
		if _, ok := have[code]; !ok {
			return false
		}
	}
	return true
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
