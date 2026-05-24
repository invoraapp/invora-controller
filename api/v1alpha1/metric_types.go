package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// BillableMetricFilter defines a property-based filter key and its allowed values
// for billing charge filter matching.
type BillableMetricFilter struct {
	// Key is the event property name used for filtering (e.g. "document_type", "regulation_id").
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`

	// Values are the allowed values for this filter key.
	// +kubebuilder:validation:MinItems=1
	Values []string `json:"values"`
}

// InvoraBillingMetricSpec defines a usage measurement definition in billing.
type InvoraBillingMetricSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this metric belongs to.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Code is the unique identifier for this metric. Immutable after creation.
	// +kubebuilder:validation:MinLength=1
	Code string `json:"code"`

	// Name is the display name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Description is optional.
	// +optional
	Description string `json:"description,omitempty"`

	// AggregationType defines how events are aggregated.
	// +kubebuilder:validation:Enum=count_agg;sum_agg;max_agg;unique_count_agg;latest_agg;weighted_sum_agg;custom_agg
	AggregationType string `json:"aggregationType"`

	// FieldName is the event property to aggregate. Required for sum_agg, max_agg, etc.
	// +optional
	FieldName string `json:"fieldName,omitempty"`

	// WeightedInterval for weighted_sum_agg (e.g. "seconds").
	// +optional
	WeightedInterval string `json:"weightedInterval,omitempty"`

	// Recurring indicates if the metric is recurring across billing periods.
	// +optional
	Recurring bool `json:"recurring,omitempty"`

	// Filters defines property-based filter keys and their allowed values for
	// billing charge filter matching. Events carry these properties; plan charges
	// use ChargeFilter to price each combination independently.
	// +optional
	Filters []BillableMetricFilter `json:"filters,omitempty"`
}

// InvoraBillingMetricStatus defines the observed state of a InvoraBillingMetric.
type InvoraBillingMetricStatus struct {
	BillingResourceStatus `json:",inline"`

	// ExternalID is the billing-backend-assigned UUID.
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lbm
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.aggregationType`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingMetric represents a billable metric in a billing organization.
type InvoraBillingMetric struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingMetricSpec   `json:"spec,omitempty"`
	Status InvoraBillingMetricStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingMetricList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingMetric `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingMetric{}, &InvoraBillingMetricList{}) }
