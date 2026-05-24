package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingStripeProviderSpec defines the desired state of a Stripe payment
// provider attached to a billing organization.
type InvoraBillingStripeProviderSpec struct {
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

	// SecretKeyRef references the Kubernetes Secret holding the Stripe secret key.
	SecretKeyRef SecretKeyRef `json:"secretKeyRef"`

	// WebhookSecretRef references the Kubernetes Secret holding the Stripe
	// webhook signing secret (whsec_*).
	// +optional
	WebhookSecretRef *SecretKeyRef `json:"webhookSecretRef,omitempty"`

	// SuccessRedirectUrl is the URL for successful payments.
	// +optional
	SuccessRedirectUrl string `json:"successRedirectUrl,omitempty"`
}

// InvoraBillingStripeProviderStatus defines the observed state.
type InvoraBillingStripeProviderStatus struct {
	BillingResourceStatus `json:",inline"`

	// ProviderCode is the resolved billing provider code.
	// +optional
	ProviderCode string `json:"providerCode,omitempty"`

	// ProviderID is the billing-backend-assigned UUID for this Stripe provider.
	// +optional
	ProviderID string `json:"providerId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=istripe
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingStripeProvider represents a Stripe payment provider configured
// on a billing organization.
type InvoraBillingStripeProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingStripeProviderSpec   `json:"spec,omitempty"`
	Status InvoraBillingStripeProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingStripeProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingStripeProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingStripeProvider{}, &InvoraBillingStripeProviderList{})
}
