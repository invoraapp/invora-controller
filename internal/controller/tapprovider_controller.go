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
	paymentproviderspb "github.com/invoraapp/invora-controller/gen/invora/billing/payment_providers/v2"
)

type InvoraBillingTapProviderReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingtappaymentproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingtappaymentproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingtappaymentproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *InvoraBillingTapProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tap billingv1alpha1.InvoraBillingTapProvider
	if err := r.Get(ctx, req.NamespacedName, &tap); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tap.DeletionTimestamp.IsZero() {
		logger.Info("removing finalizer (billing has no Tap destroy endpoint)",
			"providerCode", tap.Spec.Code)
		if err := r.RemoveFinalizer(ctx, &tap); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if added, err := r.EnsureFinalizer(ctx, &tap); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, tap.Spec.InvoraBillingOrganizationRef, &tap,
		&tap.Status.Conditions, tap.Generation)
	if result != nil {
		return *result, nil
	}

	apiKeyNS := tap.Spec.TapApiKeyRef.Namespace
	if apiKeyNS == "" {
		apiKeyNS = tap.Namespace
	}
	apiKey, err := billingclient.ResolveSecretValue(ctx, r.Client,
		tap.Spec.TapApiKeyRef.Name, apiKeyNS, tap.Spec.TapApiKeyRef.Key, tap.Namespace)
	if err != nil {
		SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "TapApiKeyResolveFailed", err.Error(), tap.Generation)
		_ = r.Status().Update(ctx, &tap)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	if importID := GetImportID(&tap); importID != "" {
		tap.Status.ProviderID = importID
		tap.Status.ID = importID
		tap.Status.ProviderCode = tap.Spec.Code
		annotations := tap.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		tap.SetAnnotations(annotations)
		_ = r.Update(ctx, &tap)
		setSuccessStatus(&tap.Status.Conditions, &tap.Status.LastSyncedAt,
			&tap.Status.ObservedGeneration, tap.Generation, "Imported")
		_ = r.Status().Update(ctx, &tap)
		return SuccessResult(&tap), nil
	}

	svc := paymentproviderspb.NewPaymentProviderServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if tap.Status.ProviderID == "" {
		code := tap.Spec.Code
		resp, err := svc.Get(grpcCtx, &paymentproviderspb.GetRequest{Code: &code})
		if err != nil && !isGrpcNotFound(err) {
			SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "LookupFailed", err.Error(), tap.Generation)
			_ = r.Status().Update(ctx, &tap)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if err == nil {
			if tp := resp.GetPaymentProvider().GetTapProvider(); tp != nil {
				logger.Info("adopting existing Tap provider", "id", tp.GetId())
				tap.Status.ProviderID = tp.GetId()
			}
		}
	}

	if tap.Status.ProviderID != "" {
		name := tap.Spec.Name
		code := tap.Spec.Code
		redirect := tap.Spec.SuccessRedirectUrl
		updated, err := svc.UpdateTapPaymentProvider(grpcCtx, &paymentproviderspb.UpdateTapPaymentProviderRequest{
			Input: &paymentproviderspb.UpdateTapPaymentProviderInput{
				Id:                 tap.Status.ProviderID,
				Code:               &code,
				Name:               &name,
				SuccessRedirectUrl: &redirect,
			},
		})
		if err != nil {
			SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "UpdateFailed", err.Error(), tap.Generation)
			_ = r.Status().Update(ctx, &tap)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		tap.Status.ProviderID = updated.GetTapProvider().GetId()
		tap.Status.ProviderCode = updated.GetTapProvider().GetCode()
		tap.Status.ID = updated.GetTapProvider().GetId()
		setSuccessStatus(&tap.Status.Conditions, &tap.Status.LastSyncedAt,
			&tap.Status.ObservedGeneration, tap.Generation, "InSync")
		if err := r.Status().Update(ctx, &tap); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return SuccessResult(&tap), nil
	}

	logger.Info("adding Tap payment provider", "code", tap.Spec.Code)
	created, err := svc.CreateTapPaymentProvider(grpcCtx, &paymentproviderspb.CreateTapPaymentProviderRequest{
		Input: &paymentproviderspb.AddTapPaymentProviderInput{
			Code:               tap.Spec.Code,
			Name:               tap.Spec.Name,
			ApiKey:             &apiKey,
			SuccessRedirectUrl: &tap.Spec.SuccessRedirectUrl,
		},
	})
	if err != nil {
		SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), tap.Generation)
		_ = r.Status().Update(ctx, &tap)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	tap.Status.ProviderID = created.GetTapProvider().GetId()
	tap.Status.ProviderCode = created.GetTapProvider().GetCode()
	tap.Status.ID = created.GetTapProvider().GetId()
	setSuccessStatus(&tap.Status.Conditions, &tap.Status.LastSyncedAt,
		&tap.Status.ObservedGeneration, tap.Generation, "Created")
	if err := r.Status().Update(ctx, &tap); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&tap), nil
}

func (r *InvoraBillingTapProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingTapProvider{}).
		Named("tapprovider").
		Complete(r)
}
