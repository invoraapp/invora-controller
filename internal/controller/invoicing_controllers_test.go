package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
	invoicingv1alpha1 "github.com/invoraapp/invora-controller/api/invoicing/v1alpha1"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

func newInvoicingScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := corev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("registering invora core scheme: %v", err)
	}
	if err := invoicingv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("registering invoicing scheme: %v", err)
	}
	return s
}

func TestInvoraInvoicingRegulation_NotFound_NoError(t *testing.T) {
	s := newInvoicingScheme(t)
	r := &InvoraInvoicingRegulationReconciler{
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

func TestInvoraInvoicingRegulation_MissingInstance_SetsNotReady(t *testing.T) {
	s := newInvoicingScheme(t)
	reg := &invoicingv1alpha1.InvoraInvoicingRegulation{
		ObjectMeta: metav1.ObjectMeta{Name: "test-reg", Namespace: "default"},
		Spec: invoicingv1alpha1.InvoraInvoicingRegulationSpec{
			InstanceRef:    invoicingv1alpha1.ResourceRef{Name: "missing"},
			BranchRef:      invoicingv1alpha1.ResourceRef{Name: "some-branch"},
			RegulationType: "zatca",
			Enabled:        true,
		},
	}

	r := &InvoraInvoicingRegulationReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(reg).
			WithStatusSubresource(reg).
			Build(),
		Scheme:      s,
		ClientCache: billingclient.NewCache(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-reg"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue")
	}

	var got invoicingv1alpha1.InvoraInvoicingRegulation
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-reg", Namespace: "default"}, &got); err != nil {
		t.Fatalf("getting reg: %v", err)
	}
	if len(got.Status.Conditions) == 0 {
		t.Fatal("expected conditions")
	}
	if got.Status.Conditions[0].Reason != "InstanceResolveFailed" {
		t.Fatalf("expected InstanceResolveFailed, got %s", got.Status.Conditions[0].Reason)
	}
}

func TestInvoraInvoicingSettings_NotFound_NoError(t *testing.T) {
	s := newInvoicingScheme(t)
	r := &InvoraInvoicingSettingsReconciler{
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

func TestInvoraInvoicingSettings_MissingInstance_SetsNotReady(t *testing.T) {
	s := newInvoicingScheme(t)
	settings := &invoicingv1alpha1.InvoraInvoicingSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "test-settings", Namespace: "default"},
		Spec: invoicingv1alpha1.InvoraInvoicingSettingsSpec{
			InstanceRef:     invoicingv1alpha1.ResourceRef{Name: "missing"},
			DefaultCurrency: "SAR",
		},
	}

	r := &InvoraInvoicingSettingsReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(settings).
			WithStatusSubresource(settings).
			Build(),
		Scheme:      s,
		ClientCache: billingclient.NewCache(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-settings"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue")
	}

	var got invoicingv1alpha1.InvoraInvoicingSettings
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-settings", Namespace: "default"}, &got); err != nil {
		t.Fatalf("getting settings: %v", err)
	}
	if len(got.Status.Conditions) == 0 {
		t.Fatal("expected conditions")
	}
	if got.Status.Conditions[0].Reason != "InstanceResolveFailed" {
		t.Fatalf("expected InstanceResolveFailed, got %s", got.Status.Conditions[0].Reason)
	}
}
