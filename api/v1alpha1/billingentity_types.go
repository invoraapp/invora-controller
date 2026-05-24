package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingEntityEmailSettings mirrors the upstream BillingEntity
// emailSettings array as discrete booleans for ergonomic CR authoring.
// Each true value contributes the corresponding string to the billing
// emailSettings array (e.g. "invoice.finalized").
type InvoraBillingEntityEmailSettings struct {
	// InvoiceFinalized enables the "invoice.finalized" email setting.
	// +optional
	InvoiceFinalized bool `json:"invoiceFinalized,omitempty"`

	// CreditNoteCreated enables the "credit_note.created" email setting.
	// +optional
	CreditNoteCreated bool `json:"creditNoteCreated,omitempty"`

	// PaymentReceiptCreated enables the "payment_receipt.created" email
	// setting.
	// +optional
	PaymentReceiptCreated bool `json:"paymentReceiptCreated,omitempty"`
}

// InvoraBillingEntitySpec defines a Billing Entity inside a billing
// organization. Backed by the GraphQL mutations createBillingEntity /
// updateBillingEntity (auth: organization API key).
type InvoraBillingEntitySpec struct {
	// InvoraBillingOrganizationRef references the InvoraBillingOrganization that owns this
	// billing entity.
	InvoraBillingOrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this
	// CR is deleted. The billing does not currently allow destroying
	// billing entities, so Delete is best-effort and falls back to Orphan
	// semantics if the API rejects the request.
	// +kubebuilder:default=Orphan
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Code is the billing-entity code (billing concept). Immutable after
	// creation.
	// +kubebuilder:validation:MinLength=1
	Code string `json:"code"`

	// Name is the display name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TaxIdentificationNumber is the tax/VAT registration number.
	// +optional
	TaxIdentificationNumber string `json:"taxIdentificationNumber,omitempty"`

	// LegalName of the entity.
	// +optional
	LegalName string `json:"legalName,omitempty"`

	// LegalNumber of the entity (registration number).
	// +optional
	LegalNumber string `json:"legalNumber,omitempty"`

	// Email contact address.
	// +optional
	Email string `json:"email,omitempty"`

	// AddressLine1 is the primary address line.
	// +optional
	AddressLine1 string `json:"addressLine1,omitempty"`

	// AddressLine2 is the secondary address line.
	// +optional
	AddressLine2 string `json:"addressLine2,omitempty"`

	// City of the entity.
	// +optional
	City string `json:"city,omitempty"`

	// State or province.
	// +optional
	State string `json:"state,omitempty"`

	// Country (ISO 3166-1 alpha-2).
	// +optional
	Country string `json:"country,omitempty"`

	// Zipcode or postal code.
	// +optional
	Zipcode string `json:"zipcode,omitempty"`

	// Timezone (IANA format, e.g. "UTC", "Europe/Paris").
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// DefaultCurrency (ISO 4217). Required by the billing when set.
	// +kubebuilder:validation:MinLength=3
	DefaultCurrency string `json:"defaultCurrency"`

	// DocumentNumberPrefix is the prefix used for invoice numbering.
	// +optional
	DocumentNumberPrefix string `json:"documentNumberPrefix,omitempty"`

	// DocumentNumbering controls invoice numbering scheme.
	// +kubebuilder:validation:Enum=per_customer;per_billing_entity
	// +optional
	DocumentNumbering string `json:"documentNumbering,omitempty"`

	// NetPaymentTerm is the default net payment term in days.
	// +kubebuilder:validation:Minimum=0
	// +optional
	NetPaymentTerm int32 `json:"netPaymentTerm,omitempty"`

	// EuTaxManagement enables EU tax management on this billing entity.
	// +optional
	EuTaxManagement *bool `json:"euTaxManagement,omitempty"`

	// Einvoicing enables electronic invoicing on this billing entity.
	// +optional
	Einvoicing *bool `json:"einvoicing,omitempty"`

	// FinalizeZeroAmountInvoice controls whether zero-amount invoices are
	// finalized automatically.
	// +optional
	FinalizeZeroAmountInvoice *bool `json:"finalizeZeroAmountInvoice,omitempty"`

	// EmailSettings configures which emails the billing system sends from this billing
	// entity.
	// +optional
	EmailSettings *InvoraBillingEntityEmailSettings `json:"emailSettings,omitempty"`

	// DunningCampaignCode is the code of the dunning campaign applied to
	// this billing entity. Reserved for future use; currently held in the
	// CR but not forwarded to the billing.
	// +optional
	DunningCampaignCode string `json:"dunningCampaignCode,omitempty"`
}

// InvoraBillingEntityStatus defines the observed state.
type InvoraBillingEntityStatus struct {
	BillingResourceStatus `json:",inline"`

	// InvoraBillingEntityID is the billing-backend-assigned UUID for this billing entity.
	// +optional
	InvoraBillingEntityID string `json:"billingEntityId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lbe
// +kubebuilder:printcolumn:name="Code",type=string,JSONPath=`.spec.code`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Currency",type=string,JSONPath=`.spec.defaultCurrency`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingEntity represents a Billing Entity in a billing organization.
type InvoraBillingEntity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingEntitySpec   `json:"spec,omitempty"`
	Status InvoraBillingEntityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingEntityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingEntity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingEntity{}, &InvoraBillingEntityList{})
}
