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

type InvoraBillingCouponReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingcoupons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingcoupons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.bdaya-dev.com,resources=billingcoupons/finalizers,verbs=update

func (r *InvoraBillingCouponReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var coupon billingv1alpha1.InvoraBillingCoupon
	if err := r.Get(ctx, req.NamespacedName, &coupon); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !coupon.DeletionTimestamp.IsZero() {
		return r.handleCodeBasedDeletion(ctx, &coupon,
			coupon.Spec.OrganizationRef, coupon.Spec.DeletionPolicy,
			coupon.Status.ExternalID, &coupon.Status.Conditions, coupon.Generation,
			func(ctx context.Context, c *billingclient.Client) error {
				return c.DeleteCoupon(ctx, coupon.Spec.Code)
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &coupon); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, coupon.Spec.OrganizationRef, &coupon,
		&coupon.Status.Conditions, coupon.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&coupon); importID != "" {
		coupon.Status.ExternalID = importID
		coupon.Status.ID = importID
		annotations := coupon.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		coupon.SetAnnotations(annotations)
		_ = r.Update(ctx, &coupon)
		setSuccessStatus(&coupon.Status.Conditions, &coupon.Status.LastSyncedAt, &coupon.Status.ObservedGeneration, coupon.Generation, "Imported")
		_ = r.Status().Update(ctx, &coupon)
		return SuccessResult(&coupon), nil
	}

	apiCoupon := billingclient.Coupon{
		Code:           coupon.Spec.Code,
		Name:           coupon.Spec.Name,
		CouponType:     coupon.Spec.CouponType,
		Frequency:      coupon.Spec.Frequency,
		Expiration:     coupon.Spec.Expiration,
		AmountCents:    coupon.Spec.AmountCents,
		AmountCurrency: coupon.Spec.AmountCurrency,
		PercentageRate: coupon.Spec.PercentageRate,
		ExpirationAt:   coupon.Spec.ExpirationAt,
		Reusable:       coupon.Spec.Reusable,
	}

	if coupon.Status.ExternalID != "" {
		_, err := orc.billingClient.GetCoupon(ctx, coupon.Spec.Code)
		if err != nil {
			if billingclient.IsNotFound(err) {
				coupon.Status.ExternalID = ""
			} else {
				SetCondition(&coupon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), coupon.Generation)
				_ = r.Status().Update(ctx, &coupon)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if coupon.Status.ExternalID != "" {
			// Update unconditionally (coupon has many fields)
			if _, err := orc.billingClient.UpdateCoupon(ctx, coupon.Spec.Code, apiCoupon); err != nil {
				SetCondition(&coupon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), coupon.Generation)
				_ = r.Status().Update(ctx, &coupon)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&coupon.Status.Conditions, &coupon.Status.LastSyncedAt, &coupon.Status.ObservedGeneration, coupon.Generation, "InSync")
			_ = r.Status().Update(ctx, &coupon)
			return SuccessResult(&coupon), nil
		}
	}

	logger.Info("creating coupon", "code", coupon.Spec.Code)
	created, err := orc.billingClient.CreateCoupon(ctx, apiCoupon)
	if err != nil {
		SetCondition(&coupon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), coupon.Generation)
		_ = r.Status().Update(ctx, &coupon)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	coupon.Status.ExternalID = created.ID
	coupon.Status.ID = created.ID
	setSuccessStatus(&coupon.Status.Conditions, &coupon.Status.LastSyncedAt, &coupon.Status.ObservedGeneration, coupon.Generation, "Created")
	if err := r.Status().Update(ctx, &coupon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&coupon), nil
}

func (r *InvoraBillingCouponReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingCoupon{}).Named("coupon").Complete(r)
}
