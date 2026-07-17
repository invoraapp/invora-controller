package controller

import (
	"context"
	"net"
	"regexp"
	"sync"
	"testing"
	"time"

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
	adminpb "github.com/invoraapp/invora-controller/gen/invora/admin/billing/v2"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	"github.com/invoraapp/invora-controller/internal/billingclient"
)

// guidPattern mimics .NET Guid.TryParse's canonical "D" form (8-4-4-4-12 hex),
// which is exactly what core's BillingOrgAdminGrpcService.ProvisionOrg validates
// (Guid.TryParse(request.TenantId)). Real Invora tenants carry their Zitadel
// org GUID (== ABP tenant id) in spec.externalId; numeric-snowflake / name-only
// platform + landing orgs never match this.
var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// fakeAdminServer is an in-process BillingOrgAdminService that records the
// tenant_id it receives and, when mimicCore is set, rejects non-GUID tenant_ids
// exactly like the real core service does — reproducing the CreateFailed the
// controller triggers today by sending org.Name instead of a GUID.
type fakeAdminServer struct {
	adminpb.UnimplementedBillingOrgAdminServiceServer

	mu                   sync.Mutex
	provisionTenantIDs   []string
	deprovisionTenantIDs []string
	listCalls            int
	getStatusTenantIDs   []string

	// behaviour knobs (set before serving)
	mimicCore    bool  // reject non-GUID tenant_id with InvalidArgument, like core
	provisionErr error // if set, ProvisionOrg returns this regardless of input

	// existingOrg, when set, is returned by GetOrgStatus for its TenantId,
	// modelling a tenant that already has a billing org (the adopt path). When
	// raceOnly is true it is withheld until ProvisionOrg has been attempted,
	// modelling a create/register race where the pre-check GetOrgStatus misses
	// but the post-AlreadyExists recovery finds it.
	existingOrg *commonpb.BillingOrg
	raceOnly    bool
}

// GetOrgStatus mirrors core's tenant-keyed lookup (FindByTenantIdAsync, which
// bypasses the IMultiTenant filter). Returns NotFound unless existingOrg matches
// the requested tenant_id — so tests without an existing org still provision.
func (s *fakeAdminServer) GetOrgStatus(_ context.Context, req *adminpb.GetOrgStatusRequest) (*adminpb.GetOrgStatusResponse, error) {
	s.mu.Lock()
	s.getStatusTenantIDs = append(s.getStatusTenantIDs, req.GetTenantId())
	provisionAttempted := len(s.provisionTenantIDs) > 0
	org := s.existingOrg
	s.mu.Unlock()

	if org != nil && org.GetTenantId() == req.GetTenantId() && (!s.raceOnly || provisionAttempted) {
		return &adminpb.GetOrgStatusResponse{Org: org}, nil
	}
	return nil, status.Error(codes.NotFound, "Tenant not found.")
}

func (s *fakeAdminServer) ProvisionOrg(_ context.Context, req *adminpb.ProvisionOrgRequest) (*adminpb.ProvisionOrgResponse, error) {
	s.mu.Lock()
	s.provisionTenantIDs = append(s.provisionTenantIDs, req.GetTenantId())
	s.mu.Unlock()

	if s.provisionErr != nil {
		return nil, s.provisionErr
	}
	if s.mimicCore {
		if req.GetTenantId() == "" {
			return nil, status.Error(codes.InvalidArgument, "tenant_id is required.")
		}
		if !guidPattern.MatchString(req.GetTenantId()) {
			return nil, status.Error(codes.InvalidArgument, "tenant_id must be a valid GUID.")
		}
	}
	return &adminpb.ProvisionOrgResponse{
		Org: &commonpb.BillingOrg{
			OrganizationId: "org-" + req.GetTenantId(),
			TenantId:       req.GetTenantId(),
			DisplayName:    req.GetDisplayName(),
		},
	}, nil
}

func (s *fakeAdminServer) DeprovisionOrg(_ context.Context, req *adminpb.DeprovisionOrgRequest) (*adminpb.DeprovisionOrgResponse, error) {
	s.mu.Lock()
	s.deprovisionTenantIDs = append(s.deprovisionTenantIDs, req.GetTenantId())
	s.mu.Unlock()
	if s.mimicCore && !guidPattern.MatchString(req.GetTenantId()) {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a valid GUID.")
	}
	return &adminpb.DeprovisionOrgResponse{}, nil
}

