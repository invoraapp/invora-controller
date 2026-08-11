package controller

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
)

// fakePlansServer is a minimal in-process PlansServiceServer used to drive
// the reconciler's List/Create calls without a real Invora gateway.
type fakePlansServer struct {
	planspb.UnimplementedPlansServiceServer

	listResp *planspb.ListResponse
	listErr  error

	getResp *planspb.GetResponse
	getErr  error

	createFunc   func(*planspb.CreateRequest) (*planspb.CreateResponse, error)
	createCalled bool

	updateCalled bool
	lastUpdate   *planspb.UpdateRequest

	// lastMetadata captures the inbound gRPC metadata of the most recent call,
	// so tests can assert the org-scoping headers the reconciler stamps.
	lastMetadata metadata.MD
}

func (f *fakePlansServer) List(ctx context.Context, _ *planspb.ListRequest) (*planspb.ListResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		f.lastMetadata = md
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakePlansServer) Create(_ context.Context, req *planspb.CreateRequest) (*planspb.CreateResponse, error) {
	f.createCalled = true
	if f.createFunc != nil {
		return f.createFunc(req)
	}
	return nil, errors.New("Create should not have been called")
}

// startFakePlansGateway starts a local, insecure gRPC server hosting the
// PlansService and returns its bare "host:port" dial target (no scheme —
// see internal/gateway.Target, which passes bare host:port through as
// insecure/plaintext).
func startFakePlansGateway(t *testing.T, srv *fakePlansServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for fake gateway: %v", err)
	}
	s := grpc.NewServer()
	planspb.RegisterPlansServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String()
}

func newPlanScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := billingv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("registering billing scheme: %v", err)
	}
	return s
}

// planTestFixtures wires up an InvoraBillingInstance + InvoraBillingOrganization
// (both already Ready, instance token Secret present) so a
// InvoraBillingPlanReconciler.Reconcile call reaches the List/Create branch
// directly (finalizer already attached, dependencies already resolved).
// Org-scoped resources authenticate with the instance's super-admin token plus
// the x-zitadel-orgid header — there is no per-org credential/Secret.
func planTestFixtures(gatewayAddr string) (*billingv1alpha1.InvoraBillingInstance, *billingv1alpha1.InvoraBillingOrganization, *corev1.Secret) {
	instSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-super-admin-token")},
	}
	instance := &billingv1alpha1.InvoraBillingInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
		Spec: billingv1alpha1.InvoraBillingInstanceSpec{
			GatewayURL: gatewayAddr,
			TokenRef:   billingv1alpha1.SecretKeyRef{Name: "inst-token", Key: "token"},
		},
	}
	meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready",
	})
	org := &billingv1alpha1.InvoraBillingOrganization{
		ObjectMeta: metav1.ObjectMeta{Name: "test-org", Namespace: "default"},
		Spec: billingv1alpha1.InvoraBillingOrganizationSpec{
			InstanceRef: billingv1alpha1.ResourceRef{Name: "test-instance"},
			Name:        "Test Org",
			// ExternalID must be a canonical GUID or resolveTenantID rejects it —
			// irrelevant for the plan reconciler itself, but kept realistic.
			ExternalID: "00000000-0000-0000-0000-000000000099",
		},
		Status: billingv1alpha1.InvoraBillingOrganizationStatus{
			OrganizationID: "org-123",
		},
	}
	meta.SetStatusCondition(&org.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready",
	})
	return instance, org, instSecret
}

func newTestPlan(code string) *billingv1alpha1.InvoraBillingPlan {
	return &billingv1alpha1.InvoraBillingPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:       code,
			Namespace:  "default",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingPlanSpec{
			OrganizationRef: billingv1alpha1.ResourceRef{Name: "test-org"},
			Code:            code,
			Name:            "Free",
			AmountCents:     0,
			AmountCurrency:  "SAR",
			Interval:        "monthly",
			PayInAdvance:    true,
		},
	}
}

