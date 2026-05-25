package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
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
		return r.handleCodeBasedDeletion(ctx, &plan,
			plan.Spec.OrganizationRef, plan.Spec.DeletionPolicy,
			plan.Status.ExternalID, &plan.Status.Conditions, plan.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeletePlan(ctx, plan.Spec.Code)
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

	apiPlan := r.buildAPIPlan(&plan)

	if plan.Status.ExternalID != "" {
		_, err := orc.billingClient.GetPlan(ctx, plan.Spec.Code)
		if err != nil {
			if billingclient.IsNotFound(err) {
				plan.Status.ExternalID = ""
			} else {
				SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), plan.Generation)
				_ = r.Status().Update(ctx, &plan)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if plan.Status.ExternalID != "" {
			if _, err := orc.billingClient.UpdatePlan(ctx, plan.Spec.Code, apiPlan); err != nil {
				SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), plan.Generation)
				_ = r.Status().Update(ctx, &plan)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "InSync")
			_ = r.Status().Update(ctx, &plan)
			return SuccessResult(&plan), nil
		}
	}

	logger.Info("creating plan", "code", plan.Spec.Code)
	created, err := orc.billingClient.CreatePlan(ctx, apiPlan)
	if err != nil {
		SetCondition(&plan.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), plan.Generation)
		_ = r.Status().Update(ctx, &plan)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	plan.Status.ExternalID = created.ID
	plan.Status.ID = created.ID
	setSuccessStatus(&plan.Status.Conditions, &plan.Status.LastSyncedAt, &plan.Status.ObservedGeneration, plan.Generation, "Created")
	if err := r.Status().Update(ctx, &plan); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&plan), nil
}

func (r *InvoraBillingPlanReconciler) buildAPIPlan(plan *billingv1alpha1.InvoraBillingPlan) billingclient.Plan {
	p := billingclient.Plan{
		Code:           plan.Spec.Code,
		Name:           plan.Spec.Name,
		Description:    plan.Spec.Description,
		AmountCents:    plan.Spec.AmountCents,
		AmountCurrency: plan.Spec.AmountCurrency,
		Interval:       plan.Spec.Interval,
		PayInAdvance:   plan.Spec.PayInAdvance,
		TrialPeriod:    parseTrialPeriod(plan.Spec.TrialPeriod),
		TaxCodes:       plan.Spec.TaxCodes,
	}

	if len(plan.Spec.Charges) > 0 {
		type apiCharge struct {
			BillableMetricCode string          `json:"billable_metric_code"`
			ChargeModel        string          `json:"charge_model"`
			InvoiceDisplayName string          `json:"invoice_display_name,omitempty"`
			PayInAdvance       bool            `json:"pay_in_advance,omitempty"`
			Prorated           bool            `json:"prorated,omitempty"`
			Properties         json.RawMessage `json:"properties,omitempty"`
		}

		charges := make([]apiCharge, len(plan.Spec.Charges))
		for i, c := range plan.Spec.Charges {
			charges[i] = apiCharge{
				BillableMetricCode: c.BillableMetricCode,
				ChargeModel:        c.ChargeModel,
				InvoiceDisplayName: c.InvoiceDisplayName,
				PayInAdvance:       c.PayInAdvance,
				Prorated:           c.Prorated,
			}
			if c.Properties != nil {
				charges[i].Properties = c.Properties.Raw
			}
		}
		chargesJSON, _ := json.Marshal(charges)
		p.Charges = chargesJSON
	}

	return p
}

func parseTrialPeriod(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func (r *InvoraBillingPlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingPlan{}).Named("plan").Complete(r)
}
