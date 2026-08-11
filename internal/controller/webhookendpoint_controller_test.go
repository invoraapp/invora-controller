package controller

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	webhookspb "github.com/invoraapp/invora-controller/gen/invora/billing/webhooks/v2"
)

// fakeWebhookServer is a minimal in-process WebhookEndpointsServiceServer used
// to drive the reconciler's Get/List/Create/Update calls without a real Invora
// gateway. It mirrors fakePlansServer in plan_controller_test.go.
type fakeWebhookServer struct {
	webhookspb.UnimplementedWebhookEndpointsServiceServer

	getResp *webhookspb.GetResponse
	getErr  error

	listResp *webhookspb.ListResponse
	listErr  error

	createFunc   func(*webhookspb.CreateRequest) (*webhookspb.CreateResponse, error)
	createCalled bool

	updateCalled bool
}

func (f *fakeWebhookServer) Get(_ context.Context, _ *webhookspb.GetRequest) (*webhookspb.GetResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResp, nil
}

func (f *fakeWebhookServer) List(_ context.Context, _ *webhookspb.ListRequest) (*webhookspb.ListResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakeWebhookServer) Create(_ context.Context, req *webhookspb.CreateRequest) (*webhookspb.CreateResponse, error) {
	f.createCalled = true
	if f.createFunc != nil {
		return f.createFunc(req)
	}
	return nil, errors.New("Create should not have been called")
}

func (f *fakeWebhookServer) Update(_ context.Context, req *webhookspb.UpdateRequest) (*webhookspb.UpdateResponse, error) {
	f.updateCalled = true
	return &webhookspb.UpdateResponse{
		WebhookEndpoint: &commonpb.BillingWebhookEndpoint{Id: req.GetId(), WebhookUrl: req.GetWebhookUrl()},
	}, nil
}

func startFakeWebhookGateway(t *testing.T, srv *fakeWebhookServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for fake gateway: %v", err)
	}
	s := grpc.NewServer()
	webhookspb.RegisterWebhookEndpointsServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String()
}

const testWebhookURL = "https://dev-api-bayader.invora.app/webhooks/invora"

func newTestWebhook(name string) *billingv1alpha1.InvoraBillingWebhookEndpoint {
	return &billingv1alpha1.InvoraBillingWebhookEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingWebhookEndpointSpec{
			OrganizationRef: billingv1alpha1.ResourceRef{Name: "test-org"},
			WebhookURL:      testWebhookURL,
			SignatureAlgo:   "hmac",
		},
	}
}

func newWebhookReconciler(t *testing.T, wh *billingv1alpha1.InvoraBillingWebhookEndpoint, gatewayAddr string, rec record.EventRecorder) *InvoraBillingWebhookEndpointReconciler {
	t.Helper()
	instance, org, instSecret := planTestFixtures(gatewayAddr)
	s := newPlanScheme(t)
	return &InvoraBillingWebhookEndpointReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, wh).
			WithStatusSubresource(wh).
			Build(),
		Scheme:   s,
		Recorder: rec,
	}}
}

