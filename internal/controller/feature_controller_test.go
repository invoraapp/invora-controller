package controller

import (
	"testing"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonv2 "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
)

// TestBuildFeaturePrivileges_MapsCodeNameValueTypeAndSelectOptions: every field
// on a declared FeaturePrivilege must round-trip into the wire shape —
// bayader-devops#101, the CRD gap that blocked bayader-backend#96's real
// session_quota grant. This is the single point that would silently drop a
// privilege declaration if it regressed.
func TestBuildFeaturePrivileges_MapsCodeNameValueTypeAndSelectOptions(t *testing.T) {
	privs := []billingv1alpha1.FeaturePrivilege{
		{Code: "count", Name: "Session Count", ValueType: "integer"},
		{Code: "tier", ValueType: "select", SelectOptions: []string{"basic", "plus"}},
		{Code: "bare"}, // no name/valueType/options declared at all
	}

	out := buildFeaturePrivileges(privs)

	if len(out) != 3 {
		t.Fatalf("buildFeaturePrivileges returned %d entries, want 3", len(out))
	}

	// "count": integer valueType, name set, no config.
	if out[0].Code != "count" {
		t.Fatalf("out[0].Code = %q, want \"count\"", out[0].Code)
	}
	if out[0].GetName() != "Session Count" {
		t.Fatalf("out[0].GetName() = %q, want \"Session Count\"", out[0].GetName())
	}
	if out[0].ValueType == nil || out[0].GetValueType() != commonv2.PrivilegeValueTypeEnum_PRIVILEGE_VALUE_TYPE_ENUM_INTEGER {
		t.Fatalf("out[0].GetValueType() = %v, want INTEGER", out[0].GetValueType())
	}
	if out[0].Config != nil {
		t.Fatalf("out[0].Config = %v, want nil (no select options declared)", out[0].Config)
	}

	// "tier": select valueType, config carries the options.
	if out[1].GetValueType() != commonv2.PrivilegeValueTypeEnum_PRIVILEGE_VALUE_TYPE_ENUM_SELECT {
		t.Fatalf("out[1].GetValueType() = %v, want SELECT", out[1].GetValueType())
	}
	if out[1].Config == nil || len(out[1].Config.SelectOptions) != 2 {
		t.Fatalf("out[1].Config = %v, want SelectOptions=[basic plus]", out[1].Config)
	}
	if out[1].Config.SelectOptions[0] != "basic" || out[1].Config.SelectOptions[1] != "plus" {
		t.Fatalf("out[1].Config.SelectOptions = %v, want [basic plus]", out[1].Config.SelectOptions)
	}

	// "bare": code only — Name/ValueType/Config must stay unset (oneof nil), not
	// zero-valued-and-sent (which would wire an explicit UNSPECIFIED value type).
	if out[2].Name != nil {
		t.Fatalf("out[2].Name = %v, want nil (not declared)", out[2].Name)
	}
	if out[2].ValueType != nil {
		t.Fatalf("out[2].ValueType = %v, want nil (not declared)", out[2].ValueType)
	}
	if out[2].Config != nil {
		t.Fatalf("out[2].Config = %v, want nil (not declared)", out[2].Config)
	}
}

// TestBuildFeaturePrivileges_EmptyDeclaration_ReturnsNil: a Feature with no
// privileges declared must send nil, not an empty-but-non-nil slice — matching
// every other repeated field builder in this file (buildCreateFeatureRequest's
// own Metadata field via convert.MetadataInputs follows the same convention).
func TestBuildFeaturePrivileges_EmptyDeclaration_ReturnsNil(t *testing.T) {
	if got := buildFeaturePrivileges(nil); got != nil {
		t.Fatalf("buildFeaturePrivileges(nil) = %v, want nil", got)
	}
	if got := buildFeaturePrivileges([]billingv1alpha1.FeaturePrivilege{}); got != nil {
		t.Fatalf("buildFeaturePrivileges([]) = %v, want nil", got)
	}
}

// TestBuildCreateAndUpdateFeatureRequest_CarryPrivileges: the two request
// builders this issue's fix touches must both plumb Spec.Privileges through —
// a regression in either one silently drops the privilege declaration on
// exactly one of create-vs-update, which would only surface as a
// privilege_not_found on whichever path a given feature happens to take
// (new feature vs. an already-existing one), not on both.
func TestBuildCreateAndUpdateFeatureRequest_CarryPrivileges(t *testing.T) {
	feature := &billingv1alpha1.InvoraBillingFeature{
		Spec: billingv1alpha1.InvoraBillingFeatureSpec{
			Code: "session_quota",
			Name: "Session Quota",
			Privileges: []billingv1alpha1.FeaturePrivilege{
				{Code: "count", Name: "Session Count", ValueType: "integer"},
			},
		},
	}
	feature.Status.ExternalID = "feat-ext-1"

	createReq := buildCreateFeatureRequest(feature)
	if len(createReq.Privileges) != 1 || createReq.Privileges[0].Code != "count" {
		t.Fatalf("buildCreateFeatureRequest(...).Privileges = %v, want [{Code: count}]", createReq.Privileges)
	}

	updateReq := buildUpdateFeatureRequest(feature)
	if len(updateReq.Privileges) != 1 || updateReq.Privileges[0].Code != "count" {
		t.Fatalf("buildUpdateFeatureRequest(...).Privileges = %v, want [{Code: count}]", updateReq.Privileges)
	}

	// Sanity: still a *planspb.FeaturePrivilegeInput, not some other type —
	// catches a copy-paste onto the wrong generated package.
	var _ *planspb.FeaturePrivilegeInput = createReq.Privileges[0]
}
