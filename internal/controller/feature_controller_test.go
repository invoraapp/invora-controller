package controller

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

// fakeFeaturePlansServer is an in-process PlansService that models the
// value_already_exist CreateFeature failure reproduced live on
// invora-controller/bayader-session-quota (bayader-devops#100): a feature
// with the requested code already exists server-side, but the reconciling
// CR's Status.ExternalID is empty (a prior reconcile lost that write).
type fakeFeaturePlansServer struct {
	planspb.UnimplementedPlansServiceServer

	mu           sync.Mutex
	createCalls  []string // codes CreateFeature was called with
	getByCode    map[string]string
	createAlways error // if set, CreateFeature always fails with this
}

func (s *fakeFeaturePlansServer) CreateFeature(_ context.Context, req *planspb.CreateFeatureRequest) (*planspb.CreateFeatureResponse, error) {
	s.mu.Lock()
	s.createCalls = append(s.createCalls, req.GetCode())
	s.mu.Unlock()
	if s.createAlways != nil {
		return nil, s.createAlways
	}
	return nil, status.Error(codes.Unimplemented, "not used in this test")
}

func (s *fakeFeaturePlansServer) GetFeature(_ context.Context, req *planspb.GetFeatureRequest) (*planspb.GetFeatureResponse, error) {
	s.mu.Lock()
	id, ok := s.getByCode[req.GetCode()]
	s.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "feature not found")
	}
	return &planspb.GetFeatureResponse{Feature: &commonpb.FeatureObject{Id: id, Code: req.GetCode()}}, nil
}

func (s *fakeFeaturePlansServer) creates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.createCalls))
	copy(out, s.createCalls)
	return out
}

func startFakePlansServer(t *testing.T, fake *fakeFeaturePlansServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	planspb.RegisterPlansServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// newFeatureReconcilerForTest wires an InvoraBillingFeatureReconciler against
// a fake client preloaded with a Ready InvoraBillingOrganization (and its
// InvoraBillingInstance + token secret) and the given feature.
func newFeatureReconcilerForTest(t *testing.T, gatewayTarget string, feature *billingv1alpha1.InvoraBillingFeature) *InvoraBillingFeatureReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("registering billing scheme: %v", err)
	}

	ns := feature.Namespace
	instance := &billingv1alpha1.InvoraBillingInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "invora-dev", Namespace: ns},
		Spec: billingv1alpha1.InvoraBillingInstanceSpec{
			GatewayURL: gatewayTarget,
			TokenRef:   billingv1alpha1.SecretKeyRef{Name: "sa-token", Key: "token"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-token", Namespace: ns},
		Data:       map[string][]byte{"token": []byte("super-admin-token")},
	}
	org := &billingv1alpha1.InvoraBillingOrganization{
		ObjectMeta: metav1.ObjectMeta{Name: "bayader", Namespace: ns},
		Spec: billingv1alpha1.InvoraBillingOrganizationSpec{
			InstanceRef: billingv1alpha1.ResourceRef{Name: "invora-dev"},
			Name:        "Bayader",
			ExternalID:  "f84623f4-bff4-4313-b68c-3a2283e5c92d",
		},
		Status: billingv1alpha1.InvoraBillingOrganizationStatus{
			BillingResourceStatus: billingv1alpha1.BillingResourceStatus{
				Conditions: []metav1.Condition{{
					Type:               billingv1alpha1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "ready for test",
					LastTransitionTime: metav1.Now(),
				}},
			},
		},
	}

	cb := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(instance, secret, org, feature).
		WithStatusSubresource(
			&billingv1alpha1.InvoraBillingInstance{},
			&billingv1alpha1.InvoraBillingOrganization{},
			&billingv1alpha1.InvoraBillingFeature{},
		)

	return &InvoraBillingFeatureReconciler{
		BaseReconciler: BaseReconciler{
			Client:      cb.Build(),
			Scheme:      scheme,
			ClientCache: billingclient.NewCache(),
		},
	}
}

