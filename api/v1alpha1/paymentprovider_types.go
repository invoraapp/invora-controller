package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingPaymentProviderSpec defines a generic payment provider for
// providers that don't have a dedicated CRD (e.g., Moneyhash, Cashfree,
// Flutterwave). Use the provider-specific CRDs (StripeProvider, AdyenProvider,
// TapProvider, GoCardlessProvider) when available for better validation.
type InvoraBillingPaymentProviderSpec struct {
	// OrganizationRef references the InvoraBillingOrganization that owns this
	// payment provider.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this
	// CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// ProviderType identifies the payment provider type.
	// Must match one of the supported backend types (e.g., "moneyhash",
	// "cashfree", "flutterwave").
	// +kubebuilder:validation:MinLength=1
	ProviderType string `json:"providerType"`

	// Code is the provider code. Immutable after creation.
	// +kubebuilder:validation:MinLength=1
	Code string `json:"code"`

	// Name is the display name shown in the billing UI.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// CredentialsRef references the Kubernetes Secret holding the provider's
	// API credentials. The expected keys depend on the ProviderType.
	CredentialsRef SecretKeyRef `json:"credentialsRef"`

	// WebhookSecretRef references the Kubernetes Secret holding the webhook
	// signing secret for this provider.
	// +optional
	WebhookSecretRef *SecretKeyRef `json:"webhookSecretRef,omitempty"`

	// SuccessRedirectUrl is the URL for successful payments.
	// +optional
	SuccessRedirectUrl string `json:"successRedirectUrl,omitempty"`

	// Config holds provider-specific configuration as key-value pairs.
	// Consult the provider documentation for supported keys.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// InvoraBillingPaymentProviderStatus defines the observed state.
type InvoraBillingPaymentProviderStatus struct {
	BillingResourceStatus `json:",inline"`

	// ProviderCode is the resolved billing provider code.
	// +optional
	ProviderCode string `json:"providerCode,omitempty"`

	// ProviderID is the billing-backend-assigned UUID for this provider.
	// +optional
	ProviderID string `json:"providerId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ipay
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.providerType`
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingPaymentProvider represents a generic payment provider configured
// on a billing organization. Use provider-specific CRDs when available.
type InvoraBillingPaymentProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingPaymentProviderSpec   `json:"spec,omitempty"`
	Status InvoraBillingPaymentProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingPaymentProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingPaymentProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingPaymentProvider{}, &InvoraBillingPaymentProviderList{})
}
