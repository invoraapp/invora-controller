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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	branchespb "github.com/invoraapp/invora-controller/gen/invora/branches/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraBranchReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraBranchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var branch corev1alpha1.InvoraBranch
	if err := r.Get(ctx, req.NamespacedName, &branch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraBranch", "name", branch.Spec.Name, "default", branch.Spec.IsDefault)

	instance, token, err := resolveInvoraInstance(ctx, r.Client, branch.Spec.InstanceRef, branch.Namespace)
	if err != nil {
		setFailed(&branch.Status.Conditions, branch.Generation, "InstanceResolveFailed", err)
		_ = r.Status().Update(ctx, &branch)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	conn, err := grpc.NewClient(instance.Spec.GatewayURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating gRPC connection: %w", err)
	}
	defer conn.Close()

	svc := branchespb.NewBranchesServiceClient(conn)
	grpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	if branch.Status.BranchID != "" {
		_, err = svc.Update(grpcCtx, &branchespb.UpdateRequest{Key: branch.Status.BranchID})
		if err != nil {
			setFailed(&branch.Status.Conditions, branch.Generation, "UpdateFailed", err)
			_ = r.Status().Update(ctx, &branch)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	} else {
		resp, err := svc.Create(grpcCtx, &branchespb.CreateRequest{})
		if err != nil {
			setFailed(&branch.Status.Conditions, branch.Generation, "CreateFailed", err)
			_ = r.Status().Update(ctx, &branch)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if resp.GetDetails() != nil {
			branch.Status.BranchID = resp.GetDetails().GetId().GetKey()
		}
	}

	branch.Status.ObservedGeneration = branch.Generation
	now := metav1.Now()
	branch.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&branch.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue,
		ObservedGeneration: branch.Generation,
		Reason: "Synced", Message: "branch synced to gateway",
	})
	if err := r.Status().Update(ctx, &branch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraBranchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.InvoraBranch{}).
		Named("invorabranch").
		Complete(r)
}

func resolveInvoraInstance(ctx context.Context, k8s client.Client, ref corev1alpha1.ResourceRef, defaultNS string) (*corev1alpha1.InvoraInstance, string, error) {
	ns := ref.Namespace
	if ns == "" {
		ns = defaultNS
	}
	var inst corev1alpha1.InvoraInstance
	if err := k8s.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &inst); err != nil {
		return nil, "", fmt.Errorf("getting InvoraInstance %s/%s: %w", ns, ref.Name, err)
	}
	tokenRef := inst.Spec.TokenRef
	tokenNS := tokenRef.Namespace
	if tokenNS == "" {
		tokenNS = ns
	}
	token, err := billingclient.ResolveSecretValue(ctx, k8s, tokenRef.Name, tokenNS, tokenRef.Key, inst.Namespace)
	if err != nil {
		return nil, "", fmt.Errorf("resolving token: %w", err)
	}
	return &inst, token, nil
}

func setFailed(conditions *[]metav1.Condition, generation int64, reason string, err error) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse,
		ObservedGeneration: generation,
		Reason: reason, Message: err.Error(),
	})
}
