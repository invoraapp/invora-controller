package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingTaxSpec defines a tax rate in billing.
type InvoraBillingTaxSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this tax belongs to.
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

	// Rate is the tax percentage (e.g. "20.0").
	// +kubebuilder:validation:MinLength=1
	Rate string `json:"rate"`

	// Description is optional.
	// +optional
	Description string `json:"description,omitempty"`
}

// InvoraBillingTaxStatus defines the observed state.
type InvoraBillingTaxStatus struct {
	BillingResourceStatus `json:",inline"`
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ltax
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Rate",type=string,JSONPath=`.spec.rate`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingTax represents a tax definition in a billing organization.
type InvoraBillingTax struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingTaxSpec   `json:"spec,omitempty"`
	Status InvoraBillingTaxStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingTaxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingTax `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingTax{}, &InvoraBillingTaxList{}) }
