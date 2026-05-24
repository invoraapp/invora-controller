package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingOrganizationSpec defines the desired state of a InvoraBillingOrganization.
// Organizations are the top-level tenant boundary in billing — they own
// plans, billable metrics, customers, and all billing resources.
type InvoraBillingOrganizationSpec struct {
	// InstanceRef references the InvoraBillingInstance connection to use.
	InstanceRef ResourceRef `json:"instanceRef"`

	// DeletionPolicy determines what happens to the billing org when this CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Name of the organization.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Email for the organization admin account.
	// +optional
	Email string `json:"email,omitempty"`

	// Timezone (IANA format, e.g. "UTC", "America/New_York").
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// DocumentNumbering controls invoice numbering scheme.
	// +kubebuilder:validation:Enum=per_organization;per_billing_entity
	// +optional
	DocumentNumbering string `json:"documentNumbering,omitempty"`

	// WriteSecretToRef specifies where to write the org's API key.
	// The Secret will contain key "apiKey" with the org's bearer token.
	// This is required — child resources need the API key to authenticate.
	WriteSecretToRef WriteSecretToRef `json:"writeSecretToRef"`

	// ParentOrgRef references another InvoraBillingOrganization that owns this tenant
	// as a customer. When set, a corresponding InvoraBillingCustomer will be created
	// in the parent org pointing at this org's externalId. Used to implement
	// reseller / multi-tier billing where the parent invoices Invora and the
	// child (tenant) sub-invoices its own end users.
	// +optional
	ParentOrgRef *ResourceRef `json:"parentOrgRef,omitempty"`

	// EnableParentDualWrite controls whether the controller writes a customer
	// record to the parent org when ParentOrgRef is set. Defaults to true.
	// Set to false to disable dual-write while keeping ParentOrgRef for
	// observability or future activation. Has no effect when ParentOrgRef is nil.
	// +optional
	EnableParentDualWrite *bool `json:"enableParentDualWrite,omitempty"`

	// ExternalID is an optional stable identifier carried by the corresponding
	// customer record in the parent org. When empty, defaults to the org's
	// Zitadel ID stored in metadata.name (typically "tenant-<zitadel-org-id>").
	// +optional
	ExternalID string `json:"externalId,omitempty"`
}

// InvoraBillingOrganizationStatus defines the observed state of a InvoraBillingOrganization.
type InvoraBillingOrganizationStatus struct {
	BillingResourceStatus `json:",inline"`

	// OrganizationID is the billing UUID for this org.
	// +optional
	OrganizationID string `json:"organizationId,omitempty"`

	// ApiKeyID is the billing UUID of the current API key (needed for regeneration).
	// +optional
	ApiKeyID string `json:"apiKeyId,omitempty"`

	// ParentOrgRef echoes the spec.parentOrgRef the controller last reconciled
	// against, so a change can be detected by comparing observed vs. desired.
	// +optional
	ParentOrgRef *ResourceRef `json:"parentOrgRef,omitempty"`

	// ParentCustomerID is the billing UUID of the customer entity in the parent org
	// that represents this tenant. Empty when no parent dual-write is active.
	// +optional
	ParentCustomerID *string `json:"parentCustomerId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lorg
// +kubebuilder:printcolumn:name="OrgName",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingOrganization represents an organization in a billing instance.
// The controller creates the org via super-admin GraphQL mutations,
// generates an API key, and writes it to the Secret specified by
// spec.writeSecretToRef.
type InvoraBillingOrganization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingOrganizationSpec   `json:"spec,omitempty"`
	Status InvoraBillingOrganizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InvoraBillingOrganizationList contains a list of InvoraBillingOrganization resources.
type InvoraBillingOrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingOrganization `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBillingOrganization{}, &InvoraBillingOrganizationList{}) }
