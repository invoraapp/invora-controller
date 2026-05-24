package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingWebhookEndpointSpec defines a webhook endpoint in billing.
type InvoraBillingWebhookEndpointSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this webhook belongs to.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// WebhookURL is the HTTPS URL that receives webhook events.
	// +kubebuilder:validation:MinLength=1
	WebhookURL string `json:"webhookUrl"`

	// SignatureAlgo defines the webhook signature algorithm.
	// +kubebuilder:validation:Enum=jwt;hmac
	// +optional
	SignatureAlgo string `json:"signatureAlgo,omitempty"`
}

// InvoraBillingWebhookEndpointStatus defines the observed state.
type InvoraBillingWebhookEndpointStatus struct {
	BillingResourceStatus `json:",inline"`

	// ExternalID is the billing-backend-assigned UUID (webhooks are identified by UUID, not code).
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lwh
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.webhookUrl`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingWebhookEndpoint represents a webhook endpoint in a billing organization.
type InvoraBillingWebhookEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingWebhookEndpointSpec   `json:"spec,omitempty"`
	Status InvoraBillingWebhookEndpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingWebhookEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingWebhookEndpoint `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingWebhookEndpoint{}, &InvoraBillingWebhookEndpointList{}) }
