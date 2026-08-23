package controller

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
)

func (f *fakePlansServer) Get(_ context.Context, _ *planspb.GetRequest) (*planspb.GetResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResp, nil
}

func (f *fakePlansServer) Update(_ context.Context, req *planspb.UpdateRequest) (*planspb.UpdateResponse, error) {
	f.updateCalled = true
	f.lastUpdate = req
	return &planspb.UpdateResponse{
		Plan: &commonpb.BillingPlan{Id: req.GetId(), Code: req.GetCode()},
	}, nil
}

func newPlanReconciler(t *testing.T, plan *billingv1alpha1.InvoraBillingPlan, gatewayAddr string) *InvoraBillingPlanReconciler {
	t.Helper()
	instance, org, instSecret := planTestFixtures(gatewayAddr)
	s := newPlanScheme(t)
	return &InvoraBillingPlanReconciler{BaseReconciler: BaseReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(instance, org, instSecret, plan).
			WithStatusSubresource(plan).
			Build(),
		Scheme: s,
	}}
}

// liveFreePlan is the remote representation of newTestPlan("free") when it is
// fully in sync — same code/name/amount/currency/interval/payInAdvance and no
// charges or taxes.
func liveFreePlan(id string) *commonpb.BillingPlan {
	return &commonpb.BillingPlan{
		Id:             id,
		Code:           "free",
		Name:           "Free",
		AmountCents:    0,
		AmountCurrency: commonpb.CurrencyEnum_CURRENCY_ENUM_SAR,
		Interval:       commonpb.PlanInterval_PLAN_INTERVAL_MONTHLY,
		PayInAdvance:   true,
	}
}

// TestPlanReconciler_RequeuesInsteadOfCreatingWhenListFails is the second site
// of the invora/devops#109 fail-open bug: plan_controller.go had the identical
// `if listResp, err := svc.List(...); err == nil { ... }` shape, so a transient
// gateway failure of the adopt-by-code probe fell through to Create and would
// duplicate the plan catalog (or hard-fail with value_already_exist).
func TestPlanReconciler_RequeuesInsteadOfCreatingWhenListFails(t *testing.T) {
	fakeSrv := &fakePlansServer{
		listErr: status.Error(codes.Unavailable, "transient gateway failure"),
	}
	addr := startFakePlansGateway(t, fakeSrv)

	plan := newTestPlan("free")
	r := newPlanReconciler(t, plan, addr)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if fakeSrv.createCalled {
		t.Fatal("Create MUST NOT be called when the adopt-by-code List probe failed (fails open -> duplicate/conflicting plans)")
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %v, want a positive backoff so the adopt probe is retried", res.RequeueAfter)
	}

	var got billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "free"}, &got); err != nil {
		t.Fatalf("getting plan: %v", err)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionFalse || synced.Reason != "AdoptProbeFailed" {
		t.Fatalf("Synced condition = %+v, want False/AdoptProbeFailed", synced)
	}
}

// TestPlanReconciler_SkipsUpdateWhenRemoteMatchesSpec is the invora/devops#68
// change-detection guard. Steady state on dev was 330,415 PlansService/Update
// calls in 14 days (~16 WRITES/min) because the reconciler issued an Update on
// EVERY pass with no desired-vs-observed comparison.
func TestPlanReconciler_SkipsUpdateWhenRemoteMatchesSpec(t *testing.T) {
	fakeSrv := &fakePlansServer{
		getResp: &planspb.GetResponse{Plan: liveFreePlan("existing-plan-id-123")},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	plan := newTestPlan("free")
	r := newPlanReconciler(t, plan, addr)

	// Pass 1 establishes status (externalId + observedGeneration).
	seedPlanStatus(t, r, "free", "existing-plan-id-123")
	fakeSrv.updateCalled = false

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if fakeSrv.updateCalled {
		t.Fatal("Update MUST NOT be issued when the live plan already matches spec and the generation is already observed (invora/devops#68)")
	}

	var got billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "free"}, &got); err != nil {
		t.Fatalf("getting plan: %v", err)
	}
	synced := meta.FindStatusCondition(got.Status.Conditions, billingv1alpha1.ConditionSynced)
	if synced == nil || synced.Status != metav1.ConditionTrue {
		t.Fatalf("Synced condition = %+v, want True (skipping a redundant Update is still 'in sync')", synced)
	}
}

// TestPlanReconciler_StillUpdatesWhenRemoteDiffers is the anti-regression
// counterpart: the short-circuit must never freeze real drift. It must pass
// both before and after the fix.
func TestPlanReconciler_StillUpdatesWhenRemoteDiffers(t *testing.T) {
	drifted := liveFreePlan("existing-plan-id-123")
	drifted.AmountCents = 9900
	drifted.Name = "Free (edited in Lago)"

	fakeSrv := &fakePlansServer{
		getResp: &planspb.GetResponse{Plan: drifted},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	plan := newTestPlan("free")
	r := newPlanReconciler(t, plan, addr)
	seedPlanStatus(t, r, "free", "existing-plan-id-123")
	fakeSrv.updateCalled = false

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if !fakeSrv.updateCalled {
		t.Fatal("Update MUST be issued when the live plan drifted away from spec")
	}
	if fakeSrv.lastUpdate.GetAmountCents() != 0 || fakeSrv.lastUpdate.GetName() != "Free" {
		t.Fatalf("Update request did not carry the CR's desired values: %+v", fakeSrv.lastUpdate)
	}
}

// TestPlanReconciler_StillUpdatesWhenSpecGenerationIsUnobserved guards the
// other half of the short-circuit: a spec edit (generation bump) must always
// reach the backend even if the comparable scalar fields happen to match — the
// remote representation does not echo every field the controller writes
// (charges are returned in a different shape; the webhook signature algo is not
// returned at all), so generation is the authoritative "spec changed" signal.
func TestPlanReconciler_StillUpdatesWhenSpecGenerationIsUnobserved(t *testing.T) {
	fakeSrv := &fakePlansServer{
		getResp: &planspb.GetResponse{Plan: liveFreePlan("existing-plan-id-123")},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	plan := newTestPlan("free")
	r := newPlanReconciler(t, plan, addr)
	seedPlanStatus(t, r, "free", "existing-plan-id-123")

	// Simulate an unobserved spec edit: observedGeneration falls behind.
	var cur billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "free"}, &cur); err != nil {
		t.Fatalf("getting plan: %v", err)
	}
	cur.Status.ObservedGeneration = cur.Generation - 1
	if err := r.Status().Update(context.Background(), &cur); err != nil {
		t.Fatalf("seeding stale observedGeneration: %v", err)
	}
	fakeSrv.updateCalled = false

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !fakeSrv.updateCalled {
		t.Fatal("Update MUST be issued when status.observedGeneration is behind metadata.generation")
	}
}

