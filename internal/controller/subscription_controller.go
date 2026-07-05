package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	customerspb "github.com/invoraapp/invora-controller/gen/invora/billing/customers/v2"
	subscriptionspb "github.com/invoraapp/invora-controller/gen/invora/billing/subscriptions/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingSubscriptionReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingsubscriptions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingsubscriptions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingsubscriptions/finalizers,verbs=update
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers,verbs=get;list;watch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingplans,verbs=get;list;watch

func (r *InvoraBillingSubscriptionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var sub billingv1alpha1.InvoraBillingSubscription
	if err := r.Get(ctx, req.NamespacedName, &sub); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sub.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &sub,
			sub.Spec.OrganizationRef, sub.Spec.DeletionPolicy,
			sub.Status.ExternalID, &sub.Status.Conditions, sub.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := subscriptionspb.NewSubscriptionsServiceClient(orc.Conn())
				_, err := svc.Terminate(orc.GrpcCtx(ctx), &subscriptionspb.TerminateRequest{
					Id: sub.Status.ExternalID,
				})
				return err
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &sub); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, sub.Spec.OrganizationRef, &sub,
		&sub.Status.Conditions, sub.Generation)
	if result != nil {
		return *result, nil
	}

	customerExternalID, err := r.resolveCustomerExternalID(ctx, &sub)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "CustomerNotReady", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	planID, planCode, err := r.resolvePlanID(ctx, &sub)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "PlanNotReady", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	sub.Status.ResolvedCustomerExternalID = customerExternalID
	sub.Status.ResolvedPlanCode = planCode

	if importID := GetImportID(&sub); importID != "" {
		sub.Status.ExternalID = importID
		sub.Status.ID = importID
		annotations := sub.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		sub.SetAnnotations(annotations)
		_ = r.Update(ctx, &sub)
		setSuccessStatus(&sub.Status.Conditions, &sub.Status.LastSyncedAt, &sub.Status.ObservedGeneration, sub.Generation, "Imported")
		_ = r.Status().Update(ctx, &sub)
		return SuccessResult(&sub), nil
	}

	svc := subscriptionspb.NewSubscriptionsServiceClient(orc.Conn())
	custSvc := customerspb.NewCustomersServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	customerID, err := resolveBillingCustomerID(grpcCtx, custSvc, customerExternalID)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "CustomerResolveFailed", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: DependencyRequeueInterval}, nil
	}

	if sub.Status.ExternalID != "" {
		var getReq *subscriptionspb.GetRequest
		if sub.Spec.ExternalID != "" {
			extID := sub.Spec.ExternalID
			getReq = &subscriptionspb.GetRequest{ExternalId: &extID}
		} else {
			getReq = &subscriptionspb.GetRequest{Id: sub.Status.ExternalID}
		}
		remote, err := svc.Get(grpcCtx, getReq)
		if err != nil {
			if isGrpcNotFound(err) {
				sub.Status.ExternalID = ""
			} else {
				SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), sub.Generation)
				_ = r.Status().Update(ctx, &sub)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if sub.Status.ExternalID != "" && remote.GetSubscription() != nil {
			sub.Status.SubscriptionStatus = remote.GetSubscription().GetStatus().String()
			name := sub.Spec.Name
			_, err := svc.Update(grpcCtx, &subscriptionspb.UpdateRequest{
				Id:   sub.Status.ExternalID,
				Name: &name,
			})
			if err != nil {
				SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), sub.Generation)
				_ = r.Status().Update(ctx, &sub)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			if err := r.reconcileEntitlements(grpcCtx, svc, &sub); err != nil {
				SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "EntitlementSyncFailed", err.Error(), sub.Generation)
				_ = r.Status().Update(ctx, &sub)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&sub.Status.Conditions, &sub.Status.LastSyncedAt, &sub.Status.ObservedGeneration, sub.Generation, "InSync")
			_ = r.Status().Update(ctx, &sub)
			return SuccessResult(&sub), nil
		}
	}

	logger.Info("creating subscription", "externalId", sub.Spec.ExternalID)
	createReq := &subscriptionspb.CreateRequest{
		CustomerId:  customerID,
		PlanId:      planID,
		BillingTime: convert.BillingTime(sub.Spec.BillingTime),
	}
	if sub.Spec.ExternalID != "" {
		createReq.ExternalId = &sub.Spec.ExternalID
	}
	if sub.Spec.Name != "" {
		createReq.Name = &sub.Spec.Name
	}
	created, err := svc.Create(grpcCtx, createReq)
	if err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	sub.Status.ExternalID = created.GetSubscription().GetId()
	sub.Status.ID = created.GetSubscription().GetId()
	sub.Status.SubscriptionStatus = created.GetSubscription().GetStatus().String()
	if err := r.reconcileEntitlements(grpcCtx, svc, &sub); err != nil {
		SetCondition(&sub.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "EntitlementSyncFailed", err.Error(), sub.Generation)
		_ = r.Status().Update(ctx, &sub)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	setSuccessStatus(&sub.Status.Conditions, &sub.Status.LastSyncedAt, &sub.Status.ObservedGeneration, sub.Generation, "Created")
	if err := r.Status().Update(ctx, &sub); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&sub), nil
}