func (s *fakeAdminServer) ListOrgs(_ context.Context, _ *adminpb.ListOrgsRequest) (*adminpb.ListOrgsResponse, error) {
	s.mu.Lock()
	s.listCalls++
	s.mu.Unlock()
	return &adminpb.ListOrgsResponse{}, nil
}

func (s *fakeAdminServer) provisions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.provisionTenantIDs))
	copy(out, s.provisionTenantIDs)
	return out
}

// startFakeAdminServer serves fake on an ephemeral localhost port and returns
// the "host:port" target (no scheme → gateway.Target dials it insecurely).
func startFakeAdminServer(t *testing.T, fake *fakeAdminServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	adminpb.RegisterBillingOrgAdminServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// newOrgReconcilerForTest wires an InvoraBillingOrganizationReconciler against a
// fake client preloaded with a Ready InvoraBillingInstance (pointing at
// gatewayTarget), its token secret, and the given org.
func newOrgReconcilerForTest(t *testing.T, gatewayTarget string, org *billingv1alpha1.InvoraBillingOrganization) *InvoraBillingOrganizationReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("registering billing scheme: %v", err)
	}

	instance := &billingv1alpha1.InvoraBillingInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "invora-dev", Namespace: org.Namespace},
		Spec: billingv1alpha1.InvoraBillingInstanceSpec{
			GatewayURL: gatewayTarget, // bare host:port → insecure passthrough
			TokenRef:   billingv1alpha1.SecretKeyRef{Name: "sa-token", Key: "token"},
		},
		Status: billingv1alpha1.InvoraBillingInstanceStatus{
			BillingResourceStatus: billingv1alpha1.BillingResourceStatus{
				Conditions: []metav1.Condition{{
					Type:               billingv1alpha1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             "Connected",
					Message:            "ready for test",
					LastTransitionTime: metav1.Now(),
				}},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-token", Namespace: org.Namespace},
		Data:       map[string][]byte{"token": []byte("super-admin-token")},
	}

	cb := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(instance, secret, org).
		WithStatusSubresource(
			&billingv1alpha1.InvoraBillingInstance{},
			&billingv1alpha1.InvoraBillingOrganization{},
		)

	return &InvoraBillingOrganizationReconciler{
		BaseReconciler: BaseReconciler{
			Client:      cb.Build(),
			Scheme:      scheme,
			ClientCache: billingclient.NewCache(),
		},
	}
}

func orgCR(name, externalID string) *billingv1alpha1.InvoraBillingOrganization {
	return &billingv1alpha1.InvoraBillingOrganization{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "invora-controller",
			Finalizers: []string{billingv1alpha1.FinalizerName}, // skip the add-finalizer requeue
		},
		Spec: billingv1alpha1.InvoraBillingOrganizationSpec{
			InstanceRef: billingv1alpha1.ResourceRef{Name: "invora-dev"},
			Name:        "Display " + name,
			ExternalID:  externalID,
		},
	}
}

func syncedCondition(t *testing.T, r *InvoraBillingOrganizationReconciler, name string) metav1.Condition {
	t.Helper()
	got := &billingv1alpha1.InvoraBillingOrganization{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "invora-controller"}, got); err != nil {
		t.Fatalf("re-reading org: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == billingv1alpha1.ConditionSynced {
			return c
		}
	}
	t.Fatalf("no Synced condition set; conditions=%+v", got.Status.Conditions)
	return metav1.Condition{}
}

func conditionOfType(t *testing.T, r *InvoraBillingOrganizationReconciler, name, condType string) metav1.Condition {
	t.Helper()
	got := &billingv1alpha1.InvoraBillingOrganization{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "invora-controller"}, got); err != nil {
		t.Fatalf("re-reading org: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == condType {
			return c
		}
	}
	t.Fatalf("no %s condition set; conditions=%+v", condType, got.Status.Conditions)
	return metav1.Condition{}
}

