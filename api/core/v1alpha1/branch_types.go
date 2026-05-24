package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBranchSpec defines a branch within an Invora organization.
// Branches represent distinct business locations or trade names, each with
// their own regulation config, document prefixes, and party information.
type InvoraBranchSpec struct {
	// InstanceRef references the InvoraBillingInstance (gateway connection).
	InstanceRef ResourceRef `json:"instanceRef"`

	// Name is the branch display name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TradeName is the DBA (Doing Business As) name for this branch.
	// +optional
	TradeName string `json:"tradeName,omitempty"`

	// IsDefault marks this as the primary branch of the organization.
	// +optional
	IsDefault bool `json:"isDefault,omitempty"`

	// DocumentPrefix is the prefix for all documents issued from this branch.
	// +optional
	DocumentPrefix string `json:"documentPrefix,omitempty"`

	// Party contains the legal party information for this branch.
	// +optional
	Party *BranchParty `json:"party,omitempty"`

	// RegulationConfig contains per-regulation settings for this branch.
	// +optional
	RegulationConfig *BranchRegulationConfig `json:"regulationConfig,omitempty"`
}

// BranchParty holds legal entity information for a branch.
type BranchParty struct {
	// LegalName is the registered legal name.
	LegalName string `json:"legalName,omitempty"`

	// TaxID is the tax identification number (e.g., VAT number).
	TaxID string `json:"taxId,omitempty"`

	// CommercialRegistrationNumber is the CRN / company registry number.
	CommercialRegistrationNumber string `json:"commercialRegistrationNumber,omitempty"`

	// Address holds the branch's physical address.
	// +optional
	Address *Address `json:"address,omitempty"`
}

// Address represents a physical address.
type Address struct {
	Street       string `json:"street,omitempty"`
	Building     string `json:"building,omitempty"`
	City         string `json:"city,omitempty"`
	State        string `json:"state,omitempty"`
	PostalCode   string `json:"postalCode,omitempty"`
	Country      string `json:"country,omitempty"`
	AdditionalNo string `json:"additionalNo,omitempty"`
}

// BranchRegulationConfig holds per-regulation identifiers for a branch.
type BranchRegulationConfig struct {
	// ZATCA-specific: Cryptographic Stamp Identifier for Saudi e-invoicing.
	// +optional
	ZatcaCsid string `json:"zatcaCsid,omitempty"`

	// ZATCA-specific: Branch ID registered with ZATCA.
	// +optional
	ZatcaBranchId string `json:"zatcaBranchId,omitempty"`

	// Peppol-specific: Global Location Number for electronic delivery.
	// +optional
	PeppolGln string `json:"peppolGln,omitempty"`

	// ETA Egypt-specific: Branch ID registered with the Egyptian Tax Authority.
	// +optional
	EtaBranchId string `json:"etaBranchId,omitempty"`
}

// InvoraBranchStatus defines the observed state.
type InvoraBranchStatus struct {
	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// BranchID is the backend-assigned ID for this branch.
	// +optional
	BranchID string `json:"branchId,omitempty"`

	// ObservedGeneration is the last spec generation the controller acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncedAt is the last time this resource was synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ibranch
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Default",type=boolean,JSONPath=`.spec.isDefault`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBranch represents a branch within an Invora organization.
type InvoraBranch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBranchSpec   `json:"spec,omitempty"`
	Status InvoraBranchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBranchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBranch `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraBranch{}, &InvoraBranchList{}) }
