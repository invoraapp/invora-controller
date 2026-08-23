package controller

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
)

// --- buildEntitlementInputs: pure-function coverage -------------------------

// TestBuildEntitlementInputs_NilSpec_ReturnsNil asserts the Go-layer
// nil-vs-empty-slice distinction the function itself preserves. NOTE: this
// distinction has no effect on the wire — proto3 gives repeated fields no
// presence, so a nil and an empty slice marshal identically either way. The
// actual "never touches an unmanaged plan's entitlements" guarantee lives
// on the backend (invora/lago/lago-api's `input.entitlements.any?` gate),
// not here — see TestBuildEntitlementInputs_EmptyNonNilSlice_ReturnsEmptyNonNilSlice
// below for the wire-equivalence this test's counterpart documents.
func TestBuildEntitlementInputs_NilSpec_ReturnsNil(t *testing.T) {
	r := &InvoraBillingPlanReconciler{}
	plan := &billingv1alpha1.InvoraBillingPlan{}

	got := r.buildEntitlementInputs(plan)
	if got != nil {
		t.Fatalf("buildEntitlementInputs(nil spec) = %v, want nil", got)
	}
}

// TestBuildEntitlementInputs_MapsFeatureCodeAndPrivileges asserts the wire
// shape matches invora.billing.common.v2.EntitlementInput field-for-field.
func TestBuildEntitlementInputs_MapsFeatureCodeAndPrivileges(t *testing.T) {
	r := &InvoraBillingPlanReconciler{}
	plan := &billingv1alpha1.InvoraBillingPlan{
		Spec: billingv1alpha1.InvoraBillingPlanSpec{
			Entitlements: []billingv1alpha1.PlanEntitlement{
				{FeatureCode: "connected_business"},
				{
					FeatureCode: "seats",
					Privileges: []billingv1alpha1.EntitlementPrivilege{
						{PrivilegeCode: "max_seats", Value: "10"},
					},
				},
			},
		},
	}

	got := r.buildEntitlementInputs(plan)
	if len(got) != 2 {
		t.Fatalf("buildEntitlementInputs() returned %d entries, want 2", len(got))
	}
	if got[0].GetFeatureCode() != "connected_business" || len(got[0].GetPrivileges()) != 0 {
		t.Fatalf("entry 0 = %+v, want connected_business with no privileges", got[0])
	}
	if got[1].GetFeatureCode() != "seats" || len(got[1].GetPrivileges()) != 1 {
		t.Fatalf("entry 1 = %+v, want seats with 1 privilege", got[1])
	}
	priv := got[1].GetPrivileges()[0]
	if priv.GetPrivilegeCode() != "max_seats" || priv.GetValue() != "10" {
		t.Fatalf("privilege = %+v, want max_seats=10", priv)
	}
}

