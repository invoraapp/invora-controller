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
	meteringpb "github.com/invoraapp/invora-controller/gen/invora/billing/metering/v2"
)

// fakeMetricsServer is a minimal in-process BillableMetricsServiceServer used
// to drive the reconciler's List/Create calls without a real Invora gateway.
type fakeMetricsServer struct {
	meteringpb.UnimplementedBillableMetricsServiceServer

	// listPages is served one entry per List call, in order, so a test can
	// exercise a cursor walk across more than one page. When it holds a single
	// entry that entry answers every call.
	listPages []*meteringpb.ListResponse
	listErr   error
	listCalls int
	// lastCursor records the pagination cursor of the most recent List call.
	lastCursor string

	createFunc   func(*meteringpb.CreateRequest) (*meteringpb.CreateResponse, error)
	createCalled bool
}

func (f *fakeMetricsServer) List(_ context.Context, req *meteringpb.ListRequest) (*meteringpb.ListResponse, error) {
	f.lastCursor = req.GetPagination().GetCursor()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listPages) == 0 {
		return &meteringpb.ListResponse{}, nil
	}
	if f.listCalls-1 < len(f.listPages) {
		return f.listPages[f.listCalls-1], nil
	}
	return f.listPages[len(f.listPages)-1], nil
}

func (f *fakeMetricsServer) Create(_ context.Context, req *meteringpb.CreateRequest) (*meteringpb.CreateResponse, error) {
	f.createCalled = true
	if f.createFunc != nil {
		return f.createFunc(req)
	}
	return nil, errors.New("Create should not have been called")
}

func startFakeMetricsGateway(t *testing.T, srv *fakeMetricsServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for fake gateway: %v", err)
	}
	s := grpc.NewServer()
	meteringpb.RegisterBillableMetricsServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String()
}

func newTestMetric(code string) *billingv1alpha1.InvoraBillingMetric {
	return &billingv1alpha1.InvoraBillingMetric{
		ObjectMeta: metav1.ObjectMeta{
			Name:       code,
			Namespace:  "default",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingMetricSpec{
			OrganizationRef: billingv1alpha1.ResourceRef{Name: "test-org"},
			Code:            code,
			Name:            "Documents issued",
			AggregationType: "count_agg",
		},
	}
}

func newMetricReconciler(t *testing.T, gatewayAddr string, metric *billingv1alpha1.InvoraBillingMetric) *InvoraBillingMetricReconciler {
	t.Helper()
	instance, org, instSecret := planTestFixtures(gatewayAddr)
	s := newPlanScheme(t)
	return &InvoraBillingMetricReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, metric).
			WithStatusSubresource(metric).
			Build(),
		Scheme: s,
	}}
}

func reconcileMetric(t *testing.T, r *InvoraBillingMetricReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: name},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
}

func getMetric(t *testing.T, r *InvoraBillingMetricReconciler, name string) billingv1alpha1.InvoraBillingMetric {
	t.Helper()
	var got billingv1alpha1.InvoraBillingMetric
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &got); err != nil {
		t.Fatalf("getting metric: %v", err)
	}
	return got
}

// TestMetricReconciler_AdoptsExistingMetricByCode is the regression test for
// the lago-retire migration (invora/devops#57): nine live invora-* billable
// metrics were created by the legacy lago-controller against the SAME billing
// organization these CRs target, so a Create would fail value_already_exist
// forever. The reconciler must adopt by code instead.
func TestMetricReconciler_AdoptsExistingMetricByCode(t *testing.T) {
	fakeSrv := &fakeMetricsServer{
		listPages: []*meteringpb.ListResponse{{
			Items: []*commonpb.BillingBillableMetric{{Id: "existing-metric-id-123", Code: "documents_issued"}},
		}},
	}
	r := newMetricReconciler(t, startFakeMetricsGateway(t, fakeSrv), newTestMetric("documents_issued"))

	reconcileMetric(t, r, "documents_issued")

	if fakeSrv.createCalled {
		t.Fatal("Create should NOT have been called when an existing metric with the same code was found")
	}
	got := getMetric(t, r, "documents_issued")
	if got.Status.ExternalID != "existing-metric-id-123" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "existing-metric-id-123")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Adopted" {
		t.Fatalf("Synced condition = %+v, want True/Adopted", synced)
	}
}

