package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/invoraapp/invora-controller/api/core/v1alpha1"
)

func newCoreScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := corev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("registering invora core scheme: %v", err)
	}
	return s
}

func TestInvoraInstance_NotFound_NoError(t *testing.T) {
	s := newCoreScheme(t)
	r := &InvoraInstanceReconciler{
		Client: fake.NewClientBuilder().WithScheme(s).Build(),
		Scheme: s,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test", Name: "nonexistent"},
	})
	if err != nil {
		t.Fatalf("expected no error for missing instance, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got: %v", result.RequeueAfter)
	}
}

func TestInvoraInstance_MissingTokenSecret_SetsNotReady(t *testing.T) {
	s := newCoreScheme(t)
	inst := &corev1alpha1.InvoraInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "test-inst", Namespace: "default"},
		Spec: corev1alpha1.InvoraInstanceSpec{
			GatewayURL: "https://gateway.test",
			TokenRef: corev1alpha1.SecretKeyRef{
				Name: "missing-secret",
				Key:  "token",
			},
		},
	}

	r := &InvoraInstanceReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(inst).
			WithStatusSubresource(inst).
			Build(),
		Scheme: s,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-inst"},
	})
	if err != nil {
		t.Fatalf("expected no error (should set status), got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue after token resolve failure")
	}

	var got corev1alpha1.InvoraInstance
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-inst", Namespace: "default"}, &got); err != nil {
		t.Fatalf("getting instance: %v", err)
	}
	if got.Status.Connected {
		t.Fatal("expected Connected=false when token secret is missing")
	}
}

func TestInvoraInstance_ValidToken_ConnectivityFails_SetsNotReady(t *testing.T) {
	s := newCoreScheme(t)
	inst := &corev1alpha1.InvoraInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "test-inst", Namespace: "default"},
		Spec: corev1alpha1.InvoraInstanceSpec{
			GatewayURL: "https://unreachable.invalid:9999",
			TokenRef: corev1alpha1.SecretKeyRef{
				Name: "sa-token",
				Key:  "token",
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-token-value")},
	}

	r := &InvoraInstanceReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(inst, secret).
			WithStatusSubresource(inst).
			Build(),
		Scheme: s,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-inst"},
	})
	if err != nil {
		t.Fatalf("expected no error (should set status), got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue after connectivity failure")
	}

	var got corev1alpha1.InvoraInstance
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-inst", Namespace: "default"}, &got); err != nil {
		t.Fatalf("getting instance: %v", err)
	}
	if got.Status.Connected {
		t.Fatal("expected Connected=false when gateway is unreachable")
	}
}
