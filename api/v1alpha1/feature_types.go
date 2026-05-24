package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingFeatureSpec defines a feature/entitlement definition in billing.
type InvoraBillingFeatureSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this feature belongs to.
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

	// Metadata stores arbitrary key-value pairs at the feature level.
	// Useful for categorizing features (e.g. entity_type: "book").
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`
}

// InvoraBillingFeatureStatus defines the observed state.
type InvoraBillingFeatureStatus struct {
	BillingResourceStatus `json:",inline"`
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lfeat
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingFeature represents a feature definition in a billing organization.
type InvoraBillingFeature struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingFeatureSpec   `json:"spec,omitempty"`
	Status InvoraBillingFeatureStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingFeatureList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingFeature `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingFeature{}, &InvoraBillingFeatureList{}) }
