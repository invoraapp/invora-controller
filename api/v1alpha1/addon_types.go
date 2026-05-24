package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingAddonSpec defines a one-time charge add-on.
type InvoraBillingAddonSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this add-on belongs to.
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

	// AmountCents is the add-on price in cents.
	AmountCents int64 `json:"amountCents"`

	// AmountCurrency is the ISO-4217 currency code.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=3
	AmountCurrency string `json:"amountCurrency"`

	// TaxCodes to apply to this add-on.
	// +optional
	TaxCodes []string `json:"taxCodes,omitempty"`
}

// InvoraBillingAddonStatus defines the observed state.
type InvoraBillingAddonStatus struct {
	BillingResourceStatus `json:",inline"`
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ladon
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingAddon represents an add-on in a billing organization.
type InvoraBillingAddon struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingAddonSpec   `json:"spec,omitempty"`
	Status InvoraBillingAddonStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingAddonList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingAddon `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingAddon{}, &InvoraBillingAddonList{}) }
