package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingInstanceSpec defines connection parameters to an Invora Billing instance.
// Every other billing CRD references a InvoraBillingInstance (directly or via
// InvoraBillingOrganization) to know which billing gateway to talk to.
type InvoraBillingInstanceSpec struct {
	// GatewayURL is the base URL of the Invora Billing gateway (e.g. "https://dev-gateway.invora.app").
	// +kubebuilder:validation:MinLength=1
	GatewayURL string `json:"gatewayUrl"`

	// Insecure disables TLS verification.
	// +kubebuilder:default=false
	// +optional
	Insecure bool `json:"insecure,omitempty"`

	// TokenRef references the Secret containing the service account
	// service account token. Required for organization lifecycle
	// operations (create/delete via GraphQL).
	TokenRef SecretKeyRef `json:"tokenRef"`
}

// InvoraBillingInstanceStatus reports the connection health.
type InvoraBillingInstanceStatus struct {
	BillingResourceStatus `json:",inline"`

	// LastConnectedAt is the last time the controller successfully
	// connected to this billing instance.
	// +optional
	LastConnectedAt *metav1.Time `json:"lastConnectedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=li
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.spec.gatewayUrl`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingInstance defines a connection to an Invora Billing instance. It serves as
// the endpoint and authentication configuration that InvoraBillingOrganization
// resources reference via their instanceRef field.
type InvoraBillingInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingInstanceSpec   `json:"spec,omitempty"`
	Status InvoraBillingInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InvoraBillingInstanceList contains a list of InvoraBillingInstance resources.
type InvoraBillingInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingInstance `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingInstance{}, &InvoraBillingInstanceList{}) }
