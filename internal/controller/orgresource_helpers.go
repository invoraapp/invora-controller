package controller

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
	"github.com/invoraapp/invora-controller/internal/gateway"
)

// orgResourceContext holds the resolved dependencies for an org-scoped resource.
type orgResourceContext struct {
	org   *billingv1alpha1.InvoraBillingOrganization
	conn  *grpc.ClientConn
	token string
	orgID string
}

// instanceAdminContext holds super-admin gateway access for billing instance operations.
type instanceAdminContext struct {
	instance *billingv1alpha1.InvoraBillingInstance
	conn     *grpc.ClientConn
	token    string
}

// GrpcCtx returns a context with auth metadata for gRPC calls.
//
// The acting org is asserted via `x-zitadel-orgid` — the CLIENT-side header the
// WebGateway's OrgContextMiddleware validates (home org / per-org grant /
// trusted-resolver machine identity) and converts into the trusted internal
// `x-invora-org-id` header. Never stamp `x-invora-org-id` here: the gateway
// unconditionally STRIPS that inbound header (anti-spoofing — the gateway is
// its sole writer) and rewrites it to the token's HOME org, which silently
// re-scoped every org-scoped write to the controller PAT's home (admin) org —
// bayader's plans landed in the admin org's Lago bucket while tenant reads hit
// the (empty) correctly-keyed org. Requires the controller machine user's sub
// in Invora:Zitadel:ResolverMachineUserSubs, else the gateway 403s the
// assertion (fail-closed).
func (o *orgResourceContext) GrpcCtx(ctx context.Context) context.Context {
	pairs := []string{"authorization", "Bearer " + o.token}
	if o.orgID != "" {
		pairs = append(pairs, "x-zitadel-orgid", o.orgID)
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

// Conn returns the gRPC connection to the gateway.
func (o *orgResourceContext) Conn() *grpc.ClientConn { return o.conn }

// GrpcCtx returns a context with super-admin auth metadata for gRPC calls.
func (i *instanceAdminContext) GrpcCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+i.token)
}

// Conn returns the gRPC connection to the gateway.
func (i *instanceAdminContext) Conn() *grpc.ClientConn { return i.conn }

// dialGateway creates a gRPC connection to the given gateway URL.
func dialGateway(url string) (*grpc.ClientConn, error) {
	// gatewayURL is a URL (e.g. https://dev-gateway.invora.app); gateway.Dial
	// translates it to a proper gRPC "host:port" target + transport creds. Passing
	// the raw URL to grpc.NewClient leaves the channel stuck (context deadline
	// exceeded) because "https" is not a gRPC resolver scheme.
	return gateway.Dial(url)
}

