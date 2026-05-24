package controller

import (
	"context"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	billingv1alpha1 "github.com/invoraapp/billing-controller/api/v1alpha1"
)

// newSubscriberForTest wires a subscriber against a fake client preloaded
// with the given objects. Returns the subscriber plus the fake client so
// assertions can inspect state directly.
func newSubscriberForTest(t *testing.T, objs ...runtime.Object) *ZitadelOrgEventSubscriber {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering core scheme: %v", err)
	}
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("registering billing scheme: %v", err)
	}
	cb := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		cb = cb.WithRuntimeObjects(objs...)
	}
	return &ZitadelOrgEventSubscriber{
		Client: cb.Build(),
		Config: ZitadelOrgEventSubscriberConfig{
			ZitadelDomain:         "dev-auth.example",
			InvoraBillingInstanceName:      "invora-billing",
			InvoraBillingInstanceNamespace: "billing-dev",
			TenantNamespace:       "invora-dev",
			StateNamespace:        "invora-billing-controller",
			AccessToken:           "test",
		},
	}
}

func TestHandleOrgAdded_CreatesInvoraBillingOrganization(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	if err := s.handleOrgAdded(ctx, "111222333", map[string]any{"name": "Acme Inc"}); err != nil {
		t.Fatalf("handleOrgAdded: %v", err)
	}

	got := &billingv1alpha1.InvoraBillingOrganization{}
	err := s.Get(ctx, types.NamespacedName{
		Name:      tenantOrgCRName("111222333"),
		Namespace: "invora-dev",
	}, got)
	if err != nil {
		t.Fatalf("expected InvoraBillingOrganization to exist: %v", err)
	}
	if got.Spec.ExternalID != "111222333" {
		t.Errorf("ExternalID = %q, want %q", got.Spec.ExternalID, "111222333")
	}
	if got.Spec.Name != "Acme Inc" {
		t.Errorf("Spec.Name = %q, want %q", got.Spec.Name, "Acme Inc")
	}
	if got.Spec.InstanceRef.Name != "invora-billing" {
		t.Errorf("InstanceRef.Name = %q, want invora-billing", got.Spec.InstanceRef.Name)
	}
	if got.Spec.WriteSecretToRef.Name != "billing-org-111222333-api-key" {
		t.Errorf("WriteSecretToRef.Name = %q", got.Spec.WriteSecretToRef.Name)
	}
}

func TestHandleOrgAdded_IdempotentOnReDelivery(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.handleOrgAdded(ctx, "abc", map[string]any{"name": "Repeat"}); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}

	list := &billingv1alpha1.InvoraBillingOrganizationList{}
	if err := s.List(ctx, list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly 1 InvoraBillingOrganization, got %d", len(list.Items))
	}
}