// reconcileEntitlements declaratively syncs sub.Spec.Entitlements against the
// billing backend: every declared entitlement is created/updated, then any
// entitlement this CR previously applied (tracked in
// Status.ManagedEntitlementCodes) but has since dropped from spec is removed.
//
// Entitlements never declared by this CR are never touched, even if spec.Entitlements
// is nil/empty — e.g. a connected_business grant applied directly against the
// billing backend, out-of-band from this CR, must survive every reconcile.
func (r *InvoraBillingSubscriptionReconciler) reconcileEntitlements(
	grpcCtx context.Context,
	svc subscriptionspb.SubscriptionsServiceClient,
	sub *billingv1alpha1.InvoraBillingSubscription,
) error {
	desired := make(map[string]billingv1alpha1.SubscriptionEntitlement, len(sub.Spec.Entitlements))
	for _, e := range sub.Spec.Entitlements {
		desired[e.FeatureCode] = e
	}

	for _, e := range sub.Spec.Entitlements {
		privileges := make([]*commonpb.EntitlementPrivilegeInput, len(e.Privileges))
		for i, p := range e.Privileges {
			privileges[i] = &commonpb.EntitlementPrivilegeInput{
				PrivilegeCode: p.PrivilegeCode,
				Value:         p.Value,
			}
		}
		_, err := svc.CreateOrUpdateEntitlement(grpcCtx, &subscriptionspb.CreateOrUpdateEntitlementRequest{
			SubscriptionId: sub.Status.ExternalID,
			Entitlement: &commonpb.EntitlementInput{
				FeatureCode: e.FeatureCode,
				Privileges:  privileges,
			},
		})
		if err != nil {
			return fmt.Errorf("upserting entitlement %q: %w", e.FeatureCode, err)
		}
	}

	// Guarded prune: only remove a remote entitlement if THIS CR previously
	// applied it (tracked in status) and it has since been dropped from spec.
	// Entitlements this CR never tracked as managed are left alone regardless
	// of what spec.Entitlements currently contains — they were granted
	// out-of-band and this controller must never delete them.
	previouslyManaged := make(map[string]bool, len(sub.Status.ManagedEntitlementCodes))
	for _, code := range sub.Status.ManagedEntitlementCodes {
		previouslyManaged[code] = true
	}

	if len(previouslyManaged) > 0 {
		listResp, err := svc.ListEntitlements(grpcCtx, &subscriptionspb.ListEntitlementsRequest{
			SubscriptionId: sub.Status.ExternalID,
		})
		if err != nil {
			return fmt.Errorf("listing entitlements: %w", err)
		}
		for _, remote := range listResp.GetItems() {
			code := remote.GetCode()
			if _, stillDesired := desired[code]; stillDesired {
				continue
			}
			if !previouslyManaged[code] {
				continue // not managed by this CR — never touch (out-of-band grant)
			}
			if _, err := svc.RemoveEntitlement(grpcCtx, &subscriptionspb.RemoveEntitlementRequest{
				SubscriptionId: sub.Status.ExternalID,
				FeatureCode:    code,
			}); err != nil && !isGrpcNotFound(err) {
				return fmt.Errorf("removing entitlement %q: %w", code, err)
			}
		}
	}

	managedCodes := make([]string, 0, len(desired))
	for code := range desired {
		managedCodes = append(managedCodes, code)
	}
	sort.Strings(managedCodes)
	sub.Status.ManagedEntitlementCodes = managedCodes

	return nil
}