// TestMetricReconciler_CreatesWhenNoCodeMatch is the counterpart, and it is the
// live path for the six khdmaa-* metric CRs, whose underlying billing records
// were never created by the legacy controller (empty status — invora/devops#57
// and #44): those must still be created.
func TestMetricReconciler_CreatesWhenNoCodeMatch(t *testing.T) {
	fakeSrv := &fakeMetricsServer{
		listPages: []*meteringpb.ListResponse{{
			Items: []*commonpb.BillingBillableMetric{{Id: "other-metric-id", Code: "invora_documents"}},
		}},
		createFunc: func(req *meteringpb.CreateRequest) (*meteringpb.CreateResponse, error) {
			if req.GetCode() != "khdmaa_activity" {
				t.Fatalf("Create called with unexpected code %q", req.GetCode())
			}
			return &meteringpb.CreateResponse{
				BillableMetric: &commonpb.BillingBillableMetric{Id: "new-metric-id-456", Code: "khdmaa_activity"},
			}, nil
		},
	}
	r := newMetricReconciler(t, startFakeMetricsGateway(t, fakeSrv), newTestMetric("khdmaa_activity"))

	reconcileMetric(t, r, "khdmaa_activity")

	if !fakeSrv.createCalled {
		t.Fatal("Create should have been called when no existing metric matched the code")
	}
	got := getMetric(t, r, "khdmaa_activity")
	if got.Status.ExternalID != "new-metric-id-456" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "new-metric-id-456")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Created" {
		t.Fatalf("Synced condition = %+v, want True/Created", synced)
	}
}

// TestMetricReconciler_FailsClosedWhenAdoptProbeFails is the invora/devops#109
// regression for the metric surface: a transient failure of the adoption probe
// must NOT degrade into an unconditional Create.
func TestMetricReconciler_FailsClosedWhenAdoptProbeFails(t *testing.T) {
	fakeSrv := &fakeMetricsServer{
		listErr: status.Error(codes.Unavailable, "gateway is having a bad day"),
		createFunc: func(*meteringpb.CreateRequest) (*meteringpb.CreateResponse, error) {
			t.Fatal("Create must NOT be called when the adopt probe failed")
			return nil, nil
		},
	}
	r := newMetricReconciler(t, startFakeMetricsGateway(t, fakeSrv), newTestMetric("documents_issued"))

	reconcileMetric(t, r, "documents_issued")

	if fakeSrv.createCalled {
		t.Fatal("Create must NOT be called when the adopt probe failed")
	}
	got := getMetric(t, r, "documents_issued")
	if got.Status.ExternalID != "" {
		t.Fatalf("ExternalID = %q, want empty (nothing was adopted or created)", got.Status.ExternalID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionFalse || synced.Reason != "AdoptProbeFailed" {
		t.Fatalf("Synced condition = %+v, want False/AdoptProbeFailed", synced)
	}
}

// TestMetricReconciler_AdoptsAcrossPaginatedPages guards the pagination half.
// This surface is the one that actually needs it: the migration declares 15
// metric CRs against one organization, so the record to adopt is materially
// more likely to sit past the first page than for the 3-row tax surface.
func TestMetricReconciler_AdoptsAcrossPaginatedPages(t *testing.T) {
	cursor := "page-2-cursor"
	fakeSrv := &fakeMetricsServer{
		listPages: []*meteringpb.ListResponse{
			{
				Items:          []*commonpb.BillingBillableMetric{{Id: "unrelated-1", Code: "invora_documents"}},
				NextPageCursor: &cursor,
			},
			{
				Items: []*commonpb.BillingBillableMetric{{Id: "existing-metric-on-page-2", Code: "documents_issued"}},
			},
		},
		createFunc: func(*meteringpb.CreateRequest) (*meteringpb.CreateResponse, error) {
			t.Fatal("Create must NOT be called when the metric exists on a later page")
			return nil, nil
		},
	}
	r := newMetricReconciler(t, startFakeMetricsGateway(t, fakeSrv), newTestMetric("documents_issued"))

	reconcileMetric(t, r, "documents_issued")

	if fakeSrv.createCalled {
		t.Fatal("Create must NOT be called when the metric exists on a later page")
	}
	if fakeSrv.listCalls != 2 {
		t.Fatalf("List called %d times, want 2 (the walk must follow next_page_cursor)", fakeSrv.listCalls)
	}
	if fakeSrv.lastCursor != cursor {
		t.Fatalf("second List cursor = %q, want %q", fakeSrv.lastCursor, cursor)
	}
	got := getMetric(t, r, "documents_issued")
	if got.Status.ExternalID != "existing-metric-on-page-2" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "existing-metric-on-page-2")
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Adopted" {
		t.Fatalf("Synced condition = %+v, want True/Adopted", synced)
	}
}
