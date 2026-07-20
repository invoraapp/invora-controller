package controller

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	paymentproviderspb "github.com/invoraapp/invora-controller/gen/invora/billing/payment_providers/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

// newTapReconcilerForTest wires a InvoraBillingTapProviderReconciler against a
// fake client preloaded with the given objects. Mirrors
// newSubscriberForTest in zitadelorgevent_subscriber_test.go for consistency.
func newTapReconcilerForTest(t *testing.T, objs ...runtime.Object) *InvoraBillingTapProviderReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("registering billing scheme: %v", err)
	}

	cb := fake.NewClientBuilder().
		WithScheme(scheme).
		// InvoraBillingTapProvider has subresource:status — without this,
		// the fake client doesn't persist Status.Conditions updates.
		WithStatusSubresource(&billingv1alpha1.InvoraBillingTapProvider{})
	if len(objs) > 0 {
		cb = cb.WithRuntimeObjects(objs...)
	}

	return &InvoraBillingTapProviderReconciler{
		BaseReconciler: BaseReconciler{
			Client:      cb.Build(),
			Scheme:      scheme,
			ClientCache: billingclient.NewCache(),
		},
	}
}

// TestInvoraBillingTapProvider_NotFound exercises the early-return on
// missing CR — the controller should silently succeed (no requeue, no
// error) so a deleted CR doesn't burn requeue cycles.
func TestInvoraBillingTapProvider_NotFound(t *testing.T) {
	r := newTapReconcilerForTest(t)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "ghost-tap"},
	})
	if err != nil {
		t.Fatalf("Reconcile on missing CR returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected zero requeue on missing CR, got %+v", result)
	}
}

// TestInvoraBillingTapProvider_AddsFinalizer asserts that the first
// reconcile attaches the standard billing finalizer and requeues
// immediately so the next pass can do the real work.
func TestInvoraBillingTapProvider_AddsFinalizer(t *testing.T) {
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "tap-test", Namespace: "billing-dev"},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "invora-org"},
			Code:                         "tap-prod",
			Name:                         "Tap (Production)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "tap-keys",
				Key:  "apiKey",
			},
		},
	}
	r := newTapReconcilerForTest(t, tap)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "tap-test"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !result.Requeue {
		t.Errorf("expected Requeue=true after finalizer add, got %+v", result)
	}

	got := &billingv1alpha1.InvoraBillingTapProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: "tap-test", Namespace: "billing-dev"}, got); err != nil {
		t.Fatalf("re-reading CR: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, billingv1alpha1.FinalizerName) {
		t.Errorf("expected finalizer %q after reconcile, finalizers=%v",
			billingv1alpha1.FinalizerName, got.Finalizers)
	}
}

// TestInvoraBillingTapProvider_RequeuesWhenOrgMissing exercises the
// dependency-resolution branch: when the referenced InvoraBillingOrganization
// doesn't exist (or isn't Ready), the controller sets the
// DependencyReady condition to False and schedules a fast requeue
// rather than failing.
func TestInvoraBillingTapProvider_RequeuesWhenOrgMissing(t *testing.T) {
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tap-test",
			Namespace:  "billing-dev",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "missing-org"},
			Code:                         "tap-prod",
			Name:                         "Tap (Production)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "tap-keys",
				Key:  "apiKey",
			},
		},
	}
	r := newTapReconcilerForTest(t, tap)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "tap-test"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter != DependencyRequeueInterval {
		t.Errorf("expected RequeueAfter=%v on missing org, got %+v",
			DependencyRequeueInterval, result)
	}

	got := &billingv1alpha1.InvoraBillingTapProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: "tap-test", Namespace: "billing-dev"}, got); err != nil {
		t.Fatalf("re-reading CR: %v", err)
	}
	var dep *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == billingv1alpha1.ConditionDependencyReady {
			dep = &got.Status.Conditions[i]
			break
		}
	}
	if dep == nil {
		t.Fatalf("expected %s condition to be set; got conditions=%+v",
			billingv1alpha1.ConditionDependencyReady, got.Status.Conditions)
	}
	if dep.Status != metav1.ConditionFalse {
		t.Errorf("DependencyReady status = %s, want False", dep.Status)
	}
}