// TestPlanReconciler_AdoptsExistingPlanByCode is the regression test for the
// catalog-ownership migration: when a plan with a matching code already
// exists in the org (e.g. pre-created by the legacy lago-controller LagoPlan
// CR under the same underlying Lago organization), the reconciler must adopt
// it by ID instead of calling Create and failing with value_already_exist.
func TestPlanReconciler_AdoptsExistingPlanByCode(t *testing.T) {
	fakeSrv := &fakePlansServer{
		listResp: &planspb.ListResponse{
			Items: []*commonpb.BillingPlan{
				{Id: "existing-plan-id-123", Code: "free"},
			},
		},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	instance, org, instSecret := planTestFixtures(addr)
	plan := newTestPlan("free")

	s := newPlanScheme(t)
	r := &InvoraBillingPlanReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, plan).
			WithStatusSubresource(plan).
			Build(),
		Scheme: s,
	}}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if fakeSrv.createCalled {
		t.Fatal("Create should NOT have been called when an existing plan with the same code was found")
	}

	var got billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "free"}, &got); err != nil {
		t.Fatalf("getting plan: %v", err)
	}
	if got.Status.ExternalID != "existing-plan-id-123" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "existing-plan-id-123")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Adopted" {
		t.Fatalf("Synced condition = %+v, want True/Adopted", synced)
	}
}

// TestPlanReconciler_CreatesWhenNoCodeMatch is the counterpart: when List
// returns plans with different codes (or none at all), the reconciler must
// still fall through to Create — the adopt-by-code guard must not swallow
// the legitimate "brand new plan" path.
func TestPlanReconciler_CreatesWhenNoCodeMatch(t *testing.T) {
	fakeSrv := &fakePlansServer{
		listResp: &planspb.ListResponse{
			Items: []*commonpb.BillingPlan{
				{Id: "other-plan-id", Code: "enterprise"},
			},
		},
		createFunc: func(req *planspb.CreateRequest) (*planspb.CreateResponse, error) {
			if req.GetCode() != "professional" {
				t.Fatalf("Create called with unexpected code %q", req.GetCode())
			}
			return &planspb.CreateResponse{
				Plan: &commonpb.BillingPlan{Id: "new-plan-id-456", Code: "professional"},
			}, nil
		},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	instance, org, instSecret := planTestFixtures(addr)
	plan := newTestPlan("professional")

	s := newPlanScheme(t)
	r := &InvoraBillingPlanReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, plan).
			WithStatusSubresource(plan).
			Build(),
		Scheme: s,
	}}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "professional"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if !fakeSrv.createCalled {
		t.Fatal("Create should have been called when no existing plan matched the code")
	}

	var got billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "professional"}, &got); err != nil {
		t.Fatalf("getting plan: %v", err)
	}
	if got.Status.ExternalID != "new-plan-id-456" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "new-plan-id-456")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Created" {
		t.Fatalf("Synced condition = %+v, want True/Created", synced)
	}
}

// TestPlanReconciler_AssertsActingOrgViaZitadelOrgidHeader is the regression
// test for the zero-plans split-brain: org-scoped calls MUST assert the acting
// org with `x-zitadel-orgid: <spec.externalId GUID>`. The previously-stamped
// `x-invora-org-id` is a trusted-INTERNAL header the WebGateway strips from
// every inbound request and rewrites to the token's HOME org, so all
// child-resource writes (plans, taxes, ...) silently landed in the controller
// PAT's home (admin) org while tenant reads hit the correctly-keyed — and
// therefore empty — Lago org ({"items":[],"totalCount":0}).
func TestPlanReconciler_AssertsActingOrgViaZitadelOrgidHeader(t *testing.T) {
	fakeSrv := &fakePlansServer{
		listResp: &planspb.ListResponse{},
		createFunc: func(req *planspb.CreateRequest) (*planspb.CreateResponse, error) {
			return &planspb.CreateResponse{
				Plan: &commonpb.BillingPlan{Id: "plan-id-789", Code: req.GetCode()},
			}, nil
		},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	instance, org, instSecret := planTestFixtures(addr)
	plan := newTestPlan("basic-monthly")

	s := newPlanScheme(t)
	r := &InvoraBillingPlanReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, plan).
			WithStatusSubresource(plan).
			Build(),
		Scheme: s,
	}}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "basic-monthly"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if fakeSrv.lastMetadata == nil {
		t.Fatal("fake gateway captured no inbound metadata")
	}
	// The acting org is the org CR's externalId (tenant GUID), never
	// status.organizationId ("org-123" in the fixture).
	if got := fakeSrv.lastMetadata.Get("x-zitadel-orgid"); len(got) != 1 || got[0] != org.Spec.ExternalID {
		t.Fatalf("x-zitadel-orgid = %v, want [%q]", got, org.Spec.ExternalID)
	}
	if got := fakeSrv.lastMetadata.Get("x-invora-org-id"); len(got) != 0 {
		t.Fatalf("x-invora-org-id must not be stamped (gateway strips it); got %v", got)
	}
}