// resolveOrgDependencies resolves the organization reference and sets
// appropriate conditions. Returns nil context if dependencies aren't ready.
func (r *BaseReconciler) resolveOrgDependencies(
	ctx context.Context,
	orgRef billingv1alpha1.ResourceRef,
	obj client.Object,
	conditions *[]metav1.Condition,
	generation int64,
) (*orgResourceContext, *ctrl.Result) {
	logger := log.FromContext(ctx)

	org, err := r.getReadyBillingOrganization(ctx, orgRef, obj.GetNamespace())
	if err != nil {
		logger.Error(err, "failed to resolve organizationRef")
		SetCondition(conditions, billingv1alpha1.ConditionDependencyReady,
			metav1.ConditionFalse, "OrganizationNotReady", err.Error(), generation)
		_ = r.Status().Update(ctx, obj)
		result := ctrl.Result{RequeueAfter: DependencyRequeueInterval}
		return nil, &result
	}

	SetCondition(conditions, billingv1alpha1.ConditionDependencyReady,
		metav1.ConditionTrue, "DependenciesReady", "All referenced resources are available", generation)

	// Org-scoped child resources authenticate with the instance's super-admin
	// token plus the x-zitadel-orgid acting-org header — there is no per-org
	// credential. The gateway validates the header (trusted-resolver machine
	// identity) and forwards the org as trusted context; the backend resolves
	// the tenant from it and uses its own backend-wide Lago machine credential
	// downstream. The org value is the tenant GUID from spec.externalId (==
	// Zitadel org id == ABP tenant id), NOT status.organizationId — that field
	// holds core's TenantBillingConfig.BillingOrgId, which is typically empty
	// on the ProvisionOrg path and is not an org id the gateway can validate.
	instance := &billingv1alpha1.InvoraBillingInstance{}
	instRef := org.Spec.InstanceRef
	instNS := instRef.Namespace
	if instNS == "" {
		instNS = obj.GetNamespace()
	}
	var token string
	if err := r.Get(ctx, types.NamespacedName{Namespace: instNS, Name: instRef.Name}, instance); err == nil {
		tokenRef := instance.Spec.TokenRef
		tokenNS := tokenRef.Namespace
		if tokenNS == "" {
			tokenNS = instNS
		}
		token, _ = billingclient.ResolveSecretValue(ctx, r.Client, tokenRef.Name, tokenNS, tokenRef.Key, instance.Namespace)

		conn, dialErr := dialGateway(instance.Spec.GatewayURL)
		if dialErr == nil {
			return &orgResourceContext{
				org: org, conn: conn, token: token, orgID: orgActingID(org),
			}, nil
		}
	}

	return &orgResourceContext{org: org, token: token, orgID: orgActingID(org)}, nil
}

// orgActingID returns the org id to assert via x-zitadel-orgid for org-scoped
// child-resource calls: the tenant GUID from spec.externalId. Non-GUID orgs
// (the platform org, snowflake landing orgs) return "" — no header is stamped
// and the call scopes to the token's home org, the pre-existing behavior that
// is correct for the platform catalog.
func orgActingID(org *billingv1alpha1.InvoraBillingOrganization) string {
	if tenantID, ok := resolveTenantID(org); ok {
		return tenantID
	}
	return ""
}

// handleGrpcDeletion handles deletion for resources using gRPC clients.
func (r *BaseReconciler) handleGrpcDeletion(
	ctx context.Context,
	obj client.Object,
	orgRef billingv1alpha1.ResourceRef,
	deletionPolicy billingv1alpha1.DeletionPolicy,
	resourceID string,
	conditions *[]metav1.Condition,
	generation int64,
	deleteFn func(ctx context.Context, orc *orgResourceContext) error,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	switch deletionPolicy {
	case billingv1alpha1.DeletionPolicyOrphan:
		logger.Info("orphaning resource (deletionPolicy=Orphan)")

	case billingv1alpha1.DeletionPolicyDelete, "":
		if resourceID != "" {
			orc, result := r.resolveOrgDependencies(ctx, orgRef, obj, conditions, generation)
			if result != nil {
				return *result, nil
			}

			if err := deleteFn(ctx, orc); err != nil {
				if !isGrpcNotFound(err) {
					SetCondition(conditions, billingv1alpha1.ConditionDeletionBlocked,
						metav1.ConditionTrue, "DeleteFailed", err.Error(), generation)
					_ = r.Status().Update(ctx, obj)
					return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
				}
			}
		}
	}

	if err := r.RemoveFinalizer(ctx, obj); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// isGrpcNotFound reports whether the error is a gRPC NOT_FOUND status.
func isGrpcNotFound(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	return st.Code() == codes.NotFound
}

// setSuccessStatus sets the standard success conditions and status fields.
func setSuccessStatus(
	conditions *[]metav1.Condition,
	lastSyncedAt **metav1.Time,
	observedGeneration *int64,
	generation int64,
	reason string,
) {
	SetCondition(conditions, billingv1alpha1.ConditionSynced,
		metav1.ConditionTrue, reason, "Resource reconciled successfully", generation)
	SetCondition(conditions, billingv1alpha1.ConditionReady,
		metav1.ConditionTrue, "Ready", "Resource reconciled successfully", generation)
	now := metav1.Now()
	*lastSyncedAt = &now
	*observedGeneration = generation
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
