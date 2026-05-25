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
	invoicingv1alpha1 "github.com/invoraapp/invora-controller/api/invoicing/v1alpha1"
	regpb "github.com/invoraapp/invora-controller/gen/invora/regulations/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraInvoicingRegulationReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraInvoicingRegulationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var reg invoicingv1alpha1.InvoraInvoicingRegulation
	if err := r.Get(ctx, req.NamespacedName, &reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraInvoicingRegulation",
		"type", reg.Spec.RegulationType,
		"model", reg.Spec.SubmissionModel,
		"enabled", reg.Spec.Enabled)

	instance, token, err := resolveInvoraInstance(ctx, r.Client,
		corev1alpha1.ResourceRef{Name: reg.Spec.InstanceRef.Name, Namespace: reg.Spec.InstanceRef.Namespace},
		reg.Namespace)
	if err != nil {
		setFailed(&reg.Status.Conditions, reg.Generation, "InstanceResolveFailed", err)
		_ = r.Status().Update(ctx, &reg)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	conn, err := grpc.NewClient(instance.Spec.GatewayURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating gRPC connection: %w", err)
	}
	defer conn.Close()

	svc := regpb.NewRegulationEnrollmentServiceClient(conn)
	grpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	resp, err := svc.GetEnrollmentState(grpcCtx, &regpb.GetEnrollmentStateRequest{
		RegulationId: reg.Spec.RegulationType,
	})
	if err != nil {
		setFailed(&reg.Status.Conditions, reg.Generation, "GetStateFailed", err)
		_ = r.Status().Update(ctx, &reg)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	reg.Status.EnrollmentStatus = resp.GetState().String()
	reg.Status.ObservedGeneration = reg.Generation
	now := metav1.Now()
	reg.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue,
		ObservedGeneration: reg.Generation,
		Reason: "Synced", Message: fmt.Sprintf("enrollment state: %s", reg.Status.EnrollmentStatus),
	})
	if err := r.Status().Update(ctx, &reg); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraInvoicingRegulationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&invoicingv1alpha1.InvoraInvoicingRegulation{}).
		Named("invorainvoicingregulation").
		Complete(r)
}
