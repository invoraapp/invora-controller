package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

func TestInvoraBranch_NotFound_NoError(t *testing.T) {
	s := newCoreScheme(t)
	r := &InvoraBranchReconciler{
		Client:      fake.NewClientBuilder().WithScheme(s).Build(),
		Scheme:      s,
		ClientCache: billingclient.NewCache(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test", Name: "nonexistent"},
	})
	if err != nil {
		t.Fatalf("expected no error for missing branch, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got: %v", result.RequeueAfter)
	}
}

func TestInvoraBranch_MissingInstance_SetsNotReady(t *testing.T) {
	s := newCoreScheme(t)
	branch := &corev1alpha1.InvoraBranch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-branch", Namespace: "default"},
		Spec: corev1alpha1.InvoraBranchSpec{
			InstanceRef: corev1alpha1.ResourceRef{Name: "nonexistent-instance"},
			Name:        "Test Branch",
		},
	}

	r := &InvoraBranchReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(branch).
			WithStatusSubresource(branch).
			Build(),
		Scheme:      s,
		ClientCache: billingclient.NewCache(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-branch"},
	})
	if err != nil {
		t.Fatalf("expected no error (should set status), got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue after instance resolve failure")
	}

	var got corev1alpha1.InvoraBranch
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-branch", Namespace: "default"}, &got); err != nil {
		t.Fatalf("getting branch: %v", err)
	}
	if len(got.Status.Conditions) == 0 {
		t.Fatal("expected status conditions to be set")
	}
	cond := got.Status.Conditions[0]
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %s", cond.Status)
	}
	if cond.Reason != "InstanceResolveFailed" {
		t.Fatalf("expected reason InstanceResolveFailed, got %s", cond.Reason)
	}
}
