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
	couponspb "github.com/invoraapp/invora-controller/gen/invora/billing/coupons/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingCouponReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcoupons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcoupons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcoupons/finalizers,verbs=update

func (r *InvoraBillingCouponReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var coupon billingv1alpha1.InvoraBillingCoupon
	if err := r.Get(ctx, req.NamespacedName, &coupon); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !coupon.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &coupon,
			coupon.Spec.OrganizationRef, coupon.Spec.DeletionPolicy,
			coupon.Status.ExternalID, &coupon.Status.Conditions, coupon.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := couponspb.NewCouponServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &couponspb.DeleteRequest{
					Input: &couponspb.DestroyCouponInput{Id: coupon.Status.ExternalID},
				})
				return err
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

	svc := couponspb.NewCouponServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if coupon.Status.ExternalID != "" {
		_, err := svc.Get(grpcCtx, &couponspb.GetRequest{Id: coupon.Status.ExternalID})
		if err != nil {
			if isGrpcNotFound(err) {
				coupon.Status.ExternalID = ""
			} else {
				SetCondition(&coupon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), coupon.Generation)
				_ = r.Status().Update(ctx, &coupon)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if coupon.Status.ExternalID != "" {
			_, err := svc.Update(grpcCtx, &couponspb.UpdateRequest{
				Input: buildUpdateCouponInput(&coupon),
			})
			if err != nil {
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
	created, err := svc.Create(grpcCtx, &couponspb.CreateRequest{
		Input: buildCreateCouponInput(&coupon),
	})
	if err != nil {
		SetCondition(&coupon.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), coupon.Generation)
		_ = r.Status().Update(ctx, &coupon)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	coupon.Status.ExternalID = created.GetCoupon().GetId()
	coupon.Status.ID = created.GetCoupon().GetId()
	setSuccessStatus(&coupon.Status.Conditions, &coupon.Status.LastSyncedAt, &coupon.Status.ObservedGeneration, coupon.Generation, "Created")
	if err := r.Status().Update(ctx, &coupon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&coupon), nil
}

func buildCreateCouponInput(coupon *billingv1alpha1.InvoraBillingCoupon) *couponspb.CreateCouponInput {
	in := &couponspb.CreateCouponInput{
		Name:       coupon.Spec.Name,
		CouponType: convert.CouponType(coupon.Spec.CouponType),
		Frequency:  convert.CouponFrequency(coupon.Spec.Frequency),
		Expiration: convert.CouponExpiration(coupon.Spec.Expiration),
	}
	if coupon.Spec.Code != "" {
		in.Code = &coupon.Spec.Code
	}
	if coupon.Spec.AmountCents != nil {
		in.AmountCents = coupon.Spec.AmountCents
	}
	if coupon.Spec.AmountCurrency != nil {
		cur := convert.Currency(*coupon.Spec.AmountCurrency)
		in.AmountCurrency = &cur
	}
	if rate, ok := convert.PercentageRate(ptrStr(coupon.Spec.PercentageRate)); ok {
		in.PercentageRate = &rate
	}
	if ts := convert.Timestamp(ptrStr(coupon.Spec.ExpirationAt)); ts != nil {
		in.ExpirationAt = ts
	}
	if coupon.Spec.Reusable {
		reusable := true
		in.Reusable = &reusable
	}
	return in
}

func buildUpdateCouponInput(coupon *billingv1alpha1.InvoraBillingCoupon) *couponspb.UpdateCouponInput {
	in := &couponspb.UpdateCouponInput{
		Id:         coupon.Status.ExternalID,
		Name:       coupon.Spec.Name,
		CouponType: convert.CouponType(coupon.Spec.CouponType),
		Frequency:  convert.CouponFrequency(coupon.Spec.Frequency),
		Expiration: convert.CouponExpiration(coupon.Spec.Expiration),
	}
	if coupon.Spec.Code != "" {
		in.Code = &coupon.Spec.Code
	}
	if coupon.Spec.AmountCents != nil {
		in.AmountCents = coupon.Spec.AmountCents
	}
	if coupon.Spec.AmountCurrency != nil {
		cur := convert.Currency(*coupon.Spec.AmountCurrency)
		in.AmountCurrency = &cur
	}
	if rate, ok := convert.PercentageRate(ptrStr(coupon.Spec.PercentageRate)); ok {
		in.PercentageRate = &rate
	}
	if ts := convert.Timestamp(ptrStr(coupon.Spec.ExpirationAt)); ts != nil {
		in.ExpirationAt = ts
	}
	reusable := coupon.Spec.Reusable
	in.Reusable = &reusable
	return in
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *InvoraBillingCouponReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingCoupon{}).Named("coupon").Complete(r)
}
