package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraConnectedBusinessSpec defines a connected business (tenant) within Invora.
// Connected businesses are downstream organizations that use your Invora instance.
type InvoraConnectedBusinessSpec struct {
	// InstanceRef references the InvoraBillingInstance (gateway connection).
	InstanceRef ResourceRef `json:"instanceRef"`

	// Name is the business display name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// AdminEmail is the email of the business admin (used for Zitadel org creation).
	// +kubebuilder:validation:MinLength=1
	AdminEmail string `json:"adminEmail"`

	// Currency is the default billing currency for this business (ISO 4217).
	// +optional
	Currency string `json:"currency,omitempty"`

	// Timezone is the default timezone (IANA format).
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// Suspended indicates whether this business is currently suspended.
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// InvoraConnectedBusinessStatus defines the observed state.
type InvoraConnectedBusinessStatus struct {
	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// TenantID is the Zitadel org ID / Invora tenant ID for this business.
	// +optional
	TenantID string `json:"tenantId,omitempty"`

	// ObservedGeneration is the last spec generation the controller acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncedAt is the last time this resource was synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=icb
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="TenantID",type=string,JSONPath=`.status.tenantId`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraConnectedBusiness represents a connected tenant business in Invora.
type InvoraConnectedBusiness struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraConnectedBusinessSpec   `json:"spec,omitempty"`
	Status InvoraConnectedBusinessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraConnectedBusinessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraConnectedBusiness `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraConnectedBusiness{}, &InvoraConnectedBusinessList{}) }
