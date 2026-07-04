package controller

import (
	"context"
	"net"
	"sort"
	"sync"
	"testing"

	"google.golang.org/grpc"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	subscriptionspb "github.com/invoraapp/invora-controller/gen/invora/billing/subscriptions/v2"
	"github.com/invoraapp/invora-controller/internal/gateway"
)

// fakeEntitlementsServer is an in-process SubscriptionsService implementing
// only the entitlement RPCs. It tracks the remote entitlement set as a plain
// map keyed by feature code, mimicking the billing backend's view of
// "what entitlements currently exist on this subscription" — independent of
// which ones any particular CR declared.
type fakeEntitlementsServer struct {
	subscriptionspb.UnimplementedSubscriptionsServiceServer

	mu          sync.Mutex
	codes       map[string]bool
	upsertCalls []string
	removeCalls []string
}

func newFakeEntitlementsServer(initialCodes ...string) *fakeEntitlementsServer {
	s := &fakeEntitlementsServer{codes: map[string]bool{}}
	for _, c := range initialCodes {
		s.codes[c] = true
	}
	return s
}

func (s *fakeEntitlementsServer) CreateOrUpdateEntitlement(_ context.Context, req *subscriptionspb.CreateOrUpdateEntitlementRequest) (*subscriptionspb.CreateOrUpdateEntitlementResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := req.GetEntitlement().GetFeatureCode()
	s.upsertCalls = append(s.upsertCalls, code)
	s.codes[code] = true
	return &subscriptionspb.CreateOrUpdateEntitlementResponse{
		Entitlement: &subscriptionspb.SubscriptionEntitlement{Code: code},
	}, nil
}

func (s *fakeEntitlementsServer) ListEntitlements(_ context.Context, _ *subscriptionspb.ListEntitlementsRequest) (*subscriptionspb.ListEntitlementsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*subscriptionspb.SubscriptionEntitlement, 0, len(s.codes))
	for code := range s.codes {
		items = append(items, &subscriptionspb.SubscriptionEntitlement{Code: code})
	}
	return &subscriptionspb.ListEntitlementsResponse{Items: items}, nil
}

func (s *fakeEntitlementsServer) RemoveEntitlement(_ context.Context, req *subscriptionspb.RemoveEntitlementRequest) (*subscriptionspb.RemoveEntitlementResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := req.GetFeatureCode()
	s.removeCalls = append(s.removeCalls, code)
	delete(s.codes, code)
	fc := code
	return &subscriptionspb.RemoveEntitlementResponse{FeatureCode: &fc}, nil
}

func (s *fakeEntitlementsServer) snapshot() (upserts, removes, remaining []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upserts = append(upserts, s.upsertCalls...)
	removes = append(removes, s.removeCalls...)
	for code := range s.codes {
		remaining = append(remaining, code)
	}
	sort.Strings(remaining)
	return
}

// startFakeEntitlementsServer serves fake on an ephemeral localhost port and
// returns a live SubscriptionsServiceClient dialed against it.
func startFakeEntitlementsServer(t *testing.T, fake *fakeEntitlementsServer) subscriptionspb.SubscriptionsServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	subscriptionspb.RegisterSubscriptionsServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// Bare host:port (no scheme) → gateway.Dial passes it through as an
	// insecure target, same convention used in organization_controller_test.go.
	conn, err := gateway.Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return subscriptionspb.NewSubscriptionsServiceClient(conn)
}

func subWithEntitlements(managedCodes []string, entitlements ...billingv1alpha1.SubscriptionEntitlement) *billingv1alpha1.InvoraBillingSubscription {
	sub := &billingv1alpha1.InvoraBillingSubscription{
		Spec: billingv1alpha1.InvoraBillingSubscriptionSpec{
			Entitlements: entitlements,
		},
		Status: billingv1alpha1.InvoraBillingSubscriptionStatus{
			ExternalID: "sub-ext-1",
		},
	}
	sub.Status.ManagedEntitlementCodes = managedCodes
	return sub
}

// TestReconcileEntitlements_UpsertsDeclaredEntitlements: every entry in
// spec.entitlements must be applied via CreateOrUpdateEntitlement, and the
// applied feature codes must be recorded in status for future guarded prunes.
func TestReconcileEntitlements_UpsertsDeclaredEntitlements(t *testing.T) {
	fs := newFakeEntitlementsServer()
	svc := startFakeEntitlementsServer(t, fs)

	sub := subWithEntitlements(nil,
		billingv1alpha1.SubscriptionEntitlement{
			FeatureCode: "connected_business",
			Privileges: []billingv1alpha1.EntitlementPrivilege{
				{PrivilegeCode: "max_connections", Value: "5"},
			},
		},
	)

	r := &InvoraBillingSubscriptionReconciler{}
	if err := r.reconcileEntitlements(context.Background(), svc, sub); err != nil {
		t.Fatalf("reconcileEntitlements returned error: %v", err)
	}

	upserts, removes, remaining := fs.snapshot()
	if len(upserts) != 1 || upserts[0] != "connected_business" {
		t.Fatalf("upsertCalls = %v, want exactly [connected_business]", upserts)
	}
	if len(removes) != 0 {
		t.Fatalf("removeCalls = %v, want none (first apply, nothing to prune)", removes)
	}
	if len(remaining) != 1 || remaining[0] != "connected_business" {
		t.Fatalf("remote entitlements = %v, want [connected_business]", remaining)
	}
	if got := sub.Status.ManagedEntitlementCodes; len(got) != 1 || got[0] != "connected_business" {
		t.Fatalf("Status.ManagedEntitlementCodes = %v, want [connected_business]", got)
	}
}