// TestInvoraBillingTapProvider_DeletionRemovesFinalizer asserts that the
// upstream-no-destroy-mutation comment in the controller is honored:
// deletion drops the finalizer even though there's no billing-side delete
// call, so the CR can be garbage-collected without manual intervention.
func TestInvoraBillingTapProvider_DeletionRemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "tap-test",
			Namespace:         "billing-dev",
			Finalizers:        []string{billingv1alpha1.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "invora-org"},
			Code:                         "tap-prod",
			Name:                         "Tap (Production)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "tap-keys",
				Key:  "apiKey",
			},
		},
	}
	r := newTapReconcilerForTest(t, tap)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "billing-dev", Name: "tap-test"},
	}); err != nil {
		t.Fatalf("Reconcile on deletion: %v", err)
	}

	got := &billingv1alpha1.InvoraBillingTapProvider{}
	err := r.Get(ctx, types.NamespacedName{Name: "tap-test", Namespace: "billing-dev"}, got)
	if apierrors.IsNotFound(err) {
		// fake-client removes the object once the last finalizer is dropped — also OK.
		return
	}
	if err != nil {
		t.Fatalf("re-reading CR after deletion reconcile: %v", err)
	}
	if controllerutil.ContainsFinalizer(got, billingv1alpha1.FinalizerName) {
		t.Errorf("expected finalizer %q to be removed; finalizers=%v",
			billingv1alpha1.FinalizerName, got.Finalizers)
	}
}

// ---------------------------------------------------------------------------
// invora/invora-backend#209 — org-scoped adoption regression suite
//
// These drive the reconciler against a real (in-process) gRPC gateway so the
// Get/CreateTap/UpdateTap call sequence and the acting-org metadata are
// observable, mirroring fakePlansServer in plan_controller_test.go.
// ---------------------------------------------------------------------------

// tapNotFound is the status a gateway returns for a code the ACTING org cannot
// see. lago-api raises ActiveRecord::RecordNotFound from
// `org.payment_providers.find_by!(code:)` and the gruf interceptor maps it to
// GRPC::NotFound (config/initializers/gruf.rb).
var tapNotFound = status.Error(codes.NotFound, "Couldn't find PaymentProviders::BaseProvider")

// tapApiKeyMandatory reproduces what billing actually returns when UpdateTap
// targets an id the acting org cannot see: PaymentProviders::FindService misses
// (it scopes BaseProvider.where(organization_id:) first), TapService builds a
// NEW record, and UpdateTapRequest carries no api_key field to populate it.
var tapApiKeyMandatory = status.Error(codes.InvalidArgument,
	`Validation errors: {"api_key":["value_is_mandatory"]}`)

type fakePaymentProvidersServer struct {
	paymentproviderspb.UnimplementedPaymentProvidersServiceServer

	// getResp/getErr drive the by-code lookup in the acting org.
	getResp *paymentproviderspb.GetResponse
	getErr  error

	createFunc   func(*paymentproviderspb.CreateTapRequest) (*paymentproviderspb.CreateTapResponse, error)
	createCalled bool
	createReq    *paymentproviderspb.CreateTapRequest

	updateFunc   func(*paymentproviderspb.UpdateTapRequest) (*paymentproviderspb.UpdateTapResponse, error)
	updateCalled bool
	updateReq    *paymentproviderspb.UpdateTapRequest

	// Metadata is captured PER RPC, not into one shared field: the whole point
	// of #209 is which org the WRITE lands in, so asserting the header on the
	// lookup alone would prove nothing about the mutation.
	getMD    metadata.MD
	createMD metadata.MD
	updateMD metadata.MD
}

