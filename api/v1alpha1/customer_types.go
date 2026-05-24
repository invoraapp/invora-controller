package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingCustomerSpec defines a billable customer in billing.
type InvoraBillingCustomerSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this customer belongs to.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// ExternalID is the customer's unique external identifier. Immutable.
	// +kubebuilder:validation:MinLength=1
	ExternalID string `json:"externalId"`

	// Name of the customer.
	// +optional
	Name string `json:"name,omitempty"`

	// Email of the customer.
	// +optional
	Email string `json:"email,omitempty"`

	// Currency (ISO-4217).
	// +optional
	Currency string `json:"currency,omitempty"`

	// AddressLine1 is the primary address line.
	// +optional
	AddressLine1 string `json:"addressLine1,omitempty"`

	// AddressLine2 is the secondary address line.
	// +optional
	AddressLine2 string `json:"addressLine2,omitempty"`

	// City of the customer.
	// +optional
	City string `json:"city,omitempty"`

	// Country (ISO 3166 alpha-2).
	// +optional
	Country string `json:"country,omitempty"`

	// State or province.
	// +optional
	State string `json:"state,omitempty"`

	// Zipcode or postal code.
	// +optional
	Zipcode string `json:"zipcode,omitempty"`

	// LegalName of the customer.
	// +optional
	LegalName string `json:"legalName,omitempty"`

	// LegalNumber (tax/company registration number).
	// +optional
	LegalNumber string `json:"legalNumber,omitempty"`

	// Phone number.
	// +optional
	Phone string `json:"phone,omitempty"`

	// Timezone (IANA format).
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// TaxCodes to apply to this customer.
	// +optional
	TaxCodes []string `json:"taxCodes,omitempty"`
}

// InvoraBillingCustomerStatus defines the observed state.
type InvoraBillingCustomerStatus struct {
	BillingResourceStatus `json:",inline"`
	// ExternalID is the billing-backend-assigned UUID.
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lcust
// +kubebuilder:printcolumn:name="ExternalID",type=string,JSONPath=`.spec.externalId`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingCustomer represents a billable customer in a billing organization.
type InvoraBillingCustomer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingCustomerSpec   `json:"spec,omitempty"`
	Status InvoraBillingCustomerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingCustomerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingCustomer `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingCustomer{}, &InvoraBillingCustomerList{}) }