func TestHandleOrgChanged_UpdatesName(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	if err := s.handleOrgAdded(ctx, "xyz", map[string]any{"name": "Old Name"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.handleOrgChanged(ctx, "xyz", map[string]any{"name": "New Name"}); err != nil {
		t.Fatalf("handleOrgChanged: %v", err)
	}

	got := &billingv1alpha1.InvoraBillingOrganization{}
	if err := s.Get(ctx, types.NamespacedName{
		Name:      tenantOrgCRName("xyz"),
		Namespace: "invora-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Name != "New Name" {
		t.Errorf("Spec.Name = %q, want %q", got.Spec.Name, "New Name")
	}
}

func TestHandleOrgChanged_NoCRYet_NoOp(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()
	// org.changed before org.added must not error or create anything.
	if err := s.handleOrgChanged(ctx, "ghost", map[string]any{"name": "Whatever"}); err != nil {
		t.Fatalf("handleOrgChanged: %v", err)
	}
	list := &billingv1alpha1.InvoraBillingOrganizationList{}
	if err := s.List(ctx, list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expected no InvoraBillingOrganization, got %d", len(list.Items))
	}
}

func TestHandleOrgRemoved_DeletesCR(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	if err := s.handleOrgAdded(ctx, "bye", map[string]any{"name": "Goner"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.handleOrgRemoved(ctx, "bye"); err != nil {
		t.Fatalf("handleOrgRemoved: %v", err)
	}

	got := &billingv1alpha1.InvoraBillingOrganization{}
	err := s.Get(ctx, types.NamespacedName{
		Name:      tenantOrgCRName("bye"),
		Namespace: "invora-dev",
	}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestHandleOrgRemoved_AlreadyAbsent_NoError(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()
	if err := s.handleOrgRemoved(ctx, "never-existed"); err != nil {
		t.Fatalf("handleOrgRemoved on absent CR returned error: %v", err)
	}
}

func TestStateConfigMap_WriteAndReadRoundTrip(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	if err := s.writeState(ctx, 42); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	got, err := s.readState(ctx)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if got != 42 {
		t.Errorf("readState = %d, want 42", got)
	}

	// Update path
	if err := s.writeState(ctx, 100); err != nil {
		t.Fatalf("writeState update: %v", err)
	}
	got, err = s.readState(ctx)
	if err != nil {
		t.Fatalf("readState update: %v", err)
	}
	if got != 100 {
		t.Errorf("readState after update = %d, want 100", got)
	}

	// Verify ConfigMap contents directly
	cm := &corev1.ConfigMap{}
	if err := s.Get(ctx, types.NamespacedName{
		Name:      SubscriberStateConfigMapName,
		Namespace: "invora-billing-controller",
	}, cm); err != nil {
		t.Fatalf("get state cm: %v", err)
	}
	if cm.Data[SubscriberStateConfigMapKey] != strconv.FormatUint(100, 10) {
		t.Errorf("ConfigMap data = %q, want 100", cm.Data[SubscriberStateConfigMapKey])
	}
}

func TestStateConfigMap_NoStateReturnsZero(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()
	got, err := s.readState(ctx)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if got != 0 {
		t.Errorf("expected zero from missing state, got %d", got)
	}
}

func TestWriteStateIfAdvanced(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	// Same sequence — no write
	if err := s.writeStateIfAdvanced(ctx, 5, 5); err != nil {
		t.Fatalf("equal seq: %v", err)
	}
	cm := &corev1.ConfigMap{}
	err := s.Get(ctx, types.NamespacedName{
		Name:      SubscriberStateConfigMapName,
		Namespace: "invora-billing-controller",
	}, cm)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("ConfigMap should not have been created when seq did not advance, got %v", err)
	}

	// Advanced — write
	if err := s.writeStateIfAdvanced(ctx, 5, 10); err != nil {
		t.Fatalf("advanced seq: %v", err)
	}
	if err := s.Get(ctx, types.NamespacedName{
		Name:      SubscriberStateConfigMapName,
		Namespace: "invora-billing-controller",
	}, cm); err != nil {
		t.Fatalf("expected state CM after advance: %v", err)
	}
	if cm.Data[SubscriberStateConfigMapKey] != "10" {
		t.Errorf("state value = %q, want 10", cm.Data[SubscriberStateConfigMapKey])
	}
}

func TestTenantOrgCRName_LowercasesAndPrefixes(t *testing.T) {
	got := tenantOrgCRName("AbCDef")
	want := "tenant-abcdef"
	if got != want {
		t.Errorf("tenantOrgCRName(%q) = %q, want %q", "AbCDef", got, want)
	}
}

func TestStringFromPayload(t *testing.T) {
	p := map[string]any{"name": "hello", "n": 42}
	if got := stringFromPayload(p, "name"); got != "hello" {
		t.Errorf("name = %q, want hello", got)
	}
	if got := stringFromPayload(p, "missing"); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
	// non-string value -> empty
	if got := stringFromPayload(p, "n"); got != "" {
		t.Errorf("non-string n = %q, want empty", got)
	}
	if got := stringFromPayload(nil, "anything"); got != "" {
		t.Errorf("nil payload = %q, want empty", got)
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ZitadelOrgEventSubscriberConfig
		wantErr bool
	}{
		{
			name: "valid PAT",
			cfg: ZitadelOrgEventSubscriberConfig{
				ZitadelDomain:         "zi",
				InvoraBillingInstanceName:      "li",
				InvoraBillingInstanceNamespace: "lns",
				TenantNamespace:       "tns",
				AccessToken:           "tok",
			},
		},
		{
			name: "missing domain",
			cfg: ZitadelOrgEventSubscriberConfig{
				InvoraBillingInstanceName:      "li",
				InvoraBillingInstanceNamespace: "lns",
				TenantNamespace:       "tns",
				AccessToken:           "tok",
			},
			wantErr: true,
		},
		{
			name: "missing auth",
			cfg: ZitadelOrgEventSubscriberConfig{
				ZitadelDomain:         "zi",
				InvoraBillingInstanceName:      "li",
				InvoraBillingInstanceNamespace: "lns",
				TenantNamespace:       "tns",
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &ZitadelOrgEventSubscriber{Config: tc.cfg}
			err := s.validateConfig()
			if (err != nil) != tc.wantErr {
				t.Errorf("validateConfig() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Sanity guard: subscriber requires leader election.
func TestSubscriber_NeedsLeaderElection(t *testing.T) {
	s := &ZitadelOrgEventSubscriber{}
	if !s.NeedLeaderElection() {
		t.Fatal("subscriber must require leader election to avoid duplicate event processing")
	}
}

// Verify the InvoraBillingOrganization the subscriber creates carries identifying
// labels so operators can map a tenant CR to its Zitadel origin.
func TestHandleOrgAdded_AttachesLabels(t *testing.T) {
	s := newSubscriberForTest(t)
	ctx := context.Background()

	if err := s.handleOrgAdded(ctx, "Z42", map[string]any{"name": "Labelled"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := &billingv1alpha1.InvoraBillingOrganization{}
	if err := s.Get(ctx, types.NamespacedName{
		Name:      tenantOrgCRName("Z42"),
		Namespace: "invora-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Labels["zitadel.bdaya-dev.com/org-id"] != "Z42" {
		t.Errorf("expected org-id label, got labels=%v", got.Labels)
	}
	if got.Labels["billing.invora.app/managed-by"] != "zitadel-event-subscriber" {
		t.Errorf("expected managed-by label, got labels=%v", got.Labels)
	}
}

// Pre-existing CR with matching name is left alone except spec.name update.
func TestHandleOrgAdded_ReusesExistingCR_OnlyUpdatesNameOnDrift(t *testing.T) {
	preExisting := &billingv1alpha1.InvoraBillingOrganization{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tenantOrgCRName("pre"),
			Namespace: "invora-dev",
		},
		Spec: billingv1alpha1.InvoraBillingOrganizationSpec{
			InstanceRef: billingv1alpha1.ResourceRef{
				Name:      "manual",
				Namespace: "manual-ns",
			},
			Name:       "Old",
			ExternalID: "pre",
			WriteSecretToRef: billingv1alpha1.WriteSecretToRef{
				Name: "manual-secret",
			},
		},
	}
	s := newSubscriberForTest(t, preExisting)
	ctx := context.Background()

	if err := s.handleOrgAdded(ctx, "pre", map[string]any{"name": "New"}); err != nil {
		t.Fatalf("handleOrgAdded: %v", err)
	}
	got := &billingv1alpha1.InvoraBillingOrganization{}
	if err := s.Get(ctx, types.NamespacedName{
		Name:      tenantOrgCRName("pre"),
		Namespace: "invora-dev",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Name != "New" {
		t.Errorf("Spec.Name = %q, want New", got.Spec.Name)
	}
	if got.Spec.InstanceRef.Name != "manual" {
		t.Errorf("InstanceRef should be left untouched, got %q", got.Spec.InstanceRef.Name)
	}
}
