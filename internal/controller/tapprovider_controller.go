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
		// billing does not currently expose a Tap destroy mutation, so the
		// best we can do is drop the finalizer (Orphan-style) regardless
		// of policy. Future support: when upstream adds a destroy
		// mutation, branch on DeletionPolicy here.
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

	// Resolve the Tap API key from the referenced Secret.
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

	// Adopt by code if no ProviderID stored yet.
	if tap.Status.ProviderID == "" {
		existing, err := orc.billingClient.FindTapPaymentProviderByCode(ctx, tap.Spec.Code)
		if err != nil {
			SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "LookupFailed", err.Error(), tap.Generation)
			_ = r.Status().Update(ctx, &tap)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if existing != nil {
			logger.Info("adopting existing Tap provider", "id", existing.ID)
			tap.Status.ProviderID = existing.ID
		}
	}

	// Update path.
	if tap.Status.ProviderID != "" {
		updated, err := orc.billingClient.UpdateTapPaymentProvider(ctx, billingclient.UpdateTapPaymentProviderInput{
			ID:                 tap.Status.ProviderID,
			Code:               tap.Spec.Code,
			Name:               tap.Spec.Name,
			SuccessRedirectURL: tap.Spec.SuccessRedirectUrl,
		})
		if err != nil {
			SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "UpdateFailed", err.Error(), tap.Generation)
			_ = r.Status().Update(ctx, &tap)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		tap.Status.ProviderID = updated.ID
		tap.Status.ProviderCode = updated.Code
		tap.Status.ID = updated.ID
		setSuccessStatus(&tap.Status.Conditions, &tap.Status.LastSyncedAt,
			&tap.Status.ObservedGeneration, tap.Generation, "InSync")
		if err := r.Status().Update(ctx, &tap); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return SuccessResult(&tap), nil
	}

	// Create path.
	logger.Info("adding Tap payment provider", "code", tap.Spec.Code)
	created, err := orc.billingClient.AddTapPaymentProvider(ctx, billingclient.AddTapPaymentProviderInput{
		Code:               tap.Spec.Code,
		Name:               tap.Spec.Name,
		APIKey:             apiKey,
		SuccessRedirectURL: tap.Spec.SuccessRedirectUrl,
	})
	if err != nil {
		SetCondition(&tap.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), tap.Generation)
		_ = r.Status().Update(ctx, &tap)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	tap.Status.ProviderID = created.ID
	tap.Status.ProviderCode = created.Code
	tap.Status.ID = created.ID
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
