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
	paymentproviderspb "github.com/invoraapp/invora-controller/gen/invora/billing/payment_providers/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
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

	svc := paymentproviderspb.NewPaymentProvidersServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	// Resolve the provider by code in the ACTING org on EVERY reconcile, and
	// treat that lookup as authoritative over status.providerId.
	//
	// invora/invora-backend#209: status.providerId used to be trusted forever
	// (the lookup below was gated on it being empty), so a providerId minted
	// under a DIFFERENT org than the one we act as today wedged the CR
	// permanently. That is exactly what happened on dev: releases up to 1.3.2
	// stamped `x-invora-org-id`, which the WebGateway strips and rewrites to the
	// token's HOME (platform) org, so CreateTap landed the row in the platform
	// org; 1.3.3 (10a1ab0) switched to the gateway-validated `x-zitadel-orgid`
	// from spec.externalId, so the acting org became the tenant. The stored
	// platform-org id is invisible to the tenant org, and billing's lookup is
	// strictly org-scoped (PaymentProviders::FindService scopes
	// BaseProvider.where(organization_id:) before matching on id), so UpdateTap
	// found nothing, built a NEW record, and failed validation with
	// `{"api_key":["value_is_mandatory"]}` — UpdateTapRequest carries no api_key
	// field, only CreateTapRequest does. There was no path back to Create.
	//
	// Looking up by code first makes create, update and lookup agree on ONE org
	// scope: whatever the acting org is, we only ever Update a row that org can
	// actually see, and we Create (with the api key) when it cannot. A CR
	// stranded by the header change self-heals on its next reconcile, and an
	// already-remediated tenant row is adopted rather than duplicated.
	code := tap.Spec.Code
	foundID := ""
	resp, err := svc.Get(grpcCtx, &paymentproviderspb.GetRequest{Code: &code})
	switch {
	case err == nil:
		if tp := resp.GetPaymentProvider().GetTapProvider(); tp != nil {
			foundID = tp.GetId()
		}
	case isGrpcNotFound(err):
		// Absent in the acting org — fall through to the create path below.
	default:
		SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "LookupFailed", err.Error(), tap.Generation)
		_ = r.Status().Update(ctx, &tap)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	switch {
	case foundID == "":
		// Nothing in the acting org. Drop any stale/foreign id so the create
		// path runs instead of updating a row this org cannot see.
		if tap.Status.ProviderID != "" {
			logger.Info("stored provider id is not visible in the acting org, recreating",
				"staleProviderId", tap.Status.ProviderID, "code", tap.Spec.Code)
			tap.Status.ProviderID = ""
		}
	case foundID != tap.Status.ProviderID:
		logger.Info("adopting existing Tap provider from the acting org",
			"id", foundID, "previousProviderId", tap.Status.ProviderID)
		tap.Status.ProviderID = foundID
	}

	if tap.Status.ProviderID != "" {
		name := tap.Spec.Name
		redirect := tap.Spec.SuccessRedirectUrl
		updated, err := svc.UpdateTap(grpcCtx, &paymentproviderspb.UpdateTapRequest{
			Id:                 tap.Status.ProviderID,
			Code:               &code,
			Name:               &name,
			SuccessRedirectUrl: &redirect,
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
	created, err := svc.CreateTap(grpcCtx, &paymentproviderspb.CreateTapRequest{
		Code:               tap.Spec.Code,
		Name:               tap.Spec.Name,
		ApiKey:             &apiKey,
		SuccessRedirectUrl: &tap.Spec.SuccessRedirectUrl,
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
