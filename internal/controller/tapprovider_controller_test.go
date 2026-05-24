package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

// newTapReconcilerForTest wires a InvoraBillingTapProviderReconciler against a
// fake client preloaded with the given objects. Mirrors
// newSubscriberForTest in zitadelorgevent_subscriber_test.go for consistency.
func newTapReconcilerForTest(t *testing.T, objs ...runtime.Object) *InvoraBillingTapProviderReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("registering billing scheme: %v", err)
	}

	cb := fake.NewClientBuilder().
		WithScheme(scheme).
		// InvoraBillingTapProvider has subresource:status — without this,
		// the fake client doesn't persist Status.Conditions updates.
		WithStatusSubresource(&billingv1alpha1.InvoraBillingTapProvider{})
	if len(objs) > 0 {
		cb = cb.WithRuntimeObjects(objs...)
	}

	return &InvoraBillingTapProviderReconciler{
		BaseReconciler: BaseReconciler{
			Client:      cb.Build(),
			Scheme:      scheme,
			ClientCache: billingclient.NewCache(),
		},
	}
}

// TestInvoraBillingTapProvider_NotFound exercises the early-return on
// missing CR — the controller should silently succeed (no requeue, no
// error) so a deleted CR doesn't burn requeue cycles.
func TestInvoraBillingTapProvider_NotFound(t *testing.T) {
	r := newTapReconcilerForTest(t)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "ghost-tap"},
	})
	if err != nil {
		t.Fatalf("Reconcile on missing CR returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected zero requeue on missing CR, got %+v", result)
	}
}

// TestInvoraBillingTapProvider_AddsFinalizer asserts that the first
// reconcile attaches the standard billing finalizer and requeues
// immediately so the next pass can do the real work.
func TestInvoraBillingTapProvider_AddsFinalizer(t *testing.T) {
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "tap-test", Namespace: "billing-dev"},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "invora-org"},
			Code:                "tap-prod",
			Name:                "Tap (Production)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "tap-keys",
				Key:  "apiKey",
			},
		},
	}
	r := newTapReconcilerForTest(t, tap)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "tap-test"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !result.Requeue {
		t.Errorf("expected Requeue=true after finalizer add, got %+v", result)
	}

	got := &billingv1alpha1.InvoraBillingTapProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: "tap-test", Namespace: "billing-dev"}, got); err != nil {
		t.Fatalf("re-reading CR: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, billingv1alpha1.FinalizerName) {
		t.Errorf("expected finalizer %q after reconcile, finalizers=%v",
			billingv1alpha1.FinalizerName, got.Finalizers)
	}
}

// TestInvoraBillingTapProvider_RequeuesWhenOrgMissing exercises the
// dependency-resolution branch: when the referenced InvoraBillingOrganization
// doesn't exist (or isn't Ready), the controller sets the
// DependencyReady condition to False and schedules a fast requeue
// rather than failing.
func TestInvoraBillingTapProvider_RequeuesWhenOrgMissing(t *testing.T) {
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tap-test",
			Namespace:  "billing-dev",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "missing-org"},
			Code:                "tap-prod",
			Name:                "Tap (Production)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "tap-keys",
				Key:  "apiKey",
			},
		},
	}
	r := newTapReconcilerForTest(t, tap)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "tap-test"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter != DependencyRequeueInterval {
		t.Errorf("expected RequeueAfter=%v on missing org, got %+v",
			DependencyRequeueInterval, result)
	}

	got := &billingv1alpha1.InvoraBillingTapProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: "tap-test", Namespace: "billing-dev"}, got); err != nil {
		t.Fatalf("re-reading CR: %v", err)
	}
	var dep *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == billingv1alpha1.ConditionDependencyReady {
			dep = &got.Status.Conditions[i]
			break
		}
	}
	if dep == nil {
		t.Fatalf("expected %s condition to be set; got conditions=%+v",
			billingv1alpha1.ConditionDependencyReady, got.Status.Conditions)
	}
	if dep.Status != metav1.ConditionFalse {
		t.Errorf("DependencyReady status = %s, want False", dep.Status)
	}
}

// TestInvoraBillingTapProvider_DeletionRemovesFinalizer asserts that the
// upstream-no-destroy-mutation comment in the controller is honored:
// deletion drops the finalizer even though there's no billing-side delete
// call, so the CR can be garbage-collected without manual intervention.
func TestInvoraBillingTapProvider_DeletionRemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "tap-test",
			Namespace:         "billing-dev",
			Finalizers:        []string{billingv1alpha1.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "invora-org"},
			Code:                "tap-prod",
			Name:                "Tap (Production)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "tap-keys",
				Key:  "apiKey",
			},
		},
	}
	r := newTapReconcilerForTest(t, tap)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "tap-test"},
	}); err != nil {
		t.Fatalf("Reconcile on deletion: %v", err)
	}

	got := &billingv1alpha1.InvoraBillingTapProvider{}
	err := r.Get(ctx, types.NamespacedName{Name: "tap-test", Namespace: "billing-dev"}, got)
	if apierrors.IsNotFound(err) {
		// fake-client removes the object once the last finalizer is dropped — also OK.
		return
	}
	if err != nil {
		t.Fatalf("re-reading CR after deletion reconcile: %v", err)
	}
	if controllerutil.ContainsFinalizer(got, billingv1alpha1.FinalizerName) {
		t.Errorf("expected finalizer %q to be removed; finalizers=%v",
			billingv1alpha1.FinalizerName, got.Finalizers)
	}
}

// silence unused import linter; corev1 stays for parity with the
// sibling test file pattern (handlers may grow to attach Secret refs).
var _ corev1.Secret
