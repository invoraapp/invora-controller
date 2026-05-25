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
	customerspb "github.com/invoraapp/invora-controller/gen/invora/billing/customers/v2"
	"github.com/invoraapp/invora-controller/internal/convert"
)

type InvoraBillingCustomerReconciler struct{ BaseReconciler }

// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=billing.invora.app,resources=billingcustomers/finalizers,verbs=update

func (r *InvoraBillingCustomerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var customer billingv1alpha1.InvoraBillingCustomer
	if err := r.Get(ctx, req.NamespacedName, &customer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !customer.DeletionTimestamp.IsZero() {
		return r.handleGrpcDeletion(ctx, &customer,
			customer.Spec.OrganizationRef, customer.Spec.DeletionPolicy,
			customer.Status.ExternalID, &customer.Status.Conditions, customer.Generation,
			func(ctx context.Context, orc *orgResourceContext) error {
				svc := customerspb.NewCustomerServiceClient(orc.Conn())
				_, err := svc.Delete(orc.GrpcCtx(ctx), &customerspb.DeleteRequest{
					Input: &customerspb.DestroyCustomerInput{Id: customer.Status.ExternalID},
				})
				return err
			})
	}

	if added, err := r.EnsureFinalizer(ctx, &customer); err != nil {
		return ctrl.Result{}, err
	} else if added {
		return ctrl.Result{Requeue: true}, nil
	}

	orc, result := r.resolveOrgDependencies(ctx, customer.Spec.OrganizationRef, &customer,
		&customer.Status.Conditions, customer.Generation)
	if result != nil {
		return *result, nil
	}

	if importID := GetImportID(&customer); importID != "" {
		customer.Status.ExternalID = importID
		customer.Status.ID = importID
		annotations := customer.GetAnnotations()
		delete(annotations, billingv1alpha1.AnnotationImportID)
		customer.SetAnnotations(annotations)
		_ = r.Update(ctx, &customer)
		setSuccessStatus(&customer.Status.Conditions, &customer.Status.LastSyncedAt, &customer.Status.ObservedGeneration, customer.Generation, "Imported")
		_ = r.Status().Update(ctx, &customer)
		return SuccessResult(&customer), nil
	}

	svc := customerspb.NewCustomerServiceClient(orc.Conn())
	grpcCtx := orc.GrpcCtx(ctx)

	if customer.Status.ExternalID != "" {
		extID := customer.Spec.ExternalID
		_, err := svc.Get(grpcCtx, &customerspb.GetRequest{ExternalId: &extID})
		if err != nil {
			if isGrpcNotFound(err) {
				customer.Status.ExternalID = ""
			} else {
				SetCondition(&customer.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "GetFailed", err.Error(), customer.Generation)
				_ = r.Status().Update(ctx, &customer)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		if customer.Status.ExternalID != "" {
			_, err := svc.Create(grpcCtx, &customerspb.CreateRequest{
				Input: buildCreateCustomerInput(&customer),
			})
			if err != nil {
				SetCondition(&customer.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "UpdateFailed", err.Error(), customer.Generation)
				_ = r.Status().Update(ctx, &customer)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			setSuccessStatus(&customer.Status.Conditions, &customer.Status.LastSyncedAt, &customer.Status.ObservedGeneration, customer.Generation, "InSync")
			_ = r.Status().Update(ctx, &customer)
			return SuccessResult(&customer), nil
		}
	}

	logger.Info("creating customer", "externalId", customer.Spec.ExternalID)
	created, err := svc.Create(grpcCtx, &customerspb.CreateRequest{
		Input: buildCreateCustomerInput(&customer),
	})
	if err != nil {
		SetCondition(&customer.Status.Conditions, billingv1alpha1.ConditionSynced, metav1.ConditionFalse, "CreateFailed", err.Error(), customer.Generation)
		_ = r.Status().Update(ctx, &customer)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	customer.Status.ExternalID = created.GetCustomer().GetId()
	customer.Status.ID = created.GetCustomer().GetId()
	setSuccessStatus(&customer.Status.Conditions, &customer.Status.LastSyncedAt, &customer.Status.ObservedGeneration, customer.Generation, "Created")
	if err := r.Status().Update(ctx, &customer); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return SuccessResult(&customer), nil
}

func buildCreateCustomerInput(customer *billingv1alpha1.InvoraBillingCustomer) *customerspb.CreateCustomerInput {
	in := &customerspb.CreateCustomerInput{
		ExternalId: customer.Spec.ExternalID,
		TaxCodes:   customer.Spec.TaxCodes,
	}
	if customer.Spec.Name != "" {
		in.Name = &customer.Spec.Name
	}
	if customer.Spec.Email != "" {
		in.Email = &customer.Spec.Email
	}
	if customer.Spec.Currency != "" {
		cur := convert.Currency(customer.Spec.Currency)
		in.Currency = &cur
	}
	if customer.Spec.AddressLine1 != "" {
		in.AddressLine1 = &customer.Spec.AddressLine1
	}
	if customer.Spec.AddressLine2 != "" {
		in.AddressLine2 = &customer.Spec.AddressLine2
	}
	if customer.Spec.City != "" {
		in.City = &customer.Spec.City
	}
	if customer.Spec.State != "" {
		in.State = &customer.Spec.State
	}
	if customer.Spec.Zipcode != "" {
		in.Zipcode = &customer.Spec.Zipcode
	}
	if customer.Spec.LegalName != "" {
		in.LegalName = &customer.Spec.LegalName
	}
	if customer.Spec.LegalNumber != "" {
		in.LegalNumber = &customer.Spec.LegalNumber
	}
	if customer.Spec.Phone != "" {
		in.Phone = &customer.Spec.Phone
	}
	return in
}

func (r *InvoraBillingCustomerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&billingv1alpha1.InvoraBillingCustomer{}).Named("customer").Complete(r)
}