// TestBuildEntitlementInputs_EmptyNonNilSlice_ReturnsEmptyNonNilSlice documents
// (rather than papers over) the proto3 limitation: an explicit `entitlements: []`
// on the CR is wire-indistinguishable from omission once serialized — the
// backend's own `entitlements.any?` gate treats both as "field not sent". This
// test locks in that buildEntitlementInputs itself is faithful to the CR (it
// does NOT collapse empty-to-nil), so the limitation is visibly a backend/proto3
// constraint, not a bug introduced by this function.
func TestBuildEntitlementInputs_EmptyNonNilSlice_ReturnsEmptyNonNilSlice(t *testing.T) {
	r := &InvoraBillingPlanReconciler{}
	plan := &billingv1alpha1.InvoraBillingPlan{
		Spec: billingv1alpha1.InvoraBillingPlanSpec{
			Entitlements: []billingv1alpha1.PlanEntitlement{},
		},
	}

	got := r.buildEntitlementInputs(plan)
	if got == nil {
		t.Fatal("buildEntitlementInputs(empty non-nil spec) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("buildEntitlementInputs(empty non-nil spec) = %v, want empty", got)
	}
}

// --- Reconcile-path coverage: entitlements reach Create/Update requests -----

// fakePlansEntitlementsServer is a minimal PlansServiceServer capturing the
// Entitlements field sent on Get/Update/Create so a full Reconcile pass can
// assert what actually crossed the wire, without a real Invora gateway.
type fakePlansEntitlementsServer struct {
	planspb.UnimplementedPlansServiceServer

	existingID string // non-empty => Get succeeds, driving the Update branch

	updateReq    *planspb.UpdateRequest
	updateCalled bool

	createReq    *planspb.CreateRequest
	createCalled bool
}

func (f *fakePlansEntitlementsServer) Get(context.Context, *planspb.GetRequest) (*planspb.GetResponse, error) {
	return &planspb.GetResponse{Plan: &commonpb.BillingPlan{Id: f.existingID}}, nil
}

func (f *fakePlansEntitlementsServer) Update(_ context.Context, req *planspb.UpdateRequest) (*planspb.UpdateResponse, error) {
	f.updateCalled = true
	f.updateReq = req
	return &planspb.UpdateResponse{Plan: &commonpb.BillingPlan{Id: req.GetId(), Code: req.GetCode()}}, nil
}

func (f *fakePlansEntitlementsServer) List(context.Context, *planspb.ListRequest) (*planspb.ListResponse, error) {
	return &planspb.ListResponse{}, nil
}

func (f *fakePlansEntitlementsServer) Create(_ context.Context, req *planspb.CreateRequest) (*planspb.CreateResponse, error) {
	f.createCalled = true
	f.createReq = req
	return &planspb.CreateResponse{Plan: &commonpb.BillingPlan{Id: "new-id", Code: req.GetCode()}}, nil
}

func startFakePlansEntitlementsGateway(t *testing.T, srv *fakePlansEntitlementsServer) string {
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

// TestPlanReconciler_UpdateSendsDeclaredEntitlements: a plan CR that already
// exists remotely (ExternalID set) and declares spec.entitlements must send
// exactly those entitlements on the Update RPC.
func TestPlanReconciler_UpdateSendsDeclaredEntitlements(t *testing.T) {
	fakeSrv := &fakePlansEntitlementsServer{existingID: "plan-ext-1"}
	addr := startFakePlansEntitlementsGateway(t, fakeSrv)

	instance, org, instSecret := planTestFixtures(addr)
	plan := newTestPlan("free")
	plan.Status.ExternalID = "plan-ext-1"
	meta.SetStatusCondition(&plan.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready",
	})
	plan.Spec.Entitlements = []billingv1alpha1.PlanEntitlement{
		{FeatureCode: "connected_business"},
	}

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

	if !fakeSrv.updateCalled {
		t.Fatal("Update should have been called for an already-synced plan")
	}
	entitlements := fakeSrv.updateReq.GetEntitlements()
	if len(entitlements) != 1 || entitlements[0].GetFeatureCode() != "connected_business" {
		t.Fatalf("UpdateRequest.Entitlements = %v, want exactly [connected_business]", entitlements)
	}
}

// TestPlanReconciler_UpdateOmitsEntitlementsWhenSpecNil: a plan CR that never
// declares spec.entitlements must send a nil Entitlements field on Update —
// never an empty one — so the backend's full-replace path is never triggered
// for a plan this CR doesn't manage entitlements on (e.g. the Salla plans).
func TestPlanReconciler_UpdateOmitsEntitlementsWhenSpecNil(t *testing.T) {
	fakeSrv := &fakePlansEntitlementsServer{existingID: "plan-ext-2"}
	addr := startFakePlansEntitlementsGateway(t, fakeSrv)

	instance, org, instSecret := planTestFixtures(addr)
	plan := newTestPlan("salla_growth")
	plan.Status.ExternalID = "plan-ext-2"
	meta.SetStatusCondition(&plan.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "ready",
	})

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
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "salla_growth"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if !fakeSrv.updateCalled {
		t.Fatal("Update should have been called for an already-synced plan")
	}
	if entitlements := fakeSrv.updateReq.GetEntitlements(); entitlements != nil {
		t.Fatalf("UpdateRequest.Entitlements = %v, want nil (spec never declared entitlements)", entitlements)
	}
}

// TestPlanReconciler_CreateSendsDeclaredEntitlements: a brand-new plan CR
// (no ExternalID yet, no code match on List) that declares spec.entitlements
// must send them on the Create RPC too, so a freshly-created plan starts
// with its full desired entitlement set from the first reconcile.
func TestPlanReconciler_CreateSendsDeclaredEntitlements(t *testing.T) {
	fakeSrv := &fakePlansEntitlementsServer{}
	addr := startFakePlansEntitlementsGateway(t, fakeSrv)

	instance, org, instSecret := planTestFixtures(addr)
	plan := newTestPlan("free")
	plan.Spec.Entitlements = []billingv1alpha1.PlanEntitlement{
		{FeatureCode: "connected_business"},
	}

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

	if !fakeSrv.createCalled {
		t.Fatal("Create should have been called for a brand-new plan")
	}
	entitlements := fakeSrv.createReq.GetEntitlements()
	if len(entitlements) != 1 || entitlements[0].GetFeatureCode() != "connected_business" {
		t.Fatalf("CreateRequest.Entitlements = %v, want exactly [connected_business]", entitlements)
	}
}