// TestOrg_ExistingOrg_Adopts_NoProvision is the bayader blocker directly: a
// tenant that already HAS a billing org (best-effort-provisioned at
// self-registration) must be ADOPTED via GetOrgStatus, never re-provisioned.
//
// Pre-fix this FAILS: the old ListOrgs scan is blind to the tenant's org
// (core's ListOrgs runs through the IMultiTenant LINQ filter and, in host
// context, returns only host-owned rows), so every reconcile fell through to
// ProvisionOrg -> AlreadyExists and wedged the org — and its dependent
// InvoraBillingPlan / InvoraBillingTapProvider CRs — on CreateFailed.
func TestOrg_ExistingOrg_Adopts_NoProvision(t *testing.T) {
	const guid = "f84623f4-bff4-4313-b68c-3a2283e5c92d" // bayader's tenant GUID
	fs := &fakeAdminServer{
		mimicCore:   true,
		existingOrg: &commonpb.BillingOrg{OrganizationId: "org-" + guid, TenantId: guid, DisplayName: "Bayader"},
	}
	target := startFakeAdminServer(t, fs)

	org := orgCR("bayader", guid)
	r := newOrgReconcilerForTest(t, target, org)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "invora-controller", Name: org.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := fs.provisions(); len(got) != 0 {
		t.Fatalf("ProvisionOrg called with %v; want adoption of the existing org with NO provisioning", got)
	}
	if c := syncedCondition(t, r, org.Name); c.Status != metav1.ConditionTrue || c.Reason != "Adopted" {
		t.Fatalf("Synced = %s/%s, want True/Adopted", c.Status, c.Reason)
	}
	// Ready=True is what unblocks the dependent Plan/TapProvider CRs
	// (getReadyBillingOrganization gates on it).
	if c := conditionOfType(t, r, org.Name, billingv1alpha1.ConditionReady); c.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %s, want True (so dependent Plan/TapProvider CRs unblock)", c.Status)
	}
	got := &billingv1alpha1.InvoraBillingOrganization{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: org.Name, Namespace: "invora-controller"}, got); err != nil {
		t.Fatalf("re-reading org: %v", err)
	}
	if got.Status.OrganizationID != "org-"+guid {
		t.Fatalf("Status.OrganizationID = %q, want the adopted org id org-%s", got.Status.OrganizationID, guid)
	}
	if result.RequeueAfter != DefaultRequeueInterval {
		t.Errorf("RequeueAfter = %v, want steady-state %v", result.RequeueAfter, DefaultRequeueInterval)
	}
}

// TestOrg_ProvisionAlreadyExists_Adopts covers the create/register race: the
// pre-check GetOrgStatus misses (NotFound), ProvisionOrg then races the tenant's
// own self-registration and returns AlreadyExists. The controller must recover
// by re-fetching via GetOrgStatus and adopting — not surface CreateFailed.
func TestOrg_ProvisionAlreadyExists_Adopts(t *testing.T) {
	const guid = "3a16dbef-dead-beef-cafe-444455556666"
	fs := &fakeAdminServer{
		provisionErr: status.Error(codes.AlreadyExists,
			"A billing organization already exists for tenant '"+guid+"'."),
		existingOrg: &commonpb.BillingOrg{OrganizationId: "org-" + guid, TenantId: guid, DisplayName: "Racer"},
		raceOnly:    true,
	}
	target := startFakeAdminServer(t, fs)

	org := orgCR("tenant-"+guid, guid)
	r := newOrgReconcilerForTest(t, target, org)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "invora-controller", Name: org.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error on AlreadyExists; want silent adoption: %v", err)
	}
	if got := fs.provisions(); len(got) != 1 {
		t.Fatalf("ProvisionOrg calls = %v, want exactly one attempt before adopting on AlreadyExists", got)
	}
	if c := syncedCondition(t, r, org.Name); c.Status != metav1.ConditionTrue || c.Reason != "Adopted" {
		t.Fatalf("Synced = %s/%s, want True/Adopted", c.Status, c.Reason)
	}
	if c := conditionOfType(t, r, org.Name, billingv1alpha1.ConditionReady); c.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %s, want True", c.Status)
	}
}

// TestOrg_GuidTenant_SendsGuid_Provisions: a real tenant CR (name tenant-<guid>,
// spec.externalId == the Zitadel org GUID) must provision the billing org using
// the GUID as tenant_id — NOT the K8s object name.
//
// Pre-fix this FAILS: the controller sends org.Name ("tenant-<guid>"), core
// rejects it as a non-GUID, and Synced flips to CreateFailed.
func TestOrg_GuidTenant_SendsGuid_Provisions(t *testing.T) {
	const guid = "3a16dbef-1111-2222-3333-444455556666"
	fs := &fakeAdminServer{mimicCore: true}
	target := startFakeAdminServer(t, fs)

	org := orgCR("tenant-"+guid, guid)
	r := newOrgReconcilerForTest(t, target, org)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "invora-controller", Name: org.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := fs.provisions(); len(got) != 1 || got[0] != guid {
		t.Fatalf("ProvisionOrg tenant_id = %v, want exactly [%s] (the GUID, not org.Name)", got, guid)
	}
	if c := syncedCondition(t, r, org.Name); c.Status != metav1.ConditionTrue {
		t.Fatalf("Synced = %s/%s, want True/Created", c.Status, c.Reason)
	}
	if result.RequeueAfter != DefaultRequeueInterval {
		t.Errorf("RequeueAfter = %v, want steady-state %v", result.RequeueAfter, DefaultRequeueInterval)
	}
}

