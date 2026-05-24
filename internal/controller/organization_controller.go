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

type InvoraBillingOrganizationReconciler struct {
	BaseReconciler
}

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingorganizations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingorganizations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingorganizations/finalizers,verbs=update
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billinginstances,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// parentDualWriteEnabled reports whether parent dual-write should be active
// for this org. True iff spec.parentOrgRef is set AND
// spec.enableParentDualWrite is nil-or-true (defaults to true per Phase 4).
func parentDualWriteEnabled(org *billingv1alpha1.InvoraBillingOrganization) bool {
	if org.Spec.ParentOrgRef == nil {
		return false
	}
	if org.Spec.EnableParentDualWrite != nil && !*org.Spec.EnableParentDualWrite {
		return false
	}
	return true
}

// orgExternalID returns the externalId used for the tenant's customer record
// in the parent org. Prefers spec.externalId; falls back to metadata.name.
func orgExternalID(org *billingv1alpha1.InvoraBillingOrganization) string {
	if org.Spec.ExternalID != "" {
		return org.Spec.ExternalID
	}
	return org.Name
}

func (r *InvoraBillingOrganizationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var org billingv1alpha1.InvoraBillingOrganization
	if err := r.Get(ctx, req.NamespacedName, &org); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !org.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &org)
	}

	added, err := r.EnsureFinalizer(ctx, &org)
	if err != nil {
		return ctrl.Result{}, err
	}
	if added {
		return ctrl.Result{Requeue: true}, nil
	}

	// Resolve instance -> super-admin client
	client, _, err := r.ResolveInstance(ctx, org.Spec.InstanceRef, org.Namespace)
	if err != nil {
		logger.Error(err, "failed to resolve instanceRef")
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "InstanceNotReady", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, &org)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
		metav1.ConditionTrue, "InstanceReady", "InvoraBillingInstance is available", org.Generation)

	// Check import-id annotation
	if importID := GetImportID(&org); importID != "" {
		logger.Info("adopting existing organization by import-id", "importID", importID)
		org.Status.OrganizationID = importID

		annotations := org.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		org.SetAnnotations(annotations)
		if err := r.Update(ctx, &org); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing import-id annotation: %w", err)
		}

		// Ensure API key is written
		if err := r.ensureApiKey(ctx, client, &org); err != nil {
			SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "ApiKeyFailed", err.Error(), org.Generation)
			_ = r.Status().Update(ctx, &org)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		return r.updateOrgStatusSuccess(ctx, &org, "Imported")
	}

	// If we already have an org ID, verify it still exists
	if org.Status.OrganizationID != "" {
		logger.V(1).Info("checking existing organization", "id", org.Status.OrganizationID)

		// Ensure API key Secret still exists
		if err := r.ensureApiKey(ctx, client, &org); err != nil {
			SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
				metav1.ConditionFalse, "ApiKeyFailed", err.Error(), org.Generation)
			_ = r.Status().Update(ctx, &org)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		return r.updateOrgStatusSuccess(ctx, &org, "InSync")
	}

	// No org ID -> create via GraphQL
	logger.Info("creating organization in billing", "name", org.Spec.Name)

	// Search for existing org by name
	orgs, err := client.ListOrganizations(ctx)
	if err != nil {
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "SearchFailed", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, &org)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	for _, existing := range orgs {
		if existing.Name == org.Spec.Name {
			logger.Info("found existing org, adopting", "id", existing.ID)
			org.Status.OrganizationID = existing.ID

			if err := r.ensureApiKey(ctx, client, &org); err != nil {
				SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
					metav1.ConditionFalse, "ApiKeyFailed", err.Error(), org.Generation)
				_ = r.Status().Update(ctx, &org)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}

			return r.updateOrgStatusSuccess(ctx, &org, "Adopted")
		}
	}

	// Create new org
	created, err := client.CreateOrganization(ctx, billingclient.CreateOrganizationInput{
		Name:              org.Spec.Name,
		Email:             org.Spec.Email,
		Timezone:          org.Spec.Timezone,
		DocumentNumbering: org.Spec.DocumentNumbering,
	})
	if err != nil {
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, &org)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	org.Status.OrganizationID = created.ID

	// Generate and store API key
	if err := r.ensureApiKey(ctx, client, &org); err != nil {
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "ApiKeyFailed", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, &org)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return r.updateOrgStatusSuccess(ctx, &org, "Created")
}

