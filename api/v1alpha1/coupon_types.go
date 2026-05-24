package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingCouponSpec defines a discount coupon in billing.
type InvoraBillingCouponSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this coupon belongs to.
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

	// CouponType: fixed_amount or percentage.
	// +kubebuilder:validation:Enum=fixed_amount;percentage
	CouponType string `json:"couponType"`

	// Frequency: once, recurring, or forever.
	// +kubebuilder:validation:Enum=once;recurring;forever
	Frequency string `json:"frequency"`

	// Expiration: time_limit or no_expiration.
	// +kubebuilder:validation:Enum=time_limit;no_expiration
	Expiration string `json:"expiration"`

	// AmountCents for fixed_amount coupons.
	// +optional
	AmountCents *int64 `json:"amountCents,omitempty"`

	// AmountCurrency for fixed_amount coupons.
	// +optional
	AmountCurrency *string `json:"amountCurrency,omitempty"`

	// PercentageRate for percentage coupons.
	// +optional
	PercentageRate *string `json:"percentageRate,omitempty"`

	// ExpirationAt when expiration=time_limit (ISO 8601).
	// +optional
	ExpirationAt *string `json:"expirationAt,omitempty"`

	// Reusable allows the coupon to be applied multiple times.
	// +optional
	Reusable bool `json:"reusable,omitempty"`
}

// InvoraBillingCouponStatus defines the observed state.
type InvoraBillingCouponStatus struct {
	BillingResourceStatus `json:",inline"`
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lcoup
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.couponType`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingCoupon represents a coupon in a billing organization.
type InvoraBillingCoupon struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingCouponSpec   `json:"spec,omitempty"`
	Status InvoraBillingCouponStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingCouponList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingCoupon `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingCoupon{}, &InvoraBillingCouponList{}) }
