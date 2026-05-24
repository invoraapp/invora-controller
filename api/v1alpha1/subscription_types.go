package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingSubscriptionSpec defines a plan subscription for a customer in billing.
type InvoraBillingSubscriptionSpec struct {
	// OrganizationRef references the InvoraBillingOrganization this subscription belongs to.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// ExternalID is the subscription's unique external identifier. Immutable.
	// +kubebuilder:validation:MinLength=1
	ExternalID string `json:"externalId"`

	// ExternalCustomerID references the customer by external ID.
	// Mutually exclusive with customerRef.
	// +optional
	ExternalCustomerID string `json:"externalCustomerId,omitempty"`

	// CustomerRef references a InvoraBillingCustomer CR. The controller resolves it
	// to the customer's externalId. Mutually exclusive with externalCustomerID.
	// +optional
	CustomerRef *ResourceRef `json:"customerRef,omitempty"`

	// PlanCode references the plan by code.
	// Mutually exclusive with planRef.
	// +optional
	PlanCode string `json:"planCode,omitempty"`

	// PlanRef references a InvoraBillingPlan CR. The controller resolves it
	// to the plan's code. Mutually exclusive with planCode.
	// +optional
	PlanRef *ResourceRef `json:"planRef,omitempty"`

	// Name is the display name for the subscription.
	// +optional
	Name string `json:"name,omitempty"`

	// BillingTime: calendar or anniversary.
	// +kubebuilder:validation:Enum=calendar;anniversary
	// +optional
	BillingTime string `json:"billingTime,omitempty"`
}

// InvoraBillingSubscriptionStatus defines the observed state.
type InvoraBillingSubscriptionStatus struct {
	BillingResourceStatus `json:",inline"`

	// ExternalID is the billing-backend-assigned UUID.
	// +optional
	ExternalID string `json:"externalId,omitempty"`

	// ResolvedCustomerExternalID is populated from customerRef resolution.
	// +optional
	ResolvedCustomerExternalID string `json:"resolvedCustomerExternalId,omitempty"`

	// ResolvedPlanCode is populated from planRef resolution.
	// +optional
	ResolvedPlanCode string `json:"resolvedPlanCode,omitempty"`

	// SubscriptionStatus is the current billing status (pending, active, terminated, canceled).
	// +optional
	SubscriptionStatus string `json:"subscriptionStatus,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lsub
// +kubebuilder:printcolumn:name="ExternalID",type=string,JSONPath=`.spec.externalId`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.subscriptionStatus`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingSubscription represents a subscription in a billing organization.
type InvoraBillingSubscription struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingSubscriptionSpec   `json:"spec,omitempty"`
	Status InvoraBillingSubscriptionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingSubscriptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingSubscription `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingSubscription{}, &InvoraBillingSubscriptionList{}) }