// TestPlanReconciler_SkipsNoOpStatusWriteOnSteadyState is the second half of
// invora/devops#68: setSuccessStatus stamps LastSyncedAt with time.Now() on
// every pass, so an unconditional r.Status().Update() bumped resourceVersion
// every 5 minutes, fired the controller's OWN watch, and re-enqueued the object
// outside its requeue timer — a self-sustaining loop. A steady-state pass that
// changes nothing must not write status at all.
func TestPlanReconciler_SkipsNoOpStatusWriteOnSteadyState(t *testing.T) {
	fakeSrv := &fakePlansServer{
		getResp: &planspb.GetResponse{Plan: liveFreePlan("existing-plan-id-123")},
	}
	addr := startFakePlansGateway(t, fakeSrv)

	plan := newTestPlan("free")
	r := newPlanReconciler(t, plan, addr)
	seedPlanStatus(t, r, "free", "existing-plan-id-123")

	// Two reconciles from a fully-settled state must not mutate the object.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	}); err != nil {
		t.Fatalf("settling Reconcile returned error: %v", err)
	}
	var before billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "free"}, &before); err != nil {
		t.Fatalf("getting plan: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "free"},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	var after billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "free"}, &after); err != nil {
		t.Fatalf("getting plan: %v", err)
	}

	if before.ResourceVersion != after.ResourceVersion {
		t.Fatalf("steady-state reconcile wrote status (resourceVersion %s -> %s); a no-op status write re-triggers the controller's own watch (invora/devops#68)",
			before.ResourceVersion, after.ResourceVersion)
	}
}

// TestStatusUnchanged_IgnoresLastSyncedAt pins the contract of the shared
// helper: LastSyncedAt is stamped with metav1.Now() on every successful pass,
// so including it in the comparison would make every status "changed" and
// defeat the guard entirely.
func TestStatusUnchanged_IgnoresLastSyncedAt(t *testing.T) {
	t0 := metav1.NewTime(metav1.Now().Add(-1))
	t1 := metav1.Now()

	base := billingv1alpha1.BillingResourceStatus{
		ID:                 "id-1",
		ObservedGeneration: 3,
		LastSyncedAt:       &t0,
	}
	meta.SetStatusCondition(&base.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionSynced, Status: metav1.ConditionTrue, Reason: "InSync", Message: "ok",
	})

	same := *base.DeepCopy()
	same.LastSyncedAt = &t1
	if !StatusUnchanged(&base, &same) {
		t.Fatal("StatusUnchanged must ignore LastSyncedAt")
	}

	reasonChanged := *base.DeepCopy()
	meta.SetStatusCondition(&reasonChanged.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionSynced, Status: metav1.ConditionTrue, Reason: "Adopted", Message: "ok",
	})
	if StatusUnchanged(&base, &reasonChanged) {
		t.Fatal("StatusUnchanged must report a changed condition Reason")
	}

	idChanged := *base.DeepCopy()
	idChanged.ID = "id-2"
	if StatusUnchanged(&base, &idChanged) {
		t.Fatal("StatusUnchanged must report a changed ID")
	}

	genChanged := *base.DeepCopy()
	genChanged.ObservedGeneration = 4
	if StatusUnchanged(&base, &genChanged) {
		t.Fatal("StatusUnchanged must report a changed ObservedGeneration")
	}
}

// seedPlanStatus drives one Reconcile against a List-adoption so the CR ends up
// with status.externalId set and status.observedGeneration == metadata.generation,
// i.e. the steady state the guards under test are about.
func seedPlanStatus(t *testing.T, r *InvoraBillingPlanReconciler, name, externalID string) {
	t.Helper()
	var cur billingv1alpha1.InvoraBillingPlan
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &cur); err != nil {
		t.Fatalf("getting plan: %v", err)
	}
	cur.Status.ExternalID = externalID
	cur.Status.ID = externalID
	cur.Status.ObservedGeneration = cur.Generation
	meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionDependencyReady, Status: metav1.ConditionTrue,
		ObservedGeneration: cur.Generation, Reason: "DependenciesReady", Message: "All referenced resources are available",
	})
	meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionSynced, Status: metav1.ConditionTrue,
		ObservedGeneration: cur.Generation, Reason: "InSync", Message: "Resource reconciled successfully",
	})
	meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
		Type: billingv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
		ObservedGeneration: cur.Generation, Reason: "Ready", Message: "Resource reconciled successfully",
	})
	if err := r.Status().Update(context.Background(), &cur); err != nil {
		t.Fatalf("seeding plan status: %v", err)
	}
}
