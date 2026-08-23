package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InvoraBillingPlanSpec defines a pricing plan in billing.
type InvoraBillingPlanSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this plan belongs to.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Code is the unique identifier. Immutable after creation.
	// +kubebuilder:validation:MinLength=1
	Code string `json:"code"`

	// Name is the display name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Description is optional.
	// +optional
	Description string `json:"description,omitempty"`

	// AmountCents is the plan's base price in cents.
	AmountCents int64 `json:"amountCents"`

	// AmountCurrency is the ISO-4217 currency code.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=3
	AmountCurrency string `json:"amountCurrency"`

	// Interval defines the billing cycle.
	// +kubebuilder:validation:Enum=weekly;monthly;quarterly;yearly
	Interval string `json:"interval"`

	// PayInAdvance charges at the beginning of the billing period.
	// +optional
	PayInAdvance bool `json:"payInAdvance,omitempty"`

	// TrialPeriod in days (as string to avoid float precision issues, e.g. "14.5").
	// +optional
	TrialPeriod string `json:"trialPeriod,omitempty"`

	// Charges defines usage-based pricing tied to billable metrics.
	// +optional
	Charges []PlanCharge `json:"charges,omitempty"`

	// TaxCodes references billing tax codes to apply to this plan.
	// +optional
	TaxCodes []string `json:"taxCodes,omitempty"`

	// Entitlements declares the plan-level feature entitlements (e.g. a
	// connected_business grant) that apply to EVERY subscription on this
	// plan — current and future. The controller reconciles this field
	// declaratively and AUTHORITATIVELY: on every reconcile the full list
	// here is sent to the billing backend, which replaces the plan's
	// entire entitlement set with exactly what is declared (the backend's
	// plan-update semantics are full-replace, not merge — see
	// invora-controller#9 / invora/lago/lago-api MR wiring
	// Entitlement::PlanEntitlementsUpdateService with partial: false).
	//
	// Leaving this field unset, or setting it to an empty list, never
	// touches the plan's remote entitlements at all — the controller only
	// calls the backend's entitlement-replace path when this list is
	// NON-EMPTY (the backend gates on `entitlements.any?`, and proto3
	// serializes a nil and an empty repeated field identically, so an
	// explicit `entitlements: []` is wire-indistinguishable from omitting
	// the field — it does NOT clear a plan's existing entitlements). A
	// plan CR that never declares a non-empty spec.entitlements (e.g. the
	// Salla plans) leaves any out-of-band-granted entitlements on that
	// plan completely untouched forever.
	// +optional
	Entitlements []PlanEntitlement `json:"entitlements,omitempty"`
}

// PlanEntitlement declares a single feature entitlement for a plan. Maps
// 1:1 to invora.billing.common.v2.EntitlementInput — mirrors
// SubscriptionEntitlement (subscription_types.go) at the plan level.
type PlanEntitlement struct {
	// FeatureCode references the feature by code.
	// +kubebuilder:validation:MinLength=1
	FeatureCode string `json:"featureCode"`

	// Privileges overrides privilege values for this feature on this plan.
	// +optional
	Privileges []EntitlementPrivilege `json:"privileges,omitempty"`
}

// PlanCharge defines a usage-based charge on a plan.
type PlanCharge struct {
	// BillableMetricCode references the billable metric by code.
	// +kubebuilder:validation:MinLength=1
	BillableMetricCode string `json:"billableMetricCode"`

	// ChargeModel defines the pricing model.
	// +kubebuilder:validation:Enum=standard;graduated;package;percentage;volume
	ChargeModel string `json:"chargeModel"`

	// InvoiceDisplayName overrides the charge name on invoices.
	// +optional
	InvoiceDisplayName string `json:"invoiceDisplayName,omitempty"`

	// PayInAdvance charges usage at the beginning of the period.
	// +optional
	PayInAdvance bool `json:"payInAdvance,omitempty"`

	// Prorated enables prorated billing for mid-period changes.
	// +optional
	Prorated bool `json:"prorated,omitempty"`

	// Properties holds charge-model-specific configuration as raw JSON.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	Properties *apiextensionsv1.JSON `json:"properties,omitempty"`
}

// InvoraBillingPlanStatus defines the observed state of a InvoraBillingPlan.
type InvoraBillingPlanStatus struct {
	BillingResourceStatus `json:",inline"`

	// ExternalID is the billing-backend-assigned UUID.
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lplan
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Interval",type=string,JSONPath=`.spec.interval`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingPlan represents a pricing plan in a billing organization.
type InvoraBillingPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingPlanSpec   `json:"spec,omitempty"`
	Status InvoraBillingPlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingPlan `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingPlan{}, &InvoraBillingPlanList{}) }
