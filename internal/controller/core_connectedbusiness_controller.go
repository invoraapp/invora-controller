package controller

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	identitypb "github.com/invoraapp/invora-controller/gen/invora/identity/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraConnectedBusinessReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraConnectedBusinessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var cb corev1alpha1.InvoraConnectedBusiness
	if err := r.Get(ctx, req.NamespacedName, &cb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraConnectedBusiness", "name", cb.Spec.Name, "suspended", cb.Spec.Suspended)

	instance, token, err := resolveInvoraInstance(ctx, r.Client, cb.Spec.InstanceRef, cb.Namespace)
	if err != nil {
		setFailed(&cb.Status.Conditions, cb.Generation, "InstanceResolveFailed", err)
		_ = r.Status().Update(ctx, &cb)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	conn, err := grpc.NewClient(instance.Spec.GatewayURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating gRPC connection: %w", err)
	}
	defer conn.Close()

	svc := identitypb.NewConnectedBusinessServiceClient(conn)
	grpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	if cb.Status.TenantID != "" {
		if cb.Spec.Suspended {
			_, err = svc.SuspendConnectedBusiness(grpcCtx, &identitypb.SuspendConnectedBusinessRequest{
				TenantId: cb.Status.TenantID,
				Reason:   "suspended via CRD",
			})
		} else {
			_, err = svc.UpdateConnectedBusiness(grpcCtx, &identitypb.UpdateConnectedBusinessRequest{
				TenantId: cb.Status.TenantID,
				Name:     &cb.Spec.Name,
			})
		}
		if err != nil {
			setFailed(&cb.Status.Conditions, cb.Generation, "UpdateFailed", err)
			_ = r.Status().Update(ctx, &cb)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	} else {
		resp, err := svc.CreateConnectedBusiness(grpcCtx, &identitypb.CreateConnectedBusinessRequest{
			Name:       cb.Spec.Name,
			AdminEmail: cb.Spec.AdminEmail,
		})
		if err != nil {
			setFailed(&cb.Status.Conditions, cb.Generation, "CreateFailed", err)
			_ = r.Status().Update(ctx, &cb)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if resp.GetConnectedBusiness() != nil {
			cb.Status.TenantID = resp.GetConnectedBusiness().GetTenantId()
		}
	}

	cb.Status.ObservedGeneration = cb.Generation
	now := metav1.Now()
	cb.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&cb.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue,
		ObservedGeneration: cb.Generation,
		Reason: "Synced", Message: "connected business synced to gateway",
	})
	if err := r.Status().Update(ctx, &cb); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraConnectedBusinessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.InvoraConnectedBusiness{}).
		Named("invoraconnectedbusiness").
		Complete(r)
}
