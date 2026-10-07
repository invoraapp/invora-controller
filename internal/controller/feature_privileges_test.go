package controller

import (
	"testing"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	planspb "github.com/invoraapp/invora-controller/gen/invora/billing/plans/v2"
)

func privCodeList(in []*planspb.FeaturePrivilegeInput) []string {
	var out []string
	for _, p := range in {
		out = append(out, p.GetCode())
	}
	return out
}

func TestFeatureRequestsCarryPrivileges(t *testing.T) {
	f := &billingv1alpha1.InvoraBillingFeature{}
	f.Spec.Code = "session_quota"
	f.Spec.Privileges = []billingv1alpha1.FeaturePrivilege{{Code: "count", ValueType: "integer"}}
	f.Status.ExternalID = "id1"
	for name, got := range map[string][]string{
		"create": privCodeList(buildCreateFeatureRequest(f).Privileges),
		"update": privCodeList(buildUpdateFeatureRequest(f).Privileges),
	} {
		if len(got) != 1 || got[0] != "count" {
			t.Fatalf("%s: privileges = %v", name, got)
		}
	}
	vt := buildCreateFeatureRequest(f).Privileges[0].GetValueType()
	if vt != commonpb.PrivilegeValueTypeEnum_PRIVILEGE_VALUE_TYPE_ENUM_INTEGER {
		t.Fatalf("value type %v", vt)
	}
}