// TestOrg_NonGuidTenant_Skips_NoStorm: a platform / numeric-snowflake org whose
// externalId is not a GUID is not a billing tenant. It must be skipped
// (Synced=True, reason SkippedNoTenantGuid) with NO ProvisionOrg call and NO
// requeue — i.e. the 30s storm is gone.
//
// Pre-fix this FAILS: the controller sends org.Name, core returns
// InvalidArgument, Synced=CreateFailed, RequeueAfter=30s (the storm).
func TestOrg_NonGuidTenant_Skips_NoStorm(t *testing.T) {
	fs := &fakeAdminServer{mimicCore: true}
	target := startFakeAdminServer(t, fs)

	org := orgCR("tenant-369339722666935201", "369339722666935201")
	r := newOrgReconcilerForTest(t, target, org)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "invora-controller", Name: org.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if got := fs.provisions(); len(got) != 0 {
		t.Fatalf("ProvisionOrg called with %v; want NO provisioning for a non-GUID/platform org", got)
	}
	if c := syncedCondition(t, r, org.Name); c.Status != metav1.ConditionTrue || c.Reason != "SkippedNoTenantGuid" {
		t.Fatalf("Synced = %s/%s, want True/SkippedNoTenantGuid", c.Status, c.Reason)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 (no storm)", result.RequeueAfter)
	}
}

// TestOrg_ProvisionError_ReturnsErrForBackoff: a genuine transient gRPC error on
// ProvisionOrg must be returned so controller-runtime applies capped exponential
// backoff — NOT swallowed into a bare RequeueAfter:30s with nil error.
//
// Pre-fix this FAILS: the controller returns (RequeueAfter:30s, nil).
func TestOrg_ProvisionError_ReturnsErrForBackoff(t *testing.T) {
	const guid = "3a16dbef-aaaa-bbbb-cccc-444455556666"
	fs := &fakeAdminServer{provisionErr: status.Error(codes.Unavailable, "gateway down")}
	target := startFakeAdminServer(t, fs)

	org := orgCR("tenant-"+guid, guid)
	r := newOrgReconcilerForTest(t, target, org)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "invora-controller", Name: org.Name},
	})
	if err == nil {
		t.Fatalf("Reconcile returned nil error on a transient gRPC failure; want the error returned so controller-runtime backs off")
	}
	if c := syncedCondition(t, r, org.Name); c.Status != metav1.ConditionFalse {
		t.Errorf("Synced = %s/%s, want False/CreateFailed", c.Status, c.Reason)
	}
}

// TestResolveTenantID covers the GUID gate directly: real tenants carry the
// Zitadel org GUID in spec.externalId; platform/numeric/name orgs do not.
func TestResolveTenantID(t *testing.T) {
	cases := []struct {
		name       string
		externalID string
		wantID     string
		wantOK     bool
	}{
		{"canonical guid", "3a16dbef-1111-2222-3333-444455556666", "3a16dbef-1111-2222-3333-444455556666", true},
		{"uppercase guid", "3A16DBEF-1111-2222-3333-444455556666", "3A16DBEF-1111-2222-3333-444455556666", true},
		{"guid with surrounding space", "  3a16dbef-1111-2222-3333-444455556666  ", "3a16dbef-1111-2222-3333-444455556666", true},
		{"numeric snowflake", "369339722666935201", "", false},
		{"empty", "", "", false},
		{"tenant-prefixed name", "tenant-369339722666935201", "", false},
		{"too short", "3a16dbef-1111-2222-3333-4444", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			org := &billingv1alpha1.InvoraBillingOrganization{
				Spec: billingv1alpha1.InvoraBillingOrganizationSpec{ExternalID: tc.externalID},
			}
			id, ok := resolveTenantID(org)
			if ok != tc.wantOK || id != tc.wantID {
				t.Fatalf("resolveTenantID(%q) = (%q,%v), want (%q,%v)", tc.externalID, id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

// keep time import used if the file evolves; harmless no-op reference.
var _ = time.Second
