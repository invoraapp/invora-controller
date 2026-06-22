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
	adminpb "github.com/invoraapp/invora-controller/gen/invora/admin/billing/v2"
	customerspb "github.com/invoraapp/invora-controller/gen/invora/billing/customers/v2"
)

type InvoraBillingOrganizationReconciler struct {
	BaseReconciler
}

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingorganizations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingorganizations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingorganizations/finalizers,verbs=update
// +kubebuilder:rbac:groups=billing.invora.app,resources=billinginstances,verbs=get;list;watch
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

	// Resolve instance -> super-admin admin client + gRPC connection
	instCtx, err := r.ResolveInstanceAdmin(ctx, org.Spec.InstanceRef, org.Namespace)
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

		return r.updateOrgStatusSuccess(ctx, &org, "Imported")
	}

	// If we already have an org ID, verify it still exists
	if org.Status.OrganizationID != "" {
		logger.V(1).Info("checking existing organization", "id", org.Status.OrganizationID)
		return r.updateOrgStatusSuccess(ctx, &org, "InSync")
	}

	// No org ID -> create via billing admin gRPC API
	logger.Info("creating organization in billing", "name", org.Spec.Name)

	adminSvc := adminpb.NewBillingOrgAdminServiceClient(instCtx.Conn())

	// Search for existing org by tenant_id (K8s resource name)
	listResp, err := adminSvc.ListOrgs(instCtx.GrpcCtx(ctx), &adminpb.ListOrgsRequest{})
	if err != nil {
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "SearchFailed", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, &org)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	for _, existing := range listResp.GetItems() {
		if existing.GetTenantId() == org.Name {
			logger.Info("found existing org, adopting", "id", existing.GetOrganizationId())
			org.Status.OrganizationID = existing.GetOrganizationId()
			return r.updateOrgStatusSuccess(ctx, &org, "Adopted")
		}
	}

	// Create new org
	provisionResp, err := adminSvc.ProvisionOrg(instCtx.GrpcCtx(ctx), &adminpb.ProvisionOrgRequest{
		TenantId:    org.Name,
		DisplayName: org.Spec.Name,
	})
	if err != nil {
		SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionSynced,
			metav1.ConditionFalse, "CreateFailed", err.Error(), org.Generation)
		_ = r.Status().Update(ctx, &org)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	org.Status.OrganizationID = provisionResp.GetOrg().GetOrganizationId()

	return r.updateOrgStatusSuccess(ctx, &org, "Created")
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
	parentOrc, result := r.resolveOrgDependencies(ctx, parentRef, org, &org.Status.Conditions, org.Generation)
	if result != nil {
		return fmt.Errorf("resolving parent org %s/%s: dependencies not ready", parentRef.Namespace, parentRef.Name)
	}

	externalID := orgExternalID(org)
	name := org.Spec.Name
	logger.V(1).Info("upserting tenant customer in parent org",
		"parent", parentRef.Name, "externalId", externalID)
	svc := customerspb.NewCustomersServiceClient(parentOrc.Conn())
	created, err := svc.Create(parentOrc.GrpcCtx(ctx), &customerspb.CreateRequest{
		ExternalId: externalID,
		Name:       &name,
	})
	if err != nil {
		return fmt.Errorf("upserting customer in parent org: %w", err)
	}

	// Snapshot ref into status. Take a copy so the spec pointer isn't shared.
	refCopy := parentRef
	org.Status.ParentOrgRef = &refCopy
	custID := created.GetCustomer().GetId()
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
	ns := parentRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	dummy := &billingv1alpha1.InvoraBillingOrganization{}
	dummy.SetNamespace(ns)
	dummy.SetName(parentRef.Name)
	var conds []metav1.Condition
	parentOrc, result := r.resolveOrgDependencies(ctx, parentRef, dummy, &conds, 0)
	if result != nil {
		return fmt.Errorf("resolving parent org %s/%s: not ready", ns, parentRef.Name)
	}
	svc := customerspb.NewCustomersServiceClient(parentOrc.Conn())
	_, err := svc.Delete(parentOrc.GrpcCtx(ctx), &customerspb.DeleteRequest{
		Id: externalID,
	})
	if err != nil {
		if isGrpcNotFound(err) {
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

			instCtx, err := r.ResolveInstanceAdmin(ctx, org.Spec.InstanceRef, org.Namespace)
			if err != nil {
				logger.Error(err, "cannot resolve instance for deletion, will retry")
				SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDeletionBlocked,
					metav1.ConditionTrue, "InstanceUnavailable", err.Error(), org.Generation)
				_ = r.Status().Update(ctx, org)
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}

			delSvc := adminpb.NewBillingOrgAdminServiceClient(instCtx.Conn())
			_, err = delSvc.DeprovisionOrg(instCtx.GrpcCtx(ctx), &adminpb.DeprovisionOrgRequest{
				TenantId: org.Name,
			})
			if err != nil && !isGrpcNotFound(err) {
				SetCondition(&org.Status.Conditions, billingv1alpha1.ConditionDeletionBlocked,
					metav1.ConditionTrue, "DeleteFailed", err.Error(), org.Generation)
				_ = r.Status().Update(ctx, org)
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}
		}
	}

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
