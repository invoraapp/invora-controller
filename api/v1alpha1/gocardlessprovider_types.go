package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingGoCardlessProviderSpec defines the desired state of a GoCardless
// payment provider attached to a billing organization.
type InvoraBillingGoCardlessProviderSpec struct {
	// OrganizationRef references the InvoraBillingOrganization that owns this
	// payment provider.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this
	// CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Code is the provider code. Immutable after creation.
	// +kubebuilder:validation:MinLength=1
	Code string `json:"code"`

	// Name is the display name shown in the billing UI.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// AccessTokenRef references the Kubernetes Secret holding the GoCardless
	// access token.
	AccessTokenRef SecretKeyRef `json:"accessTokenRef"`

	// WebhookSecretRef references the Kubernetes Secret holding the GoCardless
	// webhook secret for signature verification.
	// +optional
	WebhookSecretRef *SecretKeyRef `json:"webhookSecretRef,omitempty"`

	// SuccessRedirectUrl is the URL for successful mandate setup.
	// +optional
	SuccessRedirectUrl string `json:"successRedirectUrl,omitempty"`

	// PrefilledCustomer enables prefilling customer details in the GoCardless
	// hosted payment page.
	// +optional
	PrefilledCustomer bool `json:"prefilledCustomer,omitempty"`
}

// InvoraBillingGoCardlessProviderStatus defines the observed state.
type InvoraBillingGoCardlessProviderStatus struct {
	BillingResourceStatus `json:",inline"`

	// ProviderCode is the resolved billing provider code.
	// +optional
	ProviderCode string `json:"providerCode,omitempty"`

	// ProviderID is the billing-backend-assigned UUID for this GoCardless provider.
	// +optional
	ProviderID string `json:"providerId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=igc
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingGoCardlessProvider represents a GoCardless payment provider
// configured on a billing organization.
type InvoraBillingGoCardlessProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingGoCardlessProviderSpec   `json:"spec,omitempty"`
	Status InvoraBillingGoCardlessProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingGoCardlessProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingGoCardlessProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingGoCardlessProvider{}, &InvoraBillingGoCardlessProviderList{})
}
