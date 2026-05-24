package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/billing-controller/api/v1alpha1"
	"github.com/invoraapp/billing-controller/internal/billingclient"
)

type InvoraBillingWebhookEndpointReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingwebhookendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingwebhookendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingwebhookendpoints/finalizers,verbs=update

func (r *InvoraBillingWebhookEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var wh billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(ctx, req.NamespacedName, &wh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !wh.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &wh,
			wh.Spec.OrganizationRef, wh.Spec.DeletionPolicy,
			wh.Status.ExternalID, &wh.Status.Conditions, wh.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				if wh.Status.ExternalID == "" {
					return nil
				}
				return c.DeleteWebhookEndpoint(ctx, wh.Status.ExternalID)
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

	apiWH := billingclient.WebhookEndpoint{
		WebhookURL:    wh.Spec.WebhookURL,
		SignatureAlgo: wh.Spec.SignatureAlgo,
	}

	if wh.Status.ExternalID != "" {
		_, err := orc.billingClient.GetWebhookEndpoint(ctx, wh.Status.ExternalID)
		if err != nil {
			if billingclient.IsNotFound(err) {
				wh.Status.ExternalID = ""
			} else {
				SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), wh.Generation)
				_ = r.Status().Update(ctx, &wh)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if wh.Status.ExternalID != "" {
			if _, err := orc.billingClient.UpdateWebhookEndpoint(ctx, wh.Status.ExternalID, apiWH); err != nil {
				SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), wh.Generation)
				_ = r.Status().Update(ctx, &wh)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "InSync")
			_ = r.Status().Update(ctx, &wh)
			return SuccessResult(&wh), nil
		}
	}

	// Before creating, list existing and match by URL for idempotency
	existing, err := orc.billingClient.ListWebhookEndpoints(ctx)
	if err == nil {
		for _, e := range existing {
			if e.WebhookURL == wh.Spec.WebhookURL {
				logger.Info("found existing webhook by URL, adopting", "externalId", e.ID)
				wh.Status.ExternalID = e.ID
				wh.Status.ID = e.ID
				setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Adopted")
				_ = r.Status().Update(ctx, &wh)
				return SuccessResult(&wh), nil
			}
		}
	}

	logger.Info("creating webhook endpoint", "url", wh.Spec.WebhookURL)
	created, err := orc.billingClient.CreateWebhookEndpoint(ctx, apiWH)
	if err != nil {
		SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), wh.Generation)
		_ = r.Status().Update(ctx, &wh)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	wh.Status.ExternalID = created.ID
	wh.Status.ID = created.ID
	setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Created")
	if err := r.Status().Update(ctx, &wh); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&wh), nil
}

func (r *InvoraBillingWebhookEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingWebhookEndpoint{}).Named("webhookendpoint").Complete(r)
}
