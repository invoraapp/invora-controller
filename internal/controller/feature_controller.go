package controller

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
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
		return r.handleGrpcDeletion(ctx, &feature,
			feature.Spec.OrganizationRef, feature.Spec.DeletionPolicy,
			feature.Status.ExternalID, &feature.Status.Conditions, feature.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := planspb.NewPlansServiceClient(orc.Conn())
				_, err := svc.DeleteFeature(orc.GrpcCtx(ctx), &planspb.DeleteFeatureRequest{
					Id: feature.Status.ExternalID,
				})
				return err
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

	svc := planspb.NewPlansServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if feature.Status.ExternalID != "" {
		_, err := svc.GetFeature(grpcCtx, &planspb.GetFeatureRequest{Id: feature.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				feature.Status.ExternalID = ""
			} else {
				SetCondition(&feature.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), feature.Generation)
				_ = r.Status().Update(ctx, &feature)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if feature.Status.ExternalID != "" {
			_, err := svc.UpdateFeature(grpcCtx, buildUpdateFeatureRequest(&feature))
			if err != nil {
				SetCondition(&feature.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), feature.Generation)
				_ = r.Status().Update(ctx, &feature)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&feature.Status.Conditions, &feature.Status.LastSyncedAt, &feature.Status.ObservedGeneration, feature.Generation, "InSync")
			_ = r.Status().Update(ctx, &feature)
			return SuccessResult(&feature), nil
		}
	}

	logger.Info("creating feature", "code", feature.Spec.Code)
	created, err := svc.CreateFeature(grpcCtx, buildCreateFeatureRequest(&feature))
	if err != nil {
		// Defensive adopt: a prior reconcile may have created this feature and
		// lost its ExternalID write before persisting it (reproduced live —
		// invora-controller/bayader-session-quota, wedged on
		// Synced=False/CreateFailed since 2026-07-19, "value_already_exist" on
		// every retry because Status.ExternalID stayed empty). Recover by
		// looking the feature up by its (immutable) code rather than storming
		// on AlreadyExists forever — mirrors the organization/Zitadel
		// controllers' adopt-on-AlreadyExists pattern (bayader-devops#100).
		if status.Code(err) == codes.AlreadyExists {
			code := feature.Spec.Code
			if existing, gerr := svc.GetFeature(grpcCtx, &planspb.GetFeatureRequest{Code: &code}); gerr == nil {
				logger.Info("feature already exists, adopting", "id", existing.GetFeature().GetId())
				feature.Status.ExternalID = existing.GetFeature().GetId()
				feature.Status.ID = existing.GetFeature().GetId()
				setSuccessStatus(&feature.Status.Conditions, &feature.Status.LastSyncedAt, &feature.Status.ObservedGeneration, feature.Generation, "Adopted")
				if serr := r.Status().Update(ctx, &feature); serr != nil {
					return ctrl.Result{}, fmt.Errorf("updating status: %w", serr)
				}
				return SuccessResult(&feature), nil
			}
		}
		SetCondition(&feature.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), feature.Generation)
		_ = r.Status().Update(ctx, &feature)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	feature.Status.ExternalID = created.GetFeature().GetId()
	feature.Status.ID = created.GetFeature().GetId()
	setSuccessStatus(&feature.Status.Conditions, &feature.Status.LastSyncedAt, &feature.Status.ObservedGeneration, feature.Generation, "Created")
	if err := r.Status().Update(ctx, &feature); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&feature), nil
}

func buildCreateFeatureRequest(feature *billingv1alpha1.InvoraBillingFeature) *planspb.CreateFeatureRequest {
	in := &planspb.CreateFeatureRequest{
		Code:     feature.Spec.Code,
		Metadata: convert.MetadataInputs(feature.Spec.Metadata),
	}
	if feature.Spec.Name != "" {
		in.Name = &feature.Spec.Name
	}
	if feature.Spec.Description != "" {
		in.Description = &feature.Spec.Description
	}
	return in
}

func buildUpdateFeatureRequest(feature *billingv1alpha1.InvoraBillingFeature) *planspb.UpdateFeatureRequest {
	in := &planspb.UpdateFeatureRequest{
		Id:       feature.Status.ExternalID,
		Metadata: convert.MetadataInputs(feature.Spec.Metadata),
	}
	if feature.Spec.Name != "" {
		in.Name = &feature.Spec.Name
	}
	if feature.Spec.Description != "" {
		in.Description = &feature.Spec.Description
	}
	return in
}

func (r *InvoraBillingFeatureReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingFeature{}).Named("feature").Complete(r)
}
