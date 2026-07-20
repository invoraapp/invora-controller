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
		} else {
			// Billing resolves the code without a type filter, so another
			// provider kind can own it. Creating below will then trip the
			// org-scoped code-uniqueness constraint with an opaque message;
			// say so here, or the CR just loops on CreateFailed.
			logger.Info("code is owned by a non-Tap provider in the acting org; create will collide",
				"code", tap.Spec.Code)
		}
	case isGrpcNotFound(err):
		// Absent in the acting org — fall through to the create path below.
	default:
		SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "LookupFailed", err.Error(), tap.Generation)
		_ = r.Status().Update(ctx, &tap)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Resolve the id to act on in a LOCAL, so status.providerId only ever moves
	// on a successful write. Clearing it in place would let a failed create
	// persist the erasure and destroy the only in-cluster record of the row the
	// CR used to point at.
	providerID := tap.Status.ProviderID
	switch {
	case foundID == "":
		// Nothing in the acting org. Drop any stale/foreign id so the create
		// path runs instead of updating a row this org cannot see.
		if providerID != "" {
			logger.Info("stored provider id is not visible in the acting org, recreating",
				"staleProviderId", providerID, "code", tap.Spec.Code)
			providerID = ""
		}
	case foundID != providerID:
		logger.Info("adopting existing Tap provider from the acting org",
			"id", foundID, "previousProviderId", providerID)
		providerID = foundID
	}

	if providerID != "" {
		name := tap.Spec.Name
		redirect := tap.Spec.SuccessRedirectUrl
		updated, err := svc.UpdateTap(grpcCtx, &paymentproviderspb.UpdateTapRequest{
			Id:                 providerID,
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
		r.cleanupPlatformTwin(ctx, svc, orc, code, tap.Status.ProviderID)
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
	r.cleanupPlatformTwin(ctx, svc, orc, code, tap.Status.ProviderID)
	return SuccessResult(&tap), nil
}

// cleanupPlatformTwin is the invora-backend#209 migration: releases <= 1.3.2
// asserted the acting org with the trusted-internal x-invora-org-id header,
// which the WebGateway strips and rewrites to the token's HOME (platform) org,
// so every tenant CR's CreateTap landed its row - api key and all - in the
// platform org (dev: 73ce31ed-... for code tap_bayader). Those twins are
// invisible to the tenant flow but still poison the platform org: with more
// than one provider row and no code, PaymentProviders::FindService fails with
// payment_provider_code_missing (find_service.rb:26-31).
//
// The probe is deliberately STATELESS - re-derived from live state on every
// successful reconcile rather than from the stale status.providerId, which the
// adoption path above has already overwritten by the time we run. That makes it
// idempotent (once the twin is gone the home-org lookup returns NotFound and
// this is a no-op) and self-healing for stg/prod twins that may be minted
// before their held CRs land on a fixed controller.
//
// It is also strictly BEST-EFFORT: the tenant row is already written and
// status already says Synced=True, so a failed probe or delete only logs and
// leaves retry to the next reconcile. Cleanup must never un-sync a CR whose
// desired state is correct.
//
// Guards, in order:
//   - tenant CRs only (orgID != ""): for the platform's own CR the acting org
//     IS the home org - the row just written is the "twin" by id, and there is
//     nothing stranded by construction.
//   - Tap rows only: a non-Tap provider owning the code in the home org was
//     never minted by this controller.
//   - never the acting org's row: if the home-scoped lookup resolves to the id
//     we just wrote (org assertion ineffective, or aliasing), deleting it would
//     destroy the tenant's live provider. The billing-side delete is a
//     soft-delete (PaymentProviders::DestroyService discard!), but relying on
//     that would still break checkout until restored.
func (r *InvoraBillingTapProviderReconciler) cleanupPlatformTwin(
	ctx context.Context,
	svc paymentproviderspb.PaymentProvidersServiceClient,
	orc *orgResourceContext,
	code, actingProviderID string,
) {
	logger := log.FromContext(ctx)
	if orc.orgID == "" || actingProviderID == "" {
		return
	}
	homeCtx := orc.HomeGrpcCtx(ctx)
	resp, err := svc.Get(homeCtx, &paymentproviderspb.GetRequest{Code: &code})
	switch {
	case isGrpcNotFound(err):
		return
	case err != nil:
		logger.Info("platform-twin probe failed; cleanup retries on a later reconcile",
			"code", code, "error", err.Error())
		return
	}
	twin := resp.GetPaymentProvider().GetTapProvider()
	if twin == nil {
		return
	}
	if twin.GetId() == "" || twin.GetId() == actingProviderID {
		return
	}
	if _, err := svc.Delete(homeCtx, &paymentproviderspb.DeleteRequest{Id: twin.GetId()}); err != nil {
		logger.Info("platform-twin delete failed; cleanup retries on a later reconcile",
			"twinId", twin.GetId(), "code", code, "error", err.Error())
		return
	}
	logger.Info("deleted platform-org twin provider stranded by the pre-1.3.3 header rewrite",
		"twinId", twin.GetId(), "code", code, "actingProviderId", actingProviderID)
}

func (r *InvoraBillingTapProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingTapProvider{}).
		Named("tapprovider").
		Complete(r)
}
