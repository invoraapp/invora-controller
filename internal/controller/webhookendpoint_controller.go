package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	webhookspb "github.com/invoraapp/invora-controller/gen/invora/billing/webhooks/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingWebhookEndpointReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingwebhookendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingwebhookendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingwebhookendpoints/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *InvoraBillingWebhookEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var wh billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(ctx, req.NamespacedName, &wh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Snapshot the status so the terminal writes can be skipped when they would
	// be a no-op — see StatusUnchanged (invora/devops#68).
	statusSnapshot := *wh.Status.BillingResourceStatus.DeepCopy()
	externalIDBefore := wh.Status.ExternalID

	if !wh.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &wh,
			wh.Spec.OrganizationRef, wh.Spec.DeletionPolicy,
			wh.Status.ExternalID, &wh.Status.Conditions, wh.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				if wh.Status.ExternalID == "" {
					return nil
				}
				svc := webhookspb.NewWebhookEndpointsServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &webhookspb.DeleteRequest{
					Id: wh.Status.ExternalID,
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

	svc := webhookspb.NewWebhookEndpointsServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if wh.Status.ExternalID != "" {
		getResp, err := svc.Get(grpcCtx, &webhookspb.GetRequest{Id: wh.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				wh.Status.ExternalID = ""
			} else {
				SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), wh.Generation)
				_ = r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if wh.Status.ExternalID != "" {
			// invora/devops#68 — change detection: skip the Update RPC when the
			// live endpoint already matches spec AND the CR's generation is
			// already observed. Generation is load-bearing here, not belt-and-
			// braces: BillingWebhookEndpoint does NOT echo the signature algo,
			// so a signatureAlgo-only spec edit is invisible in the live
			// representation and generation is its only signal.
			if wh.Status.ObservedGeneration != wh.Generation ||
				getResp.GetWebhookEndpoint().GetWebhookUrl() != wh.Spec.WebhookURL {
				_, err := svc.Update(grpcCtx, buildWebhookUpdateRequest(&wh))
				if err != nil {
					SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), wh.Generation)
					_ = r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
				}
			}
			setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "InSync")
			_ = r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID)
			return SuccessResult(&wh), nil
		}
	}

	// invora/devops#109 — the adopt-by-URL probe FAILS CLOSED. This used to
	// read `if err == nil { ... }`, so a transient failure of the probe
	// (deadline, transient 5xx, auth blip) was swallowed and execution fell
	// through to Create: the idempotent adopt-by-URL path became an
	// unconditional create, and one bayader CR accumulated 9 live endpoint
	// records over 11 days while reporting Ready=True/InSync.
	listResp, err := svc.List(grpcCtx, &webhookspb.ListRequest{})
	if err != nil {
		logger.Error(err, "adopt-by-URL probe failed; not creating", "url", wh.Spec.WebhookURL)
		SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse,
			"AdoptProbeFailed",
			fmt.Sprintf("listing existing webhook endpoints to adopt %q failed; refusing to Create to avoid a duplicate: %v", wh.Spec.WebhookURL, err),
			wh.Generation)
		_ = r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	var matches []*commonpb.BillingWebhookEndpoint
	for _, e := range listResp.GetItems() {
		if e.GetWebhookUrl() == wh.Spec.WebhookURL {
			matches = append(matches, e)
		}
	}
	if len(matches) > 1 {
		// invora/devops#109 suggestion 4 — surface it. One CR is contractually
		// 1:1 with one endpoint; more than one match means a previous
		// fail-open (or an out-of-band create) already duplicated it, and the
		// CR's own status cannot show that. Converging DOWN by deleting the
		// siblings is deliberately NOT done here: that is a destructive remote
		// write and the issue requires the existing dev records be cleaned up
		// as a separate, gated operation.
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.GetId())
		}
		logger.Info("adopt probe found multiple endpoints with the same URL",
			"url", wh.Spec.WebhookURL, "count", len(matches), "ids", ids)
		r.eventf(&wh, corev1.EventTypeWarning, "DuplicateWebhookEndpoints",
			"%d webhook endpoints share url %q (%s); this CR adopts only the first — the others must be removed through the billing API",
			len(matches), wh.Spec.WebhookURL, strings.Join(ids, ", "))
	}
	if len(matches) > 0 {
		e := matches[0]
		logger.Info("found existing webhook by URL, adopting", "externalId", e.GetId())
		wh.Status.ExternalID = e.GetId()
		wh.Status.ID = e.GetId()
		setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Adopted")
		_ = r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID)
		return SuccessResult(&wh), nil
	}

	logger.Info("creating webhook endpoint", "url", wh.Spec.WebhookURL)
	created, createErr := svc.Create(grpcCtx, buildWebhookCreateRequest(&wh))
	if createErr != nil {
		SetCondition(&wh.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", createErr.Error(), wh.Generation)
		_ = r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	wh.Status.ExternalID = created.GetWebhookEndpoint().GetId()
	wh.Status.ID = created.GetWebhookEndpoint().GetId()
	setSuccessStatus(&wh.Status.Conditions, &wh.Status.LastSyncedAt, &wh.Status.ObservedGeneration, wh.Generation, "Created")
	if err := r.writeStatusIfChanged(ctx, &wh, &statusSnapshot, &wh.Status.BillingResourceStatus, externalIDBefore, wh.Status.ExternalID); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&wh), nil
}

func buildWebhookCreateRequest(wh *billingv1alpha1.InvoraBillingWebhookEndpoint) *webhookspb.CreateRequest {
	in := &webhookspb.CreateRequest{
		WebhookUrl: wh.Spec.WebhookURL,
	}
	if wh.Spec.SignatureAlgo != "" {
		algo := convert.WebhookSignatureAlgo(wh.Spec.SignatureAlgo)
		in.SignatureAlgo = &algo
	}
	return in
}

func buildWebhookUpdateRequest(wh *billingv1alpha1.InvoraBillingWebhookEndpoint) *webhookspb.UpdateRequest {
	in := &webhookspb.UpdateRequest{
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
