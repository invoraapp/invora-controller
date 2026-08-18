package controller

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	taxespb "github.com/invoraapp/invora-controller/gen/invora/billing/taxes/v2"
)

// fakeTaxesServer is a minimal in-process TaxesServiceServer used to drive the
// reconciler's List/Create calls without a real Invora gateway. Mirrors
// fakePlansServer in plan_controller_test.go.
type fakeTaxesServer struct {
	taxespb.UnimplementedTaxesServiceServer

	// listPages is served one entry per List call, in order, so a test can
	// exercise a cursor walk across more than one page. When it holds a single
	// entry that entry answers every call.
	listPages []*taxespb.ListResponse
	listErr   error
	listCalls int
	// lastCursor records the pagination cursor of the most recent List call.
	lastCursor string

	createFunc   func(*taxespb.CreateRequest) (*taxespb.CreateResponse, error)
	createCalled bool
}

func (f *fakeTaxesServer) List(_ context.Context, req *taxespb.ListRequest) (*taxespb.ListResponse, error) {
	f.lastCursor = req.GetPagination().GetCursor()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listPages) == 0 {
		return &taxespb.ListResponse{}, nil
	}
	if f.listCalls-1 < len(f.listPages) {
		return f.listPages[f.listCalls-1], nil
	}
	return f.listPages[len(f.listPages)-1], nil
}

func (f *fakeTaxesServer) Create(_ context.Context, req *taxespb.CreateRequest) (*taxespb.CreateResponse, error) {
	f.createCalled = true
	if f.createFunc != nil {
		return f.createFunc(req)
	}
	return nil, errors.New("Create should not have been called")
}

func startFakeTaxesGateway(t *testing.T, srv *fakeTaxesServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for fake gateway: %v", err)
	}
	s := grpc.NewServer()
	taxespb.RegisterTaxesServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String()
}

func newTestTax(code string) *billingv1alpha1.InvoraBillingTax {
	return &billingv1alpha1.InvoraBillingTax{
		ObjectMeta: metav1.ObjectMeta{
			Name:       code,
			Namespace:  "default",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingTaxSpec{
			OrganizationRef: billingv1alpha1.ResourceRef{Name: "test-org"},
			Code:            code,
			Name:            "Saudi Arabia VAT",
			Rate:            "15.0",
		},
	}
}

// newTaxReconciler builds a reconciler over a fake client seeded with the
// instance/org fixtures plus the tax CR under test.
func newTaxReconciler(t *testing.T, gatewayAddr string, tax *billingv1alpha1.InvoraBillingTax) *InvoraBillingTaxReconciler {
	t.Helper()
	instance, org, instSecret := planTestFixtures(gatewayAddr)
	s := newPlanScheme(t)
	return &InvoraBillingTaxReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, tax).
			WithStatusSubresource(tax).
			Build(),
		Scheme: s,
	}}
}

func reconcileTax(t *testing.T, r *InvoraBillingTaxReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: name},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
}

func getTax(t *testing.T, r *InvoraBillingTaxReconciler, name string) billingv1alpha1.InvoraBillingTax {
	t.Helper()
	var got billingv1alpha1.InvoraBillingTax
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &got); err != nil {
		t.Fatalf("getting tax: %v", err)
	}
	return got
}

// TestTaxReconciler_AdoptsExistingTaxByCode is the regression test for the
// lago-retire migration (invora/devops#56): the three live `vat_15` taxes were
// created by the legacy lago-controller against the SAME billing organization
// these CRs target, so a Create would fail value_already_exist forever. The
// reconciler must adopt by code instead.
func TestTaxReconciler_AdoptsExistingTaxByCode(t *testing.T) {
	fakeSrv := &fakeTaxesServer{
		listPages: []*taxespb.ListResponse{{
			Items: []*commonpb.BillingTax{{Id: "existing-tax-id-123", Code: "vat_15"}},
		}},
	}
	r := newTaxReconciler(t, startFakeTaxesGateway(t, fakeSrv), newTestTax("vat_15"))

	reconcileTax(t, r, "vat_15")

	if fakeSrv.createCalled {
		t.Fatal("Create should NOT have been called when an existing tax with the same code was found")
	}
	got := getTax(t, r, "vat_15")
	if got.Status.ExternalID != "existing-tax-id-123" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "existing-tax-id-123")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Adopted" {
		t.Fatalf("Synced condition = %+v, want True/Adopted", synced)
	}
}

