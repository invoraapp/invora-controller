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

type InvoraBillingFeatureReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingfeatures,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingfeatures/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingfeatures/finalizers,verbs=update

func (r *InvoraBillingFeatureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var feature billingv1alpha1.InvoraBillingFeature
	if err := r.Get(ctx, req.NamespacedName, &feature); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !feature.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &feature,
			feature.Spec.OrganizationRef, feature.Spec.DeletionPolicy,
			feature.Status.ExternalID, &feature.Status.Conditions, feature.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeleteFeature(ctx, feature.Spec.Code)
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &feature); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, feature.Spec.OrganizationRef, &feature,
		&feature.Status.Conditions, feature.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&feature); importID != "" {
		feature.Status.ExternalID = importID
		feature.Status.ID = importID
		annotations := feature.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		feature.SetAnnotations(annotations)
		_ = r.Update(ctx, &feature)
		setSuccessStatus(&feature.Status.Conditions, &feature.Status.LastSyncedAt, &feature.Status.ObservedGeneration, feature.Generation, "Imported")
		_ = r.Status().Update(ctx, &feature)
		return SuccessResult(&feature), nil
	}

	apiFeature := billingclient.Feature{
		Code:        feature.Spec.Code,
		Name:        feature.Spec.Name,
		Description: feature.Spec.Description,
		Metadata:    feature.Spec.Metadata,
	}

	if feature.Status.ExternalID != "" {
		remote, err := orc.billingClient.GetFeature(ctx, feature.Spec.Code)
		if err != nil {
			if billingclient.IsNotFound(err) {
				feature.Status.ExternalID = ""
			} else {
				SetCondition(&feature.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), feature.Generation)
				_ = r.Status().Update(ctx, &feature)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if feature.Status.ExternalID != "" {
			if remote.Name != feature.Spec.Name || remote.Description != feature.Spec.Description || !mapsEqual(remote.Metadata, feature.Spec.Metadata) {
				if _, err := orc.billingClient.UpdateFeature(ctx, feature.Spec.Code, apiFeature); err != nil {
					SetCondition(&feature.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), feature.Generation)
					_ = r.Status().Update(ctx, &feature)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
			setSuccessStatus(&feature.Status.Conditions, &feature.Status.LastSyncedAt, &feature.Status.ObservedGeneration, feature.Generation, "InSync")
			_ = r.Status().Update(ctx, &feature)
			return SuccessResult(&feature), nil
		}
	}

	logger.Info("creating feature", "code", feature.Spec.Code)
	created, err := orc.billingClient.CreateFeature(ctx, apiFeature)
	if err != nil {
		SetCondition(&feature.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), feature.Generation)
		_ = r.Status().Update(ctx, &feature)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	feature.Status.ExternalID = created.ID
	feature.Status.ID = created.ID
	setSuccessStatus(&feature.Status.Conditions, &feature.Status.LastSyncedAt, &feature.Status.ObservedGeneration, feature.Generation, "Created")
	if err := r.Status().Update(ctx, &feature); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&feature), nil
}

func (r *InvoraBillingFeatureReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingFeature{}).Named("feature").Complete(r)
}
