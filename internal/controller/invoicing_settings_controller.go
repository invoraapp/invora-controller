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
	settingspb "github.com/invoraapp/invora-controller/gen/invora/settings/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

type InvoraInvoicingSettingsReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache
}

func (r *InvoraInvoicingSettingsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var settings invoicingv1alpha1.InvoraInvoicingSettings
	if err := r.Get(ctx, req.NamespacedName, &settings); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("reconciling InvoraInvoicingSettings",
		"currency", settings.Spec.DefaultCurrency,
		"language", settings.Spec.DefaultLanguage)

	instance, token, err := resolveInvoraInstance(ctx, r.Client,
		corev1alpha1.ResourceRef{Name: settings.Spec.InstanceRef.Name, Namespace: settings.Spec.InstanceRef.Namespace},
		settings.Namespace)
	if err != nil {
		setFailed(&settings.Status.Conditions, settings.Generation, "InstanceResolveFailed", err)
		_ = r.Status().Update(ctx, &settings)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	conn, err := grpc.NewClient(instance.Spec.GatewayURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating gRPC connection: %w", err)
	}
	defer conn.Close()

	svc := settingspb.NewSettingsServiceClient(conn)
	grpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	_, err = svc.UpdateSettings(grpcCtx, &settingspb.UpdateSettingsRequest{})
	if err != nil {
		setFailed(&settings.Status.Conditions, settings.Generation, "UpdateFailed", err)
		_ = r.Status().Update(ctx, &settings)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	settings.Status.ObservedGeneration = settings.Generation
	now := metav1.Now()
	settings.Status.LastSyncedAt = &now
	meta.SetStatusCondition(&settings.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue,
		ObservedGeneration: settings.Generation,
		Reason: "Synced", Message: "invoicing settings synced to gateway",
	})
	if err := r.Status().Update(ctx, &settings); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *InvoraInvoicingSettingsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&invoicingv1alpha1.InvoraInvoicingSettings{}).
		Named("invorainvoicingsettings").
		Complete(r)
}