func (f *fakePaymentProvidersServer) Get(ctx context.Context, _ *paymentproviderspb.GetRequest) (*paymentproviderspb.GetResponse, error) {
	f.getMD, _ = metadata.FromIncomingContext(ctx)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResp, nil
}

func (f *fakePaymentProvidersServer) CreateTap(ctx context.Context, req *paymentproviderspb.CreateTapRequest) (*paymentproviderspb.CreateTapResponse, error) {
	f.createMD, _ = metadata.FromIncomingContext(ctx)
	f.createCalled = true
	f.createReq = req
	if f.createFunc != nil {
		return f.createFunc(req)
	}
	return nil, errors.New("CreateTap should not have been called")
}

func (f *fakePaymentProvidersServer) UpdateTap(ctx context.Context, req *paymentproviderspb.UpdateTapRequest) (*paymentproviderspb.UpdateTapResponse, error) {
	f.updateMD, _ = metadata.FromIncomingContext(ctx)
	f.updateCalled = true
	f.updateReq = req
	if f.updateFunc != nil {
		return f.updateFunc(req)
	}
	return nil, errors.New("UpdateTap should not have been called")
}

// assertActingOrg pins the acting-org headers on a single captured RPC.
// `x-invora-org-id` is trusted-internal: the WebGateway strips it and rewrites
// it to the token's HOME org, which is how the provider landed in the platform
// org in the first place, so it must never be stamped.
func assertActingOrg(t *testing.T, rpc string, md metadata.MD, wantOrgID string) {
	t.Helper()
	if md == nil {
		t.Fatalf("%s: no inbound metadata captured (was the RPC called?)", rpc)
	}
	if got := md.Get("x-zitadel-orgid"); len(got) != 1 || got[0] != wantOrgID {
		t.Fatalf("%s: x-zitadel-orgid = %v, want [%q]", rpc, got, wantOrgID)
	}
	if got := md.Get("x-invora-org-id"); len(got) != 0 {
		t.Fatalf("%s: x-invora-org-id must not be stamped (gateway strips it); got %v", rpc, got)
	}
}

func startFakeProvidersGateway(t *testing.T, srv *fakePaymentProvidersServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for fake gateway: %v", err)
	}
	s := grpc.NewServer()
	paymentproviderspb.RegisterPaymentProvidersServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String()
}

// tapProviderResponse builds the oneof-wrapped Get payload for a Tap provider.
func tapProviderResponse(id, code string) *paymentproviderspb.GetResponse {
	return &paymentproviderspb.GetResponse{
		PaymentProvider: &commonpb.PaymentProvider{
			Value: &commonpb.PaymentProvider_TapProvider{
				TapProvider: &commonpb.TapProvider{Id: id, Code: code},
			},
		},
	}
}

// newTapCR builds a reconcile-ready Tap CR: finalizer attached, org ref wired to
// the planTestFixtures org, api key Secret in its own namespace.
func newTapCR(storedProviderID string) *billingv1alpha1.InvoraBillingTapProvider {
	tap := &billingv1alpha1.InvoraBillingTapProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "bayader-tap",
			Namespace:  "default",
			Finalizers: []string{billingv1alpha1.FinalizerName},
		},
		Spec: billingv1alpha1.InvoraBillingTapProviderSpec{
			InvoraBillingOrganizationRef: billingv1alpha1.ResourceRef{Name: "test-org"},
			Code:                         "tap_bayader",
			Name:                         "Tap Payments (Bayader)",
			TapApiKeyRef: billingv1alpha1.SecretKeyRef{
				Name: "bayader-tap-credentials",
				Key:  "secretKey",
			},
			SuccessRedirectUrl: "https://dev-app-bayader.invora.app/payment/return",
		},
	}
	tap.Status.ProviderID = storedProviderID
	return tap
}

func tapApiKeySecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bayader-tap-credentials", Namespace: "default"},
		Data:       map[string][]byte{"secretKey": []byte("sk_test_bayader")},
	}
}

func newTapReconcilerWithGateway(t *testing.T, gatewayAddr string, tap *billingv1alpha1.InvoraBillingTapProvider) *InvoraBillingTapProviderReconciler {
	t.Helper()
	instance, org, instSecret := planTestFixtures(gatewayAddr)
	s := newPlanScheme(t)
	return &InvoraBillingTapProviderReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, tapApiKeySecret(), tap).
			WithStatusSubresource(tap).
			Build(),
		Scheme: s,
	}}
}

func reconcileTap(t *testing.T, r *InvoraBillingTapProviderReconciler) (billingv1alpha1.InvoraBillingTapProvider, ctrl.Result) {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "bayader-tap"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	var got billingv1alpha1.InvoraBillingTapProvider
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bayader-tap"}, &got); err != nil {
		t.Fatalf("re-reading Tap CR: %v", err)
	}
	return got, res
}

// TestTapReconciler_RecreatesWhenStoredProviderIdInvisibleInActingOrg is THE
// invora-backend#209 regression. The CR carries a providerId minted under a
// different org (releases <= 1.3.2 stamped x-invora-org-id, which the gateway
// strips and rewrites to the token's HOME/platform org). Once the acting org
// became the tenant, that id is invisible: billing's lookup is org-scoped, so
// UpdateTap built a NEW record and died on `api_key value_is_mandatory` forever,
// because UpdateTapRequest has no api_key field.
//
// The reconciler must instead resolve by code in the ACTING org, see nothing,
// drop the stale id, and CreateTap with the resolved api key.
func TestTapReconciler_RecreatesWhenStoredProviderIdInvisibleInActingOrg(t *testing.T) {
	fakeSrv := &fakePaymentProvidersServer{
		getErr: tapNotFound,
		createFunc: func(req *paymentproviderspb.CreateTapRequest) (*paymentproviderspb.CreateTapResponse, error) {
			return &paymentproviderspb.CreateTapResponse{
				TapProvider: &commonpb.TapProvider{Id: "tenant-scoped-id-8561ed9b", Code: req.GetCode()},
			}, nil
		},
		// Pre-fix behaviour: the reconciler skips the lookup and updates the
		// stale id, which billing rejects exactly like this.
		updateFunc: func(*paymentproviderspb.UpdateTapRequest) (*paymentproviderspb.UpdateTapResponse, error) {
			return nil, tapApiKeyMandatory
		},
	}
	addr := startFakeProvidersGateway(t, fakeSrv)

	// 73ce31ed... is the real platform-org row from the issue.
	_, org, _ := planTestFixtures(addr)
	r := newTapReconcilerWithGateway(t, addr, newTapCR("73ce31ed-9b56-4e1e-8941-bd44baac404f"))
	got, _ := reconcileTap(t, r)

	if fakeSrv.updateCalled {
		t.Fatal("UpdateTap must NOT be called for a provider id the acting org cannot see")
	}
	if !fakeSrv.createCalled {
		t.Fatal("CreateTap must be called when the acting org has no provider with this code")
	}
	if fakeSrv.createReq.GetApiKey() != "sk_test_bayader" {
		t.Fatalf("CreateTap api_key = %q, want the resolved Secret value", fakeSrv.createReq.GetApiKey())
	}
	if got.Status.ProviderID != "tenant-scoped-id-8561ed9b" {
		t.Fatalf("Status.ProviderID = %q, want the newly created tenant-scoped id", got.Status.ProviderID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "Created" {
		t.Fatalf("Synced condition = %+v, want True/Created", synced)
	}
	// The row must be MINTED in the tenant org, not merely looked up there.
	assertActingOrg(t, "Get", fakeSrv.getMD, org.Spec.ExternalID)
	assertActingOrg(t, "CreateTap", fakeSrv.createMD, org.Spec.ExternalID)
}

// TestTapReconciler_AdoptsProviderFromActingOrgAndRehomesStaleId covers the
// already-remediated state: dev has BOTH the stranded platform-org row
// (73ce31ed...) that status.providerId still points at AND a hand-created
// tenant-scoped row (8561ed9b...). The reconciler must adopt the row the acting
// org actually sees and update THAT id — never duplicate, never update the
// stale one.
func TestTapReconciler_AdoptsProviderFromActingOrgAndRehomesStaleId(t *testing.T) {
	fakeSrv := &fakePaymentProvidersServer{
		getResp: tapProviderResponse("tenant-scoped-id-8561ed9b", "tap_bayader"),
		updateFunc: func(req *paymentproviderspb.UpdateTapRequest) (*paymentproviderspb.UpdateTapResponse, error) {
			return &paymentproviderspb.UpdateTapResponse{
				TapProvider: &commonpb.TapProvider{Id: req.GetId(), Code: req.GetCode()},
			}, nil
		},
	}
	addr := startFakeProvidersGateway(t, fakeSrv)

	_, org, _ := planTestFixtures(addr)
	r := newTapReconcilerWithGateway(t, addr, newTapCR("73ce31ed-9b56-4e1e-8941-bd44baac404f"))
	got, _ := reconcileTap(t, r)

	if fakeSrv.createCalled {
		t.Fatal("CreateTap must NOT be called when the acting org already has a provider with this code")
	}
	if !fakeSrv.updateCalled {
		t.Fatal("UpdateTap must be called for the adopted provider")
	}
	if fakeSrv.updateReq.GetId() != "tenant-scoped-id-8561ed9b" {
		t.Fatalf("UpdateTap id = %q, want the id resolved in the acting org (not the stale one)",
			fakeSrv.updateReq.GetId())
	}
	if got.Status.ProviderID != "tenant-scoped-id-8561ed9b" {
		t.Fatalf("Status.ProviderID = %q, want the re-homed acting-org id", got.Status.ProviderID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue || synced.Reason != "InSync" {
		t.Fatalf("Synced condition = %+v, want True/InSync", synced)
	}
	assertActingOrg(t, "Get", fakeSrv.getMD, org.Spec.ExternalID)
	assertActingOrg(t, "UpdateTap", fakeSrv.updateMD, org.Spec.ExternalID)
}

// TestTapReconciler_CreatesWhenAbsentWithNoStoredId guards the greenfield path:
// no stored id, nothing in the acting org, so Create still runs with the api key.
func TestTapReconciler_CreatesWhenAbsentWithNoStoredId(t *testing.T) {
	fakeSrv := &fakePaymentProvidersServer{
		getErr: tapNotFound,
		createFunc: func(req *paymentproviderspb.CreateTapRequest) (*paymentproviderspb.CreateTapResponse, error) {
			return &paymentproviderspb.CreateTapResponse{
				TapProvider: &commonpb.TapProvider{Id: "fresh-id", Code: req.GetCode()},
			}, nil
		},
	}
	addr := startFakeProvidersGateway(t, fakeSrv)

	r := newTapReconcilerWithGateway(t, addr, newTapCR(""))
	got, _ := reconcileTap(t, r)

	if !fakeSrv.createCalled {
		t.Fatal("CreateTap must be called on the greenfield path")
	}
	if got.Status.ProviderID != "fresh-id" {
		t.Fatalf("Status.ProviderID = %q, want fresh-id", got.Status.ProviderID)
	}
}

// TestTapReconciler_LookupTransportErrorDoesNotRecreate is the safety
// counterpart: a lookup failure that is NOT NotFound (gateway down, auth
// rejected, Internal) must NOT be read as "absent", or every blip would mint a
// duplicate provider and clobber status.providerId.
func TestTapReconciler_LookupTransportErrorDoesNotRecreate(t *testing.T) {
	fakeSrv := &fakePaymentProvidersServer{
		getErr: status.Error(codes.Unavailable, "gateway down"),
	}
	addr := startFakeProvidersGateway(t, fakeSrv)

	r := newTapReconcilerWithGateway(t, addr, newTapCR("73ce31ed-9b56-4e1e-8941-bd44baac404f"))
	got, res := reconcileTap(t, r)

	if fakeSrv.createCalled || fakeSrv.updateCalled {
		t.Fatal("neither CreateTap nor UpdateTap may run when the lookup itself failed")
	}
	if got.Status.ProviderID != "73ce31ed-9b56-4e1e-8941-bd44baac404f" {
		t.Fatalf("Status.ProviderID = %q, must be preserved across a failed lookup", got.Status.ProviderID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionFalse || synced.Reason != "LookupFailed" {
		t.Fatalf("Synced condition = %+v, want False/LookupFailed", synced)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("RequeueAfter = %v, want 30s so a transient gateway failure retries", res.RequeueAfter)
	}
}

// TestTapReconciler_FailedCreatePreservesStaleProviderId guards the audit
// trail. The stale id is only dropped from a LOCAL, so a CreateTap that fails
// must leave status.providerId pointing at the row the CR used to own —
// otherwise the first failed create erases the only in-cluster link to the
// orphaned platform-org row and the operator loses the thread.
func TestTapReconciler_FailedCreatePreservesStaleProviderId(t *testing.T) {
	fakeSrv := &fakePaymentProvidersServer{
		getErr: tapNotFound,
		createFunc: func(*paymentproviderspb.CreateTapRequest) (*paymentproviderspb.CreateTapResponse, error) {
			return nil, status.Error(codes.Internal, "billing exploded")
		},
	}
	addr := startFakeProvidersGateway(t, fakeSrv)

	r := newTapReconcilerWithGateway(t, addr, newTapCR("73ce31ed-9b56-4e1e-8941-bd44baac404f"))
	got, _ := reconcileTap(t, r)

	if !fakeSrv.createCalled {
		t.Fatal("CreateTap should have been attempted")
	}
	if got.Status.ProviderID != "73ce31ed-9b56-4e1e-8941-bd44baac404f" {
		t.Fatalf("Status.ProviderID = %q, must survive a failed create", got.Status.ProviderID)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionFalse || synced.Reason != "CreateFailed" {
		t.Fatalf("Synced condition = %+v, want False/CreateFailed", synced)
	}
}

// TestTapReconciler_AssertsActingOrgViaZitadelOrgidHeader pins the org scope the
// whole fix depends on: the Tap path must assert the acting org with
// `x-zitadel-orgid: <spec.externalId>`. `x-invora-org-id` is trusted-internal —
// the WebGateway strips it and rewrites it to the token's HOME org, which is how
// the provider landed in the platform org in the first place.
func TestTapReconciler_AssertsActingOrgViaZitadelOrgidHeader(t *testing.T) {
	fakeSrv := &fakePaymentProvidersServer{
		getResp: tapProviderResponse("tenant-scoped-id-8561ed9b", "tap_bayader"),
		updateFunc: func(req *paymentproviderspb.UpdateTapRequest) (*paymentproviderspb.UpdateTapResponse, error) {
			return &paymentproviderspb.UpdateTapResponse{
				TapProvider: &commonpb.TapProvider{Id: req.GetId(), Code: req.GetCode()},
			}, nil
		},
	}
	addr := startFakeProvidersGateway(t, fakeSrv)

	_, org, _ := planTestFixtures(addr)
	r := newTapReconcilerWithGateway(t, addr, newTapCR(""))
	reconcileTap(t, r)

	assertActingOrg(t, "Get", fakeSrv.getMD, org.Spec.ExternalID)
	assertActingOrg(t, "UpdateTap", fakeSrv.updateMD, org.Spec.ExternalID)
}
