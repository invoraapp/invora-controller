package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingAdyenProviderSpec defines the desired state of an Adyen payment
// provider attached to a billing organization.
type InvoraBillingAdyenProviderSpec struct {
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

	// ApiKeyRef references the Kubernetes Secret holding the Adyen API key.
	ApiKeyRef SecretKeyRef `json:"apiKeyRef"`

	// HmacKeyRef references the Kubernetes Secret holding the Adyen HMAC key
	// for webhook signature verification.
	// +optional
	HmacKeyRef *SecretKeyRef `json:"hmacKeyRef,omitempty"`

	// MerchantAccount is the Adyen merchant account identifier.
	// +kubebuilder:validation:MinLength=1
	MerchantAccount string `json:"merchantAccount"`

	// LivePrefix is required for live (non-test) Adyen environments.
	// +optional
	LivePrefix string `json:"livePrefix,omitempty"`

	// SuccessRedirectUrl is the URL for successful payments.
	// +optional
	SuccessRedirectUrl string `json:"successRedirectUrl,omitempty"`
}

// InvoraBillingAdyenProviderStatus defines the observed state.
type InvoraBillingAdyenProviderStatus struct {
	BillingResourceStatus `json:",inline"`

	// ProviderCode is the resolved billing provider code.
	// +optional
	ProviderCode string `json:"providerCode,omitempty"`

	// ProviderID is the billing-backend-assigned UUID for this Adyen provider.
	// +optional
	ProviderID string `json:"providerId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iadyen
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Merchant",type=string,JSONPath=`.spec.merchantAccount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingAdyenProvider represents an Adyen payment provider configured
// on a billing organization.
type InvoraBillingAdyenProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingAdyenProviderSpec   `json:"spec,omitempty"`
	Status InvoraBillingAdyenProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingAdyenProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingAdyenProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingAdyenProvider{}, &InvoraBillingAdyenProviderList{})
}
