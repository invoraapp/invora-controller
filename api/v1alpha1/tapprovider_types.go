package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingTapProviderSpec defines the desired state of a Tap payment
// provider attached to a billing organization.
//
// Backed by the billing GraphQL mutations addTapPaymentProvider /
// updateTapPaymentProvider, which authenticate as the organization (the
// org's API key from spec.billingOrganizationRef's WriteSecretToRef Secret).
type InvoraBillingTapProviderSpec struct {
	// InvoraBillingOrganizationRef references the InvoraBillingOrganization that owns this
	// payment provider. The controller resolves the org's API key from the
	// Secret declared in the InvoraBillingOrganization's writeSecretToRef.
	InvoraBillingOrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this
	// CR is deleted. Note: upstream billing does not currently expose a Tap
	// destroy mutation, so Delete is best-effort.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Code is the provider code (billing concept). Immutable after creation.
	// +kubebuilder:validation:MinLength=1
	Code string `json:"code"`

	// Name is the display name shown in the billing UI.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TapApiKeyRef references the Kubernetes Secret holding the Tap secret
	// API key. The value is read at reconcile time and forwarded to the billing.
	TapApiKeyRef SecretKeyRef `json:"tapApiKeyRef"`

	// TapPublicKeyRef references the Kubernetes Secret holding the Tap
	// public/publishable key. Held in the CR for completeness; only
	// forwarded to the billing when the upstream API accepts it.
	// +optional
	TapPublicKeyRef *SecretKeyRef `json:"tapPublicKeyRef,omitempty"`

	// TapWebhookSecretRef references the Kubernetes Secret holding the Tap
	// webhook signing secret. Held in the CR for completeness; only
	// forwarded to the billing when the upstream API accepts it.
	// +optional
	TapWebhookSecretRef *SecretKeyRef `json:"tapWebhookSecretRef,omitempty"`

	// SuccessRedirectUrl is the URL the customer is redirected to after a
	// successful payment.
	// +optional
	SuccessRedirectUrl string `json:"successRedirectUrl,omitempty"`

	// FailureRedirectUrl is the URL the customer is redirected to after a
	// failed payment. Reserved for future billing support; currently held in
	// the CR but not forwarded.
	// +optional
	FailureRedirectUrl string `json:"failureRedirectUrl,omitempty"`
}

// InvoraBillingTapProviderStatus defines the observed state.
type InvoraBillingTapProviderStatus struct {
	BillingResourceStatus `json:",inline"`

	// ProviderCode is the resolved billing provider code.
	// +optional
	ProviderCode string `json:"providerCode,omitempty"`

	// ProviderID is the billing-backend-assigned UUID for this Tap provider.
	// +optional
	ProviderID string `json:"providerId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ltap
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingTapProvider represents a Tap payment provider configured on
// a billing organization.
type InvoraBillingTapProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingTapProviderSpec   `json:"spec,omitempty"`
	Status InvoraBillingTapProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingTapProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingTapProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingTapProvider{}, &InvoraBillingTapProviderList{})
}
