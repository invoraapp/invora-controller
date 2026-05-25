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

func TestInvoraConnectedBusiness_NotFound_NoError(t *testing.T) {
	s := newCoreScheme(t)
	r := &InvoraConnectedBusinessReconciler{
		Client:      fake.NewClientBuilder().WithScheme(s).Build(),
		Scheme:      s,
		ClientCache: billingclient.NewCache(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test", Name: "nonexistent"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got: %v", result.RequeueAfter)
	}
}

func TestInvoraConnectedBusiness_MissingInstance_SetsNotReady(t *testing.T) {
	s := newCoreScheme(t)
	cb := &corev1alpha1.InvoraConnectedBusiness{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cb", Namespace: "default"},
		Spec: corev1alpha1.InvoraConnectedBusinessSpec{
			InstanceRef: corev1alpha1.ResourceRef{Name: "nonexistent-instance"},
			Name:        "Test Business",
			AdminEmail:  "admin@test.com",
		},
	}

	r := &InvoraConnectedBusinessReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(cb).
			WithStatusSubresource(cb).
			Build(),
		Scheme:      s,
		ClientCache: billingclient.NewCache(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-cb"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue after instance resolve failure")
	}

	var got corev1alpha1.InvoraConnectedBusiness
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-cb", Namespace: "default"}, &got); err != nil {
		t.Fatalf("getting cb: %v", err)
	}
	if len(got.Status.Conditions) == 0 {
		t.Fatal("expected status conditions to be set")
	}
	if got.Status.Conditions[0].Reason != "InstanceResolveFailed" {
		t.Fatalf("expected reason InstanceResolveFailed, got %s", got.Status.Conditions[0].Reason)
	}
}