// TestTaxReconciler_CreatesWhenNoCodeMatch is the counterpart: the adopt guard
// must not swallow the legitimate "brand new tax" path.
func TestTaxReconciler_CreatesWhenNoCodeMatch(t *testing.T) {
	fakeSrv := &fakeTaxesServer{
		listPages: []*taxespb.ListResponse{{
			Items: []*commonpb.BillingTax{{Id: "other-tax-id", Code: "vat_5"}},
		}},
		createFunc: func(req *taxespb.CreateRequest) (*taxespb.CreateResponse, error) {
			if req.GetCode() != "vat_15" {
				t.Fatalf("Create called with unexpected code %q", req.GetCode())
			}
			return &taxespb.CreateResponse{
				Tax: &commonpb.BillingTax{Id: "new-tax-id-456", Code: "vat_15"},
			}, nil
		},
	}
	r := newTaxReconciler(t, startFakeTaxesGateway(t, fakeSrv), newTestTax("vat_15"))

	reconcileTax(t, r, "vat_15")

	if !fakeSrv.createCalled {
		t.Fatal("Create should have been called when no existing tax matched the code")
	}
	got := getTax(t, r, "vat_15")
	if got.Status.ExternalID != "new-tax-id-456" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "new-tax-id-456")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Created" {
		t.Fatalf("Synced condition = %+v, want True/Created", synced)
	}
}

// TestTaxReconciler_FailsClosedWhenAdoptProbeFails is the invora/devops#109
// regression: a transient failure of the adoption probe must NOT degrade into
// an unconditional Create. A failed probe cannot distinguish "no such tax" from
// "the tax exists but I could not see it", and only the second is catastrophic
// (it mints a duplicate). The reconciler must refuse to Create and requeue.
func TestTaxReconciler_FailsClosedWhenAdoptProbeFails(t *testing.T) {
	fakeSrv := &fakeTaxesServer{
		listErr: status.Error(codes.Unavailable, "gateway is having a bad day"),
		createFunc: func(*taxespb.CreateRequest) (*taxespb.CreateResponse, error) {
			t.Fatal("Create must NOT be called when the adopt probe failed")
			return nil, nil
		},
	}
	r := newTaxReconciler(t, startFakeTaxesGateway(t, fakeSrv), newTestTax("vat_15"))

	reconcileTax(t, r, "vat_15")

	if fakeSrv.createCalled {
		t.Fatal("Create must NOT be called when the adopt probe failed")
	}
	got := getTax(t, r, "vat_15")
	if got.Status.ExternalID != "" {
		t.Fatalf("ExternalID = %q, want empty (nothing was adopted or created)", got.Status.ExternalID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionFalse || synced.Reason != "AdoptProbeFailed" {
		t.Fatalf("Synced condition = %+v, want False/AdoptProbeFailed", synced)
	}
}

// TestTaxReconciler_AdoptsAcrossPaginatedPages guards the pagination half: the
// billing List RPCs page their results (ListResponse.next_page_cursor), so a
// single unpaginated call can miss the very record that must be adopted and
// fall through to a Create that then fails value_already_exist. The walk must
// follow the cursor.
func TestTaxReconciler_AdoptsAcrossPaginatedPages(t *testing.T) {
	cursor := "page-2-cursor"
	fakeSrv := &fakeTaxesServer{
		listPages: []*taxespb.ListResponse{
			{
				Items:          []*commonpb.BillingTax{{Id: "unrelated-1", Code: "vat_5"}},
				NextPageCursor: &cursor,
			},
			{
				Items: []*commonpb.BillingTax{{Id: "existing-tax-id-on-page-2", Code: "vat_15"}},
			},
		},
		createFunc: func(*taxespb.CreateRequest) (*taxespb.CreateResponse, error) {
			t.Fatal("Create must NOT be called when the tax exists on a later page")
			return nil, nil
		},
	}
	r := newTaxReconciler(t, startFakeTaxesGateway(t, fakeSrv), newTestTax("vat_15"))

	reconcileTax(t, r, "vat_15")

	if fakeSrv.createCalled {
		t.Fatal("Create must NOT be called when the tax exists on a later page")
	}
	if fakeSrv.listCalls != 2 {
		t.Fatalf("List called %d times, want 2 (the walk must follow next_page_cursor)", fakeSrv.listCalls)
	}
	if fakeSrv.lastCursor != cursor {
		t.Fatalf("second List cursor = %q, want %q", fakeSrv.lastCursor, cursor)
	}
	got := getTax(t, r, "vat_15")
	if got.Status.ExternalID != "existing-tax-id-on-page-2" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "existing-tax-id-on-page-2")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Adopted" {
		t.Fatalf("Synced condition = %+v, want True/Adopted", synced)
	}
}