// ensureApiKey ensures the org's API key is written to the Secret.
// If the org has no apiKeyId yet, it fetches the key list and regenerates.
func (r *InvoraBillingOrganizationReconciler) ensureApiKey(
	ctx context.Context,
	superAdminClient *billingclient.Client,
	org *billingv1alpha1.InvoraBillingOrganization,
) error {
	logger := log.FromContext(ctx)

	// Check if Secret already has a valid key
	secretRef := org.Spec.WriteSecretToRef
	secretNS := secretRef.Namespace
	if secretNS == "" {
		secretNS = org.Namespace
	}
	// Pass ownerNamespace="" to bypass the cross-namespace guard: the org controller
	// owns WriteSecretToRef and may target a different namespace (e.g. invora-dev).
	// This is a trusted internal caller — no tenant-authored CR is involved here.
	existingKey, err := billingclient.ResolveSecretValue(ctx, r.Client, secretRef.Name, secretNS, "apiKey", "")
	if err == nil && existingKey != "" && org.Status.ApiKeyID != "" {
		// Secret exists and has a key, nothing to do
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionCredentialsWritten,
			metav1.ConditionTrue, "SecretExists", "API key Secret is up to date", org.Generation)
		return nil
	}

	// Need to generate/regenerate API key
	logger.Info("generating API key for organization", "orgID", org.Status.OrganizationID)

	// Get the org's API key IDs
	apiKeys, err := superAdminClient.GetOrganizationApiKeys(ctx, org.Status.OrganizationID)
	if err != nil {
		return fmt.Errorf("getting org API keys: %w", err)
	}
	if len(apiKeys) == 0 {
		return fmt.Errorf("organization %s has no API keys", org.Status.OrganizationID)
	}

	apiKeyID := apiKeys[0].ID
	org.Status.ApiKeyID = apiKeyID

	// Regenerate the key to get the plaintext value
	keyValue, err := superAdminClient.RegenerateOrganizationApiKey(ctx, org.Status.OrganizationID, apiKeyID)
	if err != nil {
		return fmt.Errorf("regenerating API key: %w", err)
	}

	// Write to Secret
	if err := r.WriteSecret(ctx, org, org.Spec.WriteSecretToRef, org.Namespace, map[string][]byte{
		"apiKey": []byte(keyValue),
	}); err != nil {
		return fmt.Errorf("writing API key Secret: %w", err)
	}

	// Invalidate any cached org client so it picks up the new key
	r.ClientCache.InvalidateOrg(org.Namespace, org.Name)

	SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionCredentialsWritten,
		metav1.ConditionTrue, "SecretWritten", "API key written to Secret", org.Generation)

	return nil
}

func (r *InvoraBillingOrganizationReconciler) updateOrgStatusSuccess(
	ctx context.Context,
	org *billingv1alpha1.InvoraBillingOrganization,
	reason string,
) (ctrl.Result, error) {
	// Reconcile parent dual-write before finalising status so any failure
	// surfaces as a non-Ready condition.
	if err := r.reconcileParentDualWrite(ctx, org); err != nil {
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "ParentDualWriteFailed", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, org)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
		metav1.ConditionTrue, reason, "Organization reconciled successfully", org.Generation)
	SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionReady,
		metav1.ConditionTrue, "Ready", "Organization reconciled successfully", org.Generation)
	now := metav1.Now()
	org.Status.LastSyncedAt = &now
	org.Status.ObservedGeneration = org.Generation
	org.Status.ID = org.Status.OrganizationID
	if err := r.Status().Update(ctx, org); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(org), nil
}

