package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

const (
	DefaultRequeueInterval    = 5 * time.Minute
	DependencyRequeueInterval = 10 * time.Second
)

// BaseReconciler provides shared infrastructure for all billing controllers.
type BaseReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ClientCache *billingclient.Cache

	// Recorder emits Kubernetes Events for conditions that are invisible from
	// the CR's own status — most importantly a reconcile that finds MORE THAN
	// ONE remote record for a CR that is contractually 1:1 (invora/devops#109:
	// one webhook CR reporting Ready=True/InSync while owning 1 of 9 identical
	// endpoint records, unnoticed for 11 days). Optional: nil-safe via
	// eventf, so unit tests may leave it unset.
	Recorder record.EventRecorder
}

// eventf records a Kubernetes Event when a Recorder is wired, and is a no-op
// otherwise.
func (r *BaseReconciler) eventf(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
}

// ResolveInstanceAdmin looks up the InvoraBillingInstance referenced by instanceRef and
// returns a cached super-admin billing client plus a gRPC connection to the gateway.
func (r *BaseReconciler) ResolveInstanceAdmin(
	ctx context.Context,
	instanceRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
) (*instanceAdminContext, error) {
	ns := instanceRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	instance := &billingv1alpha1.InvoraBillingInstance{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      instanceRef.Name,
	}, instance); err != nil {
		return nil, fmt.Errorf("getting InvoraBillingInstance %s/%s: %w", ns, instanceRef.Name, err)
	}

	readyCond := meta.FindStatusCondition(instance.Status.Conditions, billingv1alpha1.ConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("InvoraBillingInstance %s/%s is not Ready", ns, instanceRef.Name)
	}

	ref := instance.Spec.TokenRef
	tokenNS := ref.Namespace
	if tokenNS == "" {
		tokenNS = ns
	}
	token, err := billingclient.ResolveSecretValue(ctx, r.Client, ref.Name, tokenNS, ref.Key, instance.Namespace)
	if err != nil {
		return nil, fmt.Errorf("resolving super-admin token: %w", err)
	}

	conn, err := dialGateway(instance.Spec.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("dialing gateway: %w", err)
	}

	return &instanceAdminContext{
		instance: instance,
		conn:     conn,
		token:    token,
	}, nil
}

func (r *BaseReconciler) getReadyBillingOrganization(
	ctx context.Context,
	orgRef billingv1alpha1.ResourceRef,
	defaultNamespace string,
) (*billingv1alpha1.InvoraBillingOrganization, error) {
	ns := orgRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	org := &billingv1alpha1.InvoraBillingOrganization{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      orgRef.Name,
	}, org); err != nil {
		return nil, fmt.Errorf("getting InvoraBillingOrganization %s/%s: %w", ns, orgRef.Name, err)
	}

	readyCond := meta.FindStatusCondition(org.Status.Conditions, billingv1alpha1.ConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("InvoraBillingOrganization %s/%s is not Ready", ns, orgRef.Name)
	}

	return org, nil
}

// EnsureFinalizer adds the billing finalizer if not present.
func (r *BaseReconciler) EnsureFinalizer(ctx context.Context, obj client.Object) (bool, error) {
	if !controllerutil.ContainsFinalizer(obj, billingv1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(obj, billingv1alpha1.FinalizerName)
		if err := r.Update(ctx, obj); err != nil {
			return false, fmt.Errorf("adding finalizer: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// RemoveFinalizer removes the billing finalizer.
func (r *BaseReconciler) RemoveFinalizer(ctx context.Context, obj client.Object) error {
	controllerutil.RemoveFinalizer(obj, billingv1alpha1.FinalizerName)
	return r.Update(ctx, obj)
}

// GetImportID returns the import-id annotation value, if set.
func GetImportID(obj client.Object) string {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return ""
	}
	return annotations[billingv1alpha1.AnnotationImportID]
}

// GetRequeueInterval returns the reconcile interval from the annotation, or default.
func GetRequeueInterval(obj client.Object) time.Duration {
	annotations := obj.GetAnnotations()
	if annotations != nil {
		if v, ok := annotations[billingv1alpha1.AnnotationReconcileInterval]; ok {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
	}
	return DefaultRequeueInterval
}

// SetCondition is a helper to set a condition on a BillingResourceStatus.
func SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

// StatusUnchanged reports whether the proposed status matches the snapshot
// taken at the top of Reconcile, meaning r.Status().Update() would be a no-op
// that only bumps resourceVersion and fires the controller's OWN watch —
// re-enqueueing the object outside its requeue timer and turning a 5-minute
// reconcile into a continuous loop (invora/devops#68). Callers should skip
// r.Status().Update() when this returns true.
//
// LastSyncedAt is deliberately IGNORED: setSuccessStatus stamps it with
// metav1.Now() on every successful pass, so including it would make every
// status compare as "changed" and defeat the guard entirely.
//
// Ported from the same helper in shared/devops/zitadel-controller
// (internal/controller/reconciler.go), which fixed the identical hot-loop.
func StatusUnchanged(snapshot, current *billingv1alpha1.BillingResourceStatus) bool {
	if snapshot.ID != current.ID {
		return false
	}
	if snapshot.ObservedGeneration != current.ObservedGeneration {
		return false
	}
	if len(snapshot.Conditions) != len(current.Conditions) {
		return false
	}
	for i, s := range snapshot.Conditions {
		c := current.Conditions[i]
		if s.Type != c.Type || s.Status != c.Status || s.Reason != c.Reason || s.Message != c.Message {
			return false
		}
		if s.ObservedGeneration != c.ObservedGeneration {
			return false
		}
	}
	return true
}

// writeStatusIfChanged persists obj's status only when it differs from the
// snapshot taken at the top of Reconcile. externalIDBefore/externalIDAfter
// carry the per-kind ExternalID field, which lives outside
// BillingResourceStatus and so cannot be compared by StatusUnchanged.
func (r *BaseReconciler) writeStatusIfChanged(
	ctx context.Context,
	obj client.Object,
	snapshot, current *billingv1alpha1.BillingResourceStatus,
	externalIDBefore, externalIDAfter string,
) error {
	if externalIDBefore == externalIDAfter && StatusUnchanged(snapshot, current) {
		return nil
	}
	return r.Status().Update(ctx, obj)
}

// SuccessResult returns a ctrl.Result with the appropriate requeue interval.
func SuccessResult(obj client.Object) ctrl.Result {
	return ctrl.Result{RequeueAfter: GetRequeueInterval(obj)}
}