func featureCR(name, ns string) *billingv1alpha1.InvoraBillingFeature {
	return &billingv1alpha1.InvoraBillingFeature{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  ns,
			Finalizers: []string{billingv1alpha1.FinalizerName}, // skip the add-finalizer requeue
		},
		Spec: billingv1alpha1.InvoraBillingFeatureSpec{
			OrganizationRef: billingv1alpha1.ResourceRef{Name: "bayader"},
			Code:            "session_quota",
			Name:            "Session Quota",
		},
	}
}

func featureSyncedCondition(t *testing.T, r *InvoraBillingFeatureReconciler, ns, name string) metav1.Condition {
	t.Helper()
	got := &billingv1alpha1.InvoraBillingFeature{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
		t.Fatalf("re-reading feature: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == billingv1alpha1.ConditionSynced {
			return c
		}
	}
	t.Fatalf("no Synced condition set; conditions=%+v", got.Status.Conditions)
	return metav1.Condition{}
}

// TestFeature_CreateAlreadyExists_AdoptsByCode is the bayader-devops#100
// reproduction directly: Status.ExternalID is empty (as it was for the live
// wedged bayader-session-quota CR) but a feature with this code already
// exists server-side. CreateFeature therefore fails with AlreadyExists on
// every reconcile; the controller must recover by looking the feature up by
// code and adopting it — not surface CreateFailed forever.
//
// Mutation-tested: with the AlreadyExists recovery branch commented out, this
// test fails (Synced stays False/CreateFailed, ExternalID stays empty).
func TestFeature_CreateAlreadyExists_AdoptsByCode(t *testing.T) {
	const existingID = "2a1f8415-b15f-4d1d-9b93-cf2d8d0c245d" // the live GUID from the incident
	fs := &fakeFeaturePlansServer{
		createAlways: status.Error(codes.AlreadyExists, `Validation errors: {"code":["value_already_exist"]}`),
		getByCode:    map[string]string{"session_quota": existingID},
	}
	target := startFakePlansServer(t, fs)

	feature := featureCR("bayader-session-quota", "invora-controller")
	r := newFeatureReconcilerForTest(t, target, feature)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: feature.Namespace, Name: feature.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error on AlreadyExists; want silent adoption: %v", err)
	}
	if got := fs.creates(); len(got) != 1 || got[0] != "session_quota" {
		t.Fatalf("CreateFeature calls = %v, want exactly one attempt (code=session_quota) before adopting", got)
	}
	if c := featureSyncedCondition(t, r, feature.Namespace, feature.Name); c.Status != metav1.ConditionTrue || c.Reason != "Adopted" {
		t.Fatalf("Synced = %s/%s, want True/Adopted", c.Status, c.Reason)
	}
	got := &billingv1alpha1.InvoraBillingFeature{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: feature.Name, Namespace: feature.Namespace}, got); err != nil {
		t.Fatalf("re-reading feature: %v", err)
	}
	if got.Status.ExternalID != existingID {
		t.Fatalf("Status.ExternalID = %q, want the adopted feature id %q", got.Status.ExternalID, existingID)
	}
	if got.Status.ID != existingID {
		t.Fatalf("Status.ID = %q, want the adopted feature id %q", got.Status.ID, existingID)
	}
	if result.RequeueAfter != DefaultRequeueInterval {
		t.Errorf("RequeueAfter = %v, want steady-state %v", result.RequeueAfter, DefaultRequeueInterval)
	}
}

// TestFeature_CreateOtherError_SurfacesCreateFailed: a non-AlreadyExists
// CreateFeature failure must still surface CreateFailed and requeue — the
// adopt recovery is scoped to AlreadyExists only, not a blanket retry-forever.
func TestFeature_CreateOtherError_SurfacesCreateFailed(t *testing.T) {
	fs := &fakeFeaturePlansServer{createAlways: status.Error(codes.Unavailable, "gateway down")}
	target := startFakePlansServer(t, fs)

	feature := featureCR("some-feature", "invora-controller")
	r := newFeatureReconcilerForTest(t, target, feature)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: feature.Namespace, Name: feature.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if c := featureSyncedCondition(t, r, feature.Namespace, feature.Name); c.Status != metav1.ConditionFalse || c.Reason != "CreateFailed" {
		t.Fatalf("Synced = %s/%s, want False/CreateFailed", c.Status, c.Reason)
	}
}