// reconcileParentDualWrite synchronises the tenant's representation as a
// customer in its parent org. It is idempotent and handles three transitions:
//
//   - parent ref unchanged → upsert customer
//   - parent ref changed (incl. previously dual-written) → delete from old
//     parent then upsert into new
//   - parent ref cleared / dual-write disabled → delete from previous parent
//
// The function mutates org.Status (parentOrgRef + parentCustomerId) but does
// not call r.Status().Update — the caller writes the final status object.
func (r *InvoraBillingOrganizationReconciler) reconcileParentDualWrite(
	ctx context.Context,
	org *billingv1alpha1.InvoraBillingOrganization,
) error {
	logger := log.FromContext(ctx)

	desired := parentDualWriteEnabled(org)
	previous := org.Status.ParentOrgRef

	// Detect change of parent target so we can clean up the old one first.
	if previous != nil {
		change := !desired ||
			previous.Name != org.Spec.ParentOrgRef.Name ||
			previous.Namespace != org.Spec.ParentOrgRef.Namespace
		if change {
			if err := r.deleteParentCustomer(ctx, *previous, org.Namespace, orgExternalID(org)); err != nil {
				return fmt.Errorf("deleting customer from previous parent: %w", err)
			}
			org.Status.ParentOrgRef = nil
			org.Status.ParentCustomerID = nil
		}
	}

	if !desired {
		return nil
	}

	parentRef := *org.Spec.ParentOrgRef
	parentClient, parentOrg, err := r.ResolveOrganization(ctx, parentRef, org.Namespace)
	if err != nil {
		return fmt.Errorf("resolving parent org %s/%s: %w", parentRef.Namespace, parentRef.Name, err)
	}
	_ = parentOrg // reserved for future label propagation

	cust := billingclient.Customer{
		ExternalID: orgExternalID(org),
		Name:       org.Spec.Name,
	}
	logger.V(1).Info("upserting tenant customer in parent org",
		"parent", parentRef.Name, "externalId", cust.ID)
	created, err := parentClient.CreateOrUpdateCustomer(ctx, cust)
	if err != nil {
		return fmt.Errorf("upserting customer in parent org: %w", err)
	}

	// Snapshot ref into status. Take a copy so the spec pointer isn't shared.
	refCopy := parentRef
	org.Status.ParentOrgRef = &refCopy
	custID := created.ID
	org.Status.ParentCustomerID = &custID

	return nil
}

// deleteParentCustomer removes the tenant's customer record from a parent org.
// IsNotFound is swallowed — the customer may have been hand-removed in billing.
func (r *InvoraBillingOrganizationReconciler) deleteParentCustomer(
	ctx context.Context,
	parentRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
	externalID string,
) error {
	logger := log.FromContext(ctx)
	parentClient, _, err := r.ResolveOrganization(ctx, parentRef, defaultNamespace)
	if err != nil {
		// Cannot resolve parent during cleanup; treat as transient — caller
		// requeues. Return the error so dependency-not-ready bubbles up.
		return err
	}
	if err := parentClient.DeleteCustomer(ctx, externalID); err != nil {
		if billingclient.IsNotFound(err) {
			logger.V(1).Info("parent customer already absent", "externalId", externalID)
			return nil
		}
		return err
	}
	return nil
}

func (r *InvoraBillingOrganizationReconciler) handleDeletion(
	ctx context.Context,
	org *billingv1alpha1.InvoraBillingOrganization,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Cleanup parent dual-write entry first (best effort). Failure here
	// blocks finalizer removal so the tenant doesn't dangle in the parent.
	if org.Status.ParentOrgRef != nil {
		if err := r.deleteParentCustomer(ctx, *org.Status.ParentOrgRef, org.Namespace, orgExternalID(org)); err != nil {
			logger.Error(err, "deleting tenant customer from parent org during finalisation")
			SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDeletionBlocked,
				metav1.ConditionTrue, "ParentCustomerDeleteFailed", err.Error(), org.Generation)
			_ = r.Status().Update(ctx, org)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		org.Status.ParentOrgRef = nil
		org.Status.ParentCustomerID = nil
	}

	switch org.Spec.DeletionPolicy {
	case billingv1alpha1.DeletionPolicyOrphan:
		logger.Info("orphaning organization (deletionPolicy=Orphan)", "id", org.Status.OrganizationID)

	case billingv1alpha1.DeletionPolicyDelete, "":
		if org.Status.OrganizationID != "" {
			logger.Info("deleting organization from billing", "id", org.Status.OrganizationID)

			client, _, err := r.ResolveInstance(ctx, org.Spec.InstanceRef, org.Namespace)
			if err != nil {
				logger.Error(err, "cannot resolve instance for deletion, will retry")
				SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDeletionBlocked,
					metav1.ConditionTrue, "InstanceUnavailable", err.Error(), org.Generation)
				_ = r.Status().Update(ctx, org)
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}

			if err := client.DestroyOrganization(ctx, org.Status.OrganizationID); err != nil {
				SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDeletionBlocked,
					metav1.ConditionTrue, "DeleteFailed", err.Error(), org.Generation)
				_ = r.Status().Update(ctx, org)
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}
		}
	}

	r.ClientCache.InvalidateOrg(org.Namespace, org.Name)

	if err := r.RemoveFinalizer(ctx, org); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *InvoraBillingOrganizationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&billingv1alpha1.InvoraBillingOrganization{}).
		Named("organization").
		Complete(r)
}
