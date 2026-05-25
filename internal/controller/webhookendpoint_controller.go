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
	webhookspb "github.com/invoraapp/invora-controller/gen/invora/billing/webhooks/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingWebhookEndpointReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingwebhookendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingwebhookendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingwebhookendpoints/finalizers,verbs=update

func (r *InvoraBillingWebhookEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var wh billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(ctx, req.NamespacedName, &wh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !wh.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &wh,
			wh.Spec.OrganizationRef, wh.Spec.DeletionPolicy,
			wh.Status.ExternalID, &wh.Status.Conditions, wh.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				if wh.Status.ExternalID == "" {
					return nil
				}
				svc := webhookspb.NewWebhookEndpointServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &webhookspb.DeleteRequest{
					Input: &webhookspb.DestroyWebhookEndpointInput{Id: wh.Status.ExternalID},
				})
				return err
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &wh); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, wh.Spec.OrganizationRef, &wh,
		&wh.Status.Conditions, wh.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&wh); importID != "" {
		wh.Status.ExternalID = importID
		wh.Status.ID = importID
		annotations := wh.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		wh.SetAnnotations(annotations)
		_ = r.Update(ctx, &wh)
		setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Imported")
		_ = r.Status().Update(ctx, &wh)
		return SuccessResult(&wh), nil
	}

	svc := webhookspb.NewWebhookEndpointServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if wh.Status.ExternalID != "" {
		_, err := svc.Get(grpcCtx, &webhookspb.GetRequest{Id: wh.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				wh.Status.ExternalID = ""
			} else {
				SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), wh.Generation)
				_ = r.Status().Update(ctx, &wh)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if wh.Status.ExternalID != "" {
			_, err := svc.Update(grpcCtx, &webhookspb.UpdateRequest{
				Input: buildWebhookUpdateInput(&wh),
			})
			if err != nil {
				SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), wh.Generation)
				_ = r.Status().Update(ctx, &wh)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "InSync")
			_ = r.Status().Update(ctx, &wh)
			return SuccessResult(&wh), nil
		}
	}

	listResp, err := svc.List(grpcCtx, &webhookspb.ListRequest{})
	if err == nil {
		for _, e := range listResp.GetItems() {
			if e.GetWebhookUrl() == wh.Spec.WebhookURL {
				logger.Info("found existing webhook by URL, adopting", "externalId", e.GetId())
				wh.Status.ExternalID = e.GetId()
				wh.Status.ID = e.GetId()
				setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Adopted")
				_ = r.Status().Update(ctx, &wh)
				return SuccessResult(&wh), nil
			}
		}
	}

	logger.Info("creating webhook endpoint", "url", wh.Spec.WebhookURL)
	created, err := svc.Create(grpcCtx, &webhookspb.CreateRequest{
		Input: buildWebhookCreateInput(&wh),
	})
	if err != nil {
		SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), wh.Generation)
		_ = r.Status().Update(ctx, &wh)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	wh.Status.ExternalID = created.GetWebhookEndpoint().GetId()
	wh.Status.ID = created.GetWebhookEndpoint().GetId()
	setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Created")
	if err := r.Status().Update(ctx, &wh); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&wh), nil
}

func buildWebhookCreateInput(wh *billingv1alpha1.InvoraBillingWebhookEndpoint) *webhookspb.WebhookEndpointCreateInput {
	in := &webhookspb.WebhookEndpointCreateInput{
		WebhookUrl: wh.Spec.WebhookURL,
	}
	if wh.Spec.SignatureAlgo != "" {
		algo := convert.WebhookSignatureAlgo(wh.Spec.SignatureAlgo)
		in.SignatureAlgo = &algo
	}
	return in
}

func buildWebhookUpdateInput(wh *billingv1alpha1.InvoraBillingWebhookEndpoint) *webhookspb.WebhookEndpointUpdateInput {
	in := &webhookspb.WebhookEndpointUpdateInput{
		Id:         wh.Status.ExternalID,
		WebhookUrl: wh.Spec.WebhookURL,
	}
	if wh.Spec.SignatureAlgo != "" {
		algo := convert.WebhookSignatureAlgo(wh.Spec.SignatureAlgo)
		in.SignatureAlgo = &algo
	}
	return in
}

func (r *InvoraBillingWebhookEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingWebhookEndpoint{}).Named("webhookendpoint").Complete(r)
}
