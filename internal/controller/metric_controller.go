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
	meteringpb "github.com/invoraapp/invora-controller/gen/invora/billing/metering/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingMetricReconciler struct {
	BaseReconciler
}

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingbillablemetrics,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingbillablemetrics/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingbillablemetrics/finalizers,verbs=update

func (r *InvoraBillingMetricReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var metric billingv1alpha1.InvoraBillingMetric
	if err := r.Get(ctx, req.NamespacedName, &metric); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !metric.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &metric,
			metric.Spec.OrganizationRef, metric.Spec.DeletionPolicy,
			metric.Status.ExternalID, &metric.Status.Conditions, metric.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := meteringpb.NewBillableMetricServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &meteringpb.DeleteRequest{
					Input: &meteringpb.DestroyBillableMetricInput{Id: metric.Status.ExternalID},
				})
				return err
			})
	}

	added, err := r.EnsureFinalizer(ctx, &metric)
	if err != nil {
		return ctrl.Result{}, err
	}
	if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, metric.Spec.OrganizationRef, &metric,
		&metric.Status.Conditions, metric.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&metric); importID != "" {
		metric.Status.ExternalID = importID
		metric.Status.ID = importID
		annotations := metric.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		metric.SetAnnotations(annotations)
		if err := r.Update(ctx, &metric); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing import-id: %w", err)
		}
		setSuccessStatus(&metric.Status.Conditions, &metric.Status.LastSyncedAt,
			&metric.Status.ObservedGeneration, metric.Generation, "Imported")
		if err := r.Status().Update(ctx, &metric); err != nil {
			return ctrl.Result{}, err
		}
		return SuccessResult(&metric), nil
	}

	svc := meteringpb.NewBillableMetricServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if metric.Status.ExternalID != "" {
		_, err := svc.Get(grpcCtx, &meteringpb.GetRequest{Id: metric.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				logger.Info("metric not found in billing, will recreate", "code", metric.Spec.Code)
				metric.Status.ExternalID = ""
			} else {
				SetCondition(&metric.Status.Conditions, billingv1alpha1.ConditionSynced,
					metav1.ConditionFalse, "GetFailed", err.Error(), metric.Generation)
				_ = r.Status().Update(ctx, &metric)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if metric.Status.ExternalID != "" {
			_, err := svc.Update(grpcCtx, &meteringpb.UpdateRequest{
				Input: buildUpdateBillableMetricInput(&metric),
			})
			if err != nil {
				SetCondition(&metric.Status.Conditions, billingv1alpha1.ConditionSynced,
					metav1.ConditionFalse, "UpdateFailed", err.Error(), metric.Generation)
				_ = r.Status().Update(ctx, &metric)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&metric.Status.Conditions, &metric.Status.LastSyncedAt,
				&metric.Status.ObservedGeneration, metric.Generation, "InSync")
			if err := r.Status().Update(ctx, &metric); err != nil {
				return ctrl.Result{}, err
			}
			return SuccessResult(&metric), nil
		}
	}

	logger.Info("creating billable metric", "code", metric.Spec.Code)
	created, err := svc.Create(grpcCtx, &meteringpb.CreateRequest{
		Input: buildCreateBillableMetricInput(&metric),
	})
	if err != nil {
		SetCondition(&metric.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), metric.Generation)
		_ = r.Status().Update(ctx, &metric)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	metric.Status.ExternalID = created.GetBillableMetric().GetId()
	metric.Status.ID = created.GetBillableMetric().GetId()

	setSuccessStatus(&metric.Status.Conditions, &metric.Status.LastSyncedAt,
		&metric.Status.ObservedGeneration, metric.Generation, "Created")
	if err := r.Status().Update(ctx, &metric); err != nil {
		return ctrl.Result{}, err
	}
	return SuccessResult(&metric), nil
}

func buildBillableMetricFilters(metric *billingv1alpha1.InvoraBillingMetric) []*meteringpb.BillableMetricFiltersInput {
	if len(metric.Spec.Filters) == 0 {
		return nil
	}
	out := make([]*meteringpb.BillableMetricFiltersInput, len(metric.Spec.Filters))
	for i, f := range metric.Spec.Filters {
		out[i] = &meteringpb.BillableMetricFiltersInput{
			Key:    f.Key,
			Values: f.Values,
		}
	}
	return out
}

func buildCreateBillableMetricInput(metric *billingv1alpha1.InvoraBillingMetric) *meteringpb.CreateBillableMetricInput {
	in := &meteringpb.CreateBillableMetricInput{
		Code:            metric.Spec.Code,
		Name:            metric.Spec.Name,
		Description:     metric.Spec.Description,
		AggregationType: convert.AggregationType(metric.Spec.AggregationType),
		Filters:         buildBillableMetricFilters(metric),
	}
	if metric.Spec.FieldName != "" {
		in.FieldName = &metric.Spec.FieldName
	}
	if metric.Spec.WeightedInterval != "" {
		wi := convert.WeightedInterval(metric.Spec.WeightedInterval)
		in.WeightedInterval = &wi
	}
	if metric.Spec.Recurring {
		recurring := true
		in.Recurring = &recurring
	}
	return in
}

func buildUpdateBillableMetricInput(metric *billingv1alpha1.InvoraBillingMetric) *meteringpb.UpdateBillableMetricInput {
	in := &meteringpb.UpdateBillableMetricInput{
		Id:              metric.Status.ExternalID,
		Code:            metric.Spec.Code,
		Name:            metric.Spec.Name,
		Description:     metric.Spec.Description,
		AggregationType: convert.AggregationType(metric.Spec.AggregationType),
		Filters:         buildBillableMetricFilters(metric),
	}
	if metric.Spec.FieldName != "" {
		in.FieldName = &metric.Spec.FieldName
	}
	if metric.Spec.WeightedInterval != "" {
		wi := convert.WeightedInterval(metric.Spec.WeightedInterval)
		in.WeightedInterval = &wi
	}
	if metric.Spec.Recurring {
		recurring := true
		in.Recurring = &recurring
	}
	return in
}

func (r *InvoraBillingMetricReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingMetric{}).
		Named("metric").
		Complete(r)
}