// TestWebhookReconciler_RequeuesInsteadOfCreatingWhenListFails is the
// regression test for invora/devops#109: the adopt-by-URL probe must fail
// CLOSED. Before the fix, `if err == nil { ... }` swallowed a List error and
// fell straight through to Create, so every transient gateway blip minted
// another endpoint for the same CR (9 live records for one bayader CR).
func TestWebhookReconciler_RequeuesInsteadOfCreatingWhenListFails(t *testing.T) {
	fakeSrv := &fakeWebhookServer{
		listErr: status.Error(codes.Unavailable, "transient gateway failure"),
	}
	addr := startFakeWebhookGateway(t, fakeSrv)

	wh := newTestWebhook("bayader-webhook")
	r := newWebhookReconciler(t, wh, addr, record.NewFakeRecorder(10))

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if fakeSrv.createCalled {
		t.Fatal("Create MUST NOT be called when the adopt-by-URL List probe failed (fails open -> duplicate endpoints, invora/devops#109)")
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %v, want a positive backoff so the adopt probe is retried", res.RequeueAfter)
	}

	var got billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bayader-webhook"}, &got); err != nil {
		t.Fatalf("getting webhook: %v", err)
	}
	if got.Status.ExternalID != "" {
		t.Fatalf("ExternalID = %q, want empty (nothing was adopted or created)", got.Status.ExternalID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionFalse || synced.Reason != "AdoptProbeFailed" {
		t.Fatalf("Synced condition = %+v, want False/AdoptProbeFailed", synced)
	}
}

// TestWebhookReconciler_CreatesWhenListSucceedsWithNoMatch is the
// anti-regression counterpart: a SUCCESSFUL probe that finds no match must
// still create. This test must pass both before and after the fix — it is what
// proves the fail-closed guard did not swallow the legitimate create path.
func TestWebhookReconciler_CreatesWhenListSucceedsWithNoMatch(t *testing.T) {
	fakeSrv := &fakeWebhookServer{
		listResp: &webhookspb.ListResponse{
			Items: []*commonpb.BillingWebhookEndpoint{
				{Id: "unrelated-id", WebhookUrl: "https://example.invalid/other"},
			},
		},
		createFunc: func(req *webhookspb.CreateRequest) (*webhookspb.CreateResponse, error) {
			if req.GetWebhookUrl() != testWebhookURL {
				t.Fatalf("Create called with unexpected url %q", req.GetWebhookUrl())
			}
			return &webhookspb.CreateResponse{
				WebhookEndpoint: &commonpb.BillingWebhookEndpoint{Id: "new-wh-id", WebhookUrl: testWebhookURL},
			}, nil
		},
	}
	addr := startFakeWebhookGateway(t, fakeSrv)

	wh := newTestWebhook("bayader-webhook")
	r := newWebhookReconciler(t, wh, addr, record.NewFakeRecorder(10))

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if !fakeSrv.createCalled {
		t.Fatal("Create should have been called when the probe succeeded and found no URL match")
	}
	var got billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bayader-webhook"}, &got); err != nil {
		t.Fatalf("getting webhook: %v", err)
	}
	if got.Status.ExternalID != "new-wh-id" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "new-wh-id")
	}
}

// TestWebhookReconciler_AdoptsExistingByURL pins the "adopt by URL, never
// duplicate" contract (invora/devops#58) that #109 violated.
func TestWebhookReconciler_AdoptsExistingByURL(t *testing.T) {
	fakeSrv := &fakeWebhookServer{
		listResp: &webhookspb.ListResponse{
			Items: []*commonpb.BillingWebhookEndpoint{
				{Id: "b8ac3051", WebhookUrl: testWebhookURL},
			},
		},
	}
	addr := startFakeWebhookGateway(t, fakeSrv)

	wh := newTestWebhook("bayader-webhook")
	r := newWebhookReconciler(t, wh, addr, record.NewFakeRecorder(10))

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if fakeSrv.createCalled {
		t.Fatal("Create must not be called when an endpoint with the same URL already exists")
	}
	var got billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bayader-webhook"}, &got); err != nil {
		t.Fatalf("getting webhook: %v", err)
	}
	if got.Status.ExternalID != "b8ac3051" {
		t.Fatalf("ExternalID = %q, want %q", got.Status.ExternalID, "b8ac3051")
	}
}

