package controller

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

// orgResourceContext holds the resolved dependencies for an org-scoped resource.
type orgResourceContext struct {
	billingClient *billingclient.Client
	org           *billingv1alpha1.InvoraBillingOrganization
	conn          *grpc.ClientConn
	token         string
	orgID         string
}

// GrpcCtx returns a context with auth metadata for gRPC calls.
func (o *orgResourceContext) GrpcCtx(ctx context.Context) context.Context {
	pairs := []string{"authorization", "Bearer " + o.token}
	if o.orgID != "" {
		pairs = append(pairs, "x-invora-org-id", o.orgID)
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

// Conn returns the gRPC connection to the gateway.
func (o *orgResourceContext) Conn() *grpc.ClientConn { return o.conn }

// dialGateway creates a gRPC connection to the given gateway URL.
func dialGateway(url string) (*grpc.ClientConn, error) {
	var cred grpc.DialOption
	if len(url) >= 5 && url[:5] == "https" {
		cred = grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, ""))
	} else {
		cred = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	return grpc.NewClient(url, cred)
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

	client, org, err := r.ResolveOrganization(ctx, orgRef, obj.GetNamespace())
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

	// Resolve gRPC conn from the org's parent instance
	instance := &billingv1alpha1.InvoraBillingInstance{}
	instRef := org.Spec.InstanceRef
	instNS := instRef.Namespace
	if instNS == "" {
		instNS = obj.GetNamespace()
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: instNS, Name: instRef.Name}, instance); err == nil {
		conn, dialErr := dialGateway(instance.Spec.GatewayURL)
		if dialErr == nil {
			return &orgResourceContext{
				billingClient: client, org: org,
				conn: conn, token: "", orgID: string(org.Status.OrganizationID),
			}, nil
		}
	}

	return &orgResourceContext{billingClient: client, org: org}, nil
}

// handleCodeBasedDeletion handles deletion for resources identified by code.
func (r *BaseReconciler) handleCodeBasedDeletion(
	ctx context.Context,
	obj client.Object,
	orgRef billingv1alpha1.ResourceRef,
	deletionPolicy billingv1alpha1.DeletionPolicy,
	resourceID string,
	conditions *[]metav1.Condition,
	generation int64,
	deleteFn func(ctx context.Context, client *billingclient.Client) error,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	switch deletionPolicy {
	case billingv1alpha1.DeletionPolicyOrphan:
		logger.Info("orphaning resource (deletionPolicy=Orphan)")

	case billingv1alpha1.DeletionPolicyDelete, "":
		if resourceID != "" {
			client, _, err := r.ResolveOrganization(ctx, orgRef, obj.GetNamespace())
			if err != nil {
				logger.Error(err, "cannot resolve org for deletion, will retry")
				SetCondition(conditions, billingv1alpha1.ConditionDeletionBlocked,
					metav1.ConditionTrue, "OrganizationUnavailable", err.Error(), generation)
				_ = r.Status().Update(ctx, obj)
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}

			if err := deleteFn(ctx, client); err != nil {
				if !billingclient.IsNotFound(err) {
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