// TestReconcileEntitlements_PrunesDroppedManagedEntitlement: a feature code
// this CR previously applied (tracked in status) and has since removed from
// spec must be removed from the remote subscription.
func TestReconcileEntitlements_PrunesDroppedManagedEntitlement(t *testing.T) {
	fs := newFakeEntitlementsServer("connected_business", "seats")
	svc := startFakeEntitlementsServer(t, fs)

	// Previously this CR managed both codes; now spec only declares "seats".
	sub := subWithEntitlements(
		[]string{"connected_business", "seats"},
		billingv1alpha1.SubscriptionEntitlement{FeatureCode: "seats"},
	)

	r := &InvoraBillingSubscriptionReconciler{}
	if err := r.reconcileEntitlements(context.Background(), svc, sub); err != nil {
		t.Fatalf("reconcileEntitlements returned error: %v", err)
	}

	_, removes, remaining := fs.snapshot()
	if len(removes) != 1 || removes[0] != "connected_business" {
		t.Fatalf("removeCalls = %v, want exactly [connected_business]", removes)
	}
	if len(remaining) != 1 || remaining[0] != "seats" {
		t.Fatalf("remote entitlements = %v, want [seats]", remaining)
	}
	if got := sub.Status.ManagedEntitlementCodes; len(got) != 1 || got[0] != "seats" {
		t.Fatalf("Status.ManagedEntitlementCodes = %v, want [seats]", got)
	}
}

// TestReconcileEntitlements_NeverPrunesUnmanagedEntitlement: an entitlement
// that exists on the remote subscription but was NEVER declared by this CR
// (e.g. a connected_business grant applied out-of-band directly against the
// billing backend) must survive reconciliation even though it's absent from
// spec.entitlements — guarding against blind deletion of out-of-band grants.
func TestReconcileEntitlements_NeverPrunesUnmanagedEntitlement(t *testing.T) {
	fs := newFakeEntitlementsServer("out_of_band_grant", "seats")
	svc := startFakeEntitlementsServer(t, fs)

	// This CR previously managed only "seats" (out_of_band_grant was never
	// declared here) and continues to declare only "seats".
	sub := subWithEntitlements(
		[]string{"seats"},
		billingv1alpha1.SubscriptionEntitlement{FeatureCode: "seats"},
	)

	r := &InvoraBillingSubscriptionReconciler{}
	if err := r.reconcileEntitlements(context.Background(), svc, sub); err != nil {
		t.Fatalf("reconcileEntitlements returned error: %v", err)
	}

	_, removes, remaining := fs.snapshot()
	if len(removes) != 0 {
		t.Fatalf("removeCalls = %v, want none — out_of_band_grant was never managed by this CR", removes)
	}
	sort.Strings(remaining)
	want := []string{"out_of_band_grant", "seats"}
	if len(remaining) != len(want) || remaining[0] != want[0] || remaining[1] != want[1] {
		t.Fatalf("remote entitlements = %v, want %v (out-of-band grant untouched)", remaining, want)
	}
}

// TestReconcileEntitlements_EmptySpec_NoManagedHistory_NoOpsAtAll: a subscription
// with no entitlements ever declared (spec.entitlements nil, status empty) must
// not call ListEntitlements/RemoveEntitlement at all — avoids any risk of
// touching unrelated remote entitlements when this feature is unused.
func TestReconcileEntitlements_EmptySpec_NoManagedHistory_NoOpsAtAll(t *testing.T) {
	fs := newFakeEntitlementsServer("some_other_grant")
	svc := startFakeEntitlementsServer(t, fs)

	sub := subWithEntitlements(nil)

	r := &InvoraBillingSubscriptionReconciler{}
	if err := r.reconcileEntitlements(context.Background(), svc, sub); err != nil {
		t.Fatalf("reconcileEntitlements returned error: %v", err)
	}

	upserts, removes, remaining := fs.snapshot()
	if len(upserts) != 0 || len(removes) != 0 {
		t.Fatalf("expected no upsert/remove calls, got upserts=%v removes=%v", upserts, removes)
	}
	if len(remaining) != 1 || remaining[0] != "some_other_grant" {
		t.Fatalf("remote entitlements = %v, want untouched [some_other_grant]", remaining)
	}
	if len(sub.Status.ManagedEntitlementCodes) != 0 {
		t.Fatalf("Status.ManagedEntitlementCodes = %v, want empty", sub.Status.ManagedEntitlementCodes)
	}
}
