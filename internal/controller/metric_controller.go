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
		return r.BaseReconciler.handleCodeBasedDeletion(ctx, &metric,
			metric.Spec.OrganizationRef, metric.Spec.DeletionPolicy,
			metric.Status.ExternalID, &metric.Status.Conditions, metric.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeleteBillableMetric(ctx, metric.Spec.Code)
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

	// Import
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

	var apiFilters []billingclient.BillableMetricFilter
	for _, f := range metric.Spec.Filters {
		apiFilters = append(apiFilters, billingclient.BillableMetricFilter{
			Key:    f.Key,
			Values: f.Values,
		})
	}

	apiMetric := billingclient.BillableMetric{
		Code:             metric.Spec.Code,
		Name:             metric.Spec.Name,
		Description:      metric.Spec.Description,
		AggregationType:  metric.Spec.AggregationType,
		FieldName:        metric.Spec.FieldName,
		WeightedInterval: metric.Spec.WeightedInterval,
		Recurring:        metric.Spec.Recurring,
		Filters:          apiFilters,
	}

	if metric.Status.ExternalID != "" {
		// GET and update if drifted
		remote, err := orc.billingClient.GetBillableMetric(ctx, metric.Spec.Code)
		if err != nil {
			if billingclient.IsNotFound(err) {
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
			needsUpdate := remote.Name != metric.Spec.Name ||
				remote.Description != metric.Spec.Description ||
				remote.AggregationType != metric.Spec.AggregationType ||
				remote.FieldName != metric.Spec.FieldName

			if needsUpdate {
				logger.Info("metric drifted, updating", "code", metric.Spec.Code)
				if _, err := orc.billingClient.UpdateBillableMetric(ctx, metric.Spec.Code, apiMetric); err != nil {
					SetCondition(&metric.Status.Conditions, billingv1alpha1.ConditionSynced,
						metav1.ConditionFalse, "UpdateFailed", err.Error(), metric.Generation)
					_ = r.Status().Update(ctx, &metric)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}

			setSuccessStatus(&metric.Status.Conditions, &metric.Status.LastSyncedAt,
				&metric.Status.ObservedGeneration, metric.Generation, "InSync")
			if err := r.Status().Update(ctx, &metric); err != nil {
				return ctrl.Result{}, err
			}
			return SuccessResult(&metric), nil
		}
	}

	// Create
	logger.Info("creating billable metric", "code", metric.Spec.Code)
	created, err := orc.billingClient.CreateBillableMetric(ctx, apiMetric)
	if err != nil {
		SetCondition(&metric.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), metric.Generation)
		_ = r.Status().Update(ctx, &metric)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	metric.Status.ExternalID = created.ID
	metric.Status.ID = created.ID

	setSuccessStatus(&metric.Status.Conditions, &metric.Status.LastSyncedAt,
		&metric.Status.ObservedGeneration, metric.Generation, "Created")
	if err := r.Status().Update(ctx, &metric); err != nil {
		return ctrl.Result{}, err
	}
	return SuccessResult(&metric), nil
}

func (r *InvoraBillingMetricReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingMetric{}).
		Named("metric").
		Complete(r)
}
