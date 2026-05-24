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

type InvoraBillingEntityReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingbillingentities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingbillingentities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingbillingentities/finalizers,verbs=update

func (r *InvoraBillingEntityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var be billingv1alpha1.InvoraBillingEntity
	if err := r.Get(ctx, req.NamespacedName, &be); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !be.DeletionTimestamp.IsZero() {
		// Upstream billing billing-entity destroy mutation is a no-op stub
		// that returns the default billing entity without deleting
		// anything. Drop the finalizer so the CR can be garbage-collected
		// regardless of DeletionPolicy.
		logger.Info("removing finalizer (billing does not allow billing-entity destruction)",
			"code", be.Spec.Code)
		if err := r.RemoveFinalizer(ctx, &be); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if added, err := r.EnsureFinalizer(ctx, &be); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, be.Spec.InvoraBillingOrganizationRef, &be,
		&be.Status.Conditions, be.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&be); importID != "" {
		be.Status.InvoraBillingEntityID = importID
		be.Status.ID = importID
		annotations := be.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		be.SetAnnotations(annotations)
		_ = r.Update(ctx, &be)
		setSuccessStatus(&be.Status.Conditions, &be.Status.LastSyncedAt,
			&be.Status.ObservedGeneration, be.Generation, "Imported")
		_ = r.Status().Update(ctx, &be)
		return SuccessResult(&be), nil
	}

	apiInput := buildBillingEntityInput(&be.Spec)

	// Adopt by code when no billing ID is stored yet.
	if be.Status.InvoraBillingEntityID == "" {
		existing, err := orc.billingClient.FindBillingEntityByCode(ctx, be.Spec.Code)
		if err != nil {
			SetCondition(&be.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "LookupFailed", err.Error(), be.Generation)
			_ = r.Status().Update(ctx, &be)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if existing != nil {
			logger.Info("adopting existing billing entity", "id", existing.ID)
			be.Status.InvoraBillingEntityID = existing.ID
		}
	}

	// Update path.
	if be.Status.InvoraBillingEntityID != "" {
		updated, err := orc.billingClient.UpdateBillingEntity(ctx, be.Status.InvoraBillingEntityID, apiInput)
		if err != nil {
			SetCondition(&be.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "UpdateFailed", err.Error(), be.Generation)
			_ = r.Status().Update(ctx, &be)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		be.Status.InvoraBillingEntityID = updated.ID
		be.Status.ID = updated.ID
		setSuccessStatus(&be.Status.Conditions, &be.Status.LastSyncedAt,
			&be.Status.ObservedGeneration, be.Generation, "InSync")
		if err := r.Status().Update(ctx, &be); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return SuccessResult(&be), nil
	}

	// Create path.
	logger.Info("creating billing entity", "code", be.Spec.Code)
	created, err := orc.billingClient.CreateBillingEntity(ctx, apiInput)
	if err != nil {
		SetCondition(&be.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), be.Generation)
		_ = r.Status().Update(ctx, &be)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	be.Status.InvoraBillingEntityID = created.ID
	be.Status.ID = created.ID
	setSuccessStatus(&be.Status.Conditions, &be.Status.LastSyncedAt,
		&be.Status.ObservedGeneration, be.Generation, "Created")
	if err := r.Status().Update(ctx, &be); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&be), nil
}

// buildBillingEntityInput translates a CRD spec into a billingclient input.
func buildBillingEntityInput(spec *billingv1alpha1.InvoraBillingEntitySpec) billingclient.BillingEntityInput {
	in := billingclient.BillingEntityInput{
		Code:                      spec.Code,
		Name:                      spec.Name,
		DefaultCurrency:           spec.DefaultCurrency,
		Email:                     spec.Email,
		LegalName:                 spec.LegalName,
		LegalNumber:               spec.LegalNumber,
		TaxIdentificationNumber:   spec.TaxIdentificationNumber,
		AddressLine1:              spec.AddressLine1,
		AddressLine2:              spec.AddressLine2,
		City:                      spec.City,
		State:                     spec.State,
		Country:                   spec.Country,
		Zipcode:                   spec.Zipcode,
		Timezone:                  spec.Timezone,
		DocumentNumberPrefix:      spec.DocumentNumberPrefix,
		DocumentNumbering:         spec.DocumentNumbering,
		EuTaxManagement:           spec.EuTaxManagement,
		Einvoicing:                spec.Einvoicing,
		FinalizeZeroAmountInvoice: spec.FinalizeZeroAmountInvoice,
	}
	if spec.NetPaymentTerm > 0 {
		v := spec.NetPaymentTerm
		in.NetPaymentTerm = &v
	}
	if spec.EmailSettings != nil {
		var settings []string
		if spec.EmailSettings.InvoiceFinalized {
			settings = append(settings, "invoice.finalized")
		}
		if spec.EmailSettings.CreditNoteCreated {
			settings = append(settings, "credit_note.created")
		}
		if spec.EmailSettings.PaymentReceiptCreated {
			settings = append(settings, "payment_receipt.created")
		}
		// Send empty array explicitly so users can disable all email
		// notifications by setting all booleans to false.
		if settings == nil {
			settings = []string{}
		}
		in.EmailSettings = settings
	}
	return in
}

func (r *InvoraBillingEntityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingEntity{}).
		Named("billingentity").
		Complete(r)
}