func resolveBillingCustomerID(ctx context.Context, svc customerspb.CustomersServiceClient, externalID string) (string, error) {
	resp, err := svc.Get(ctx, &customerspb.GetRequest{ExternalId: &externalID})
	if err != nil {
		return "", fmt.Errorf("getting customer %q: %w", externalID, err)
	}
	if resp.GetCustomer() == nil || resp.GetCustomer().GetId() == "" {
		return "", fmt.Errorf("customer %q has no billing id", externalID)
	}
	return resp.GetCustomer().GetId(), nil
}

func (r *InvoraBillingSubscriptionReconciler) resolveCustomerExternalID(ctx context.Context, sub *billingv1alpha1.InvoraBillingSubscription) (string, error) {
	if sub.Spec.ExternalCustomerID != "" {
		return sub.Spec.ExternalCustomerID, nil
	}
	if sub.Spec.CustomerRef == nil {
		return "", fmt.Errorf("either externalCustomerId or customerRef must be set")
	}

	ns := sub.Spec.CustomerRef.Namespace
	if ns == "" {
		ns = sub.Namespace
	}
	var customer billingv1alpha1.InvoraBillingCustomer
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: sub.Spec.CustomerRef.Name}, &customer); err != nil {
		return "", fmt.Errorf("getting InvoraBillingCustomer %s/%s: %w", ns, sub.Spec.CustomerRef.Name, err)
	}
	return customer.Spec.ExternalID, nil
}

func (r *InvoraBillingSubscriptionReconciler) resolvePlanID(ctx context.Context, sub *billingv1alpha1.InvoraBillingSubscription) (planID, planCode string, err error) {
	if sub.Spec.PlanRef != nil {
		ns := sub.Spec.PlanRef.Namespace
		if ns == "" {
			ns = sub.Namespace
		}
		var plan billingv1alpha1.InvoraBillingPlan
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: sub.Spec.PlanRef.Name}, &plan); err != nil {
			return "", "", fmt.Errorf("getting InvoraBillingPlan %s/%s: %w", ns, sub.Spec.PlanRef.Name, err)
		}
		if plan.Status.ExternalID == "" {
			return "", plan.Spec.Code, fmt.Errorf("InvoraBillingPlan %s/%s has no billing plan id yet", ns, sub.Spec.PlanRef.Name)
		}
		return plan.Status.ExternalID, plan.Spec.Code, nil
	}
	if sub.Spec.PlanCode != "" {
		var plans billingv1alpha1.InvoraBillingPlanList
		if err := r.List(ctx, &plans, client.InNamespace(sub.Namespace)); err != nil {
			return "", "", fmt.Errorf("listing InvoraBillingPlan in %s: %w", sub.Namespace, err)
		}
		for i := range plans.Items {
			p := &plans.Items[i]
			if p.Spec.Code == sub.Spec.PlanCode && p.Status.ExternalID != "" {
				return p.Status.ExternalID, p.Spec.Code, nil
			}
		}
		return "", sub.Spec.PlanCode, fmt.Errorf("no synced InvoraBillingPlan with code %q in namespace %s", sub.Spec.PlanCode, sub.Namespace)
	}
	return "", "", fmt.Errorf("either planCode or planRef must be set")
}

func (r *InvoraBillingSubscriptionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingSubscription{}).Named("subscription").Complete(r)
}