// TestWebhookReconciler_WarnsWhenAdoptProbeSeesDuplicates implements
// invora/devops#109 suggestion 4: "a warning event when the adopt probe sees
// >1 URL match would make it visible". A CR reporting Ready=True/InSync while
// owning 1 of 9 identical records is why the duplication ran unnoticed for 11
// days.
func TestWebhookReconciler_WarnsWhenAdoptProbeSeesDuplicates(t *testing.T) {
	fakeSrv := &fakeWebhookServer{
		listResp: &webhookspb.ListResponse{
			Items: []*commonpb.BillingWebhookEndpoint{
				{Id: "b8ac3051", WebhookUrl: testWebhookURL},
				{Id: "5e9f9d82", WebhookUrl: testWebhookURL},
				{Id: "e8ff0ab5", WebhookUrl: testWebhookURL},
			},
		},
	}
	addr := startFakeWebhookGateway(t, fakeSrv)

	rec := record.NewFakeRecorder(10)
	wh := newTestWebhook("bayader-webhook")
	r := newWebhookReconciler(t, wh, addr, rec)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var events []string
	for {
		select {
		case e := <-rec.Events:
			events = append(events, e)
			continue
		default:
		}
		break
	}
	var found string
	for _, e := range events {
		if strings.Contains(e, "DuplicateWebhookEndpoints") {
			found = e
		}
	}
	if found == "" {
		t.Fatalf("expected a DuplicateWebhookEndpoints warning event when the adopt probe saw 3 URL matches; got events %v", events)
	}
	if !strings.HasPrefix(found, "Warning ") {
		t.Fatalf("duplicate event must be a Warning; got %q", found)
	}
	if !strings.Contains(found, "3") {
		t.Fatalf("duplicate event should report the match count; got %q", found)
	}
}

// TestWebhookReconciler_SkipsUpdateWhenRemoteMatchesSpec is the invora/devops#68
// change-detection guard for the webhook steady-state path: an already-adopted
// endpoint whose live URL matches spec and whose generation is already observed
// must NOT issue an Update RPC on every reconcile pass.
func TestWebhookReconciler_SkipsUpdateWhenRemoteMatchesSpec(t *testing.T) {
	fakeSrv := &fakeWebhookServer{
		getResp: &webhookspb.GetResponse{
			WebhookEndpoint: &commonpb.BillingWebhookEndpoint{Id: "b8ac3051", WebhookUrl: testWebhookURL},
		},
	}
	addr := startFakeWebhookGateway(t, fakeSrv)

	wh := newTestWebhook("bayader-webhook")
	r := newWebhookReconciler(t, wh, addr, record.NewFakeRecorder(10))

	// First pass adopts the generation into status.observedGeneration.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	}); err != nil {
		t.Fatalf("seeding Reconcile returned error: %v", err)
	}
	// Adopt the external id so the second pass takes the Get+Update branch.
	var seeded billingv1alpha1.InvoraBillingWebhookEndpoint
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bayader-webhook"}, &seeded); err != nil {
		t.Fatalf("getting webhook: %v", err)
	}
	seeded.Status.ExternalID = "b8ac3051"
	seeded.Status.ID = "b8ac3051"
	seeded.Status.ObservedGeneration = seeded.Generation
	if err := r.Status().Update(context.Background(), &seeded); err != nil {
		t.Fatalf("seeding status: %v", err)
	}
	fakeSrv.updateCalled = false

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if fakeSrv.updateCalled {
		t.Fatal("Update MUST NOT be issued when the live endpoint already matches spec and the generation is already observed (invora/devops#68)")
	}
}

// TestWebhookReconciler_StillUpdatesWhenRemoteDiffers is the anti-regression
// counterpart to the guard above: real remote drift must still be corrected.
func TestWebhookReconciler_StillUpdatesWhenRemoteDiffers(t *testing.T) {
	fakeSrv := &fakeWebhookServer{
		getResp: &webhookspb.GetResponse{
			WebhookEndpoint: &commonpb.BillingWebhookEndpoint{Id: "b8ac3051", WebhookUrl: "https://drifted.invalid/hook"},
		},
	}
	addr := startFakeWebhookGateway(t, fakeSrv)

	wh := newTestWebhook("bayader-webhook")
	wh.Status.ExternalID = "b8ac3051"
	wh.Status.ID = "b8ac3051"
	r := newWebhookReconciler(t, wh, addr, record.NewFakeRecorder(10))

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-webhook"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !fakeSrv.updateCalled {
		t.Fatal("Update MUST be issued when the live endpoint URL drifted away from spec")
	}
}
