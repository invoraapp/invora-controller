package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraBillingWalletSpec defines a prepaid credit wallet attached to a
// billing customer. Wallets allow customers to maintain a credit balance
// that is automatically applied to invoices before charging the payment method.
type InvoraBillingWalletSpec struct {
	// OrganizationRef references the InvoraBillingOrganization that owns this wallet.
	OrganizationRef ResourceRef `json:"organizationRef"`

	// DeletionPolicy determines what happens to the billing resource when this
	// CR is deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// ExternalCustomerID is the customer who owns this wallet.
	// +kubebuilder:validation:MinLength=1
	ExternalCustomerID string `json:"externalCustomerId"`

	// Name is a human-readable label for the wallet.
	// +optional
	Name string `json:"name,omitempty"`

	// Currency is the wallet currency (ISO 4217). Must match the customer's currency.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=3
	Currency string `json:"currency"`

	// RateAmount is the conversion rate from credits to currency units.
	// For 1:1 mapping, set to "1.0".
	// +kubebuilder:default="1.0"
	RateAmount string `json:"rateAmount,omitempty"`

	// PaidCredits is the initial credits to grant on creation.
	// +optional
	PaidCredits string `json:"paidCredits,omitempty"`

	// GrantedCredits is the initial free/promotional credits to grant.
	// +optional
	GrantedCredits string `json:"grantedCredits,omitempty"`

	// ExpirationAt is when the wallet expires (ISO 8601). Empty means no expiration.
	// +optional
	ExpirationAt string `json:"expirationAt,omitempty"`

	// RecurringTopUp configures automatic wallet top-up rules.
	// +optional
	RecurringTopUp *WalletRecurringTopUp `json:"recurringTopUp,omitempty"`
}

// WalletRecurringTopUp defines automatic top-up configuration.
type WalletRecurringTopUp struct {
	// Method is the top-up trigger: "fixed" (periodic) or "target" (balance threshold).
	// +kubebuilder:validation:Enum=fixed;target
	Method string `json:"method"`

	// ThresholdCredits triggers top-up when balance falls below this (target method).
	// +optional
	ThresholdCredits string `json:"thresholdCredits,omitempty"`

	// PaidCredits is the amount to top up each time.
	PaidCredits string `json:"paidCredits"`

	// GrantedCredits is the free credits to add on each top-up.
	// +optional
	GrantedCredits string `json:"grantedCredits,omitempty"`

	// Interval is the top-up frequency for fixed method: "weekly" or "monthly" or "quarterly" or "yearly".
	// +optional
	Interval string `json:"interval,omitempty"`
}

// InvoraBillingWalletStatus defines the observed state.
type InvoraBillingWalletStatus struct {
	BillingResourceStatus `json:",inline"`

	// WalletID is the billing-backend-assigned UUID for this wallet.
	// +optional
	WalletID string `json:"walletId,omitempty"`

	// BalanceCredits is the current credit balance (last observed).
	// +optional
	BalanceCredits string `json:"balanceCredits,omitempty"`

	// ConsumedCredits is the total credits consumed (last observed).
	// +optional
	ConsumedCredits string `json:"consumedCredits,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iwallet
// +kubebuilder:printcolumn:name="Customer",type=string,JSONPath=`.spec.externalCustomerId`
// +kubebuilder:printcolumn:name="Currency",type=string,JSONPath=`.spec.currency`
// +kubebuilder:printcolumn:name="Balance",type=string,JSONPath=`.status.balanceCredits`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraBillingWallet represents a prepaid credit wallet attached to a customer.
type InvoraBillingWallet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraBillingWalletSpec   `json:"spec,omitempty"`
	Status InvoraBillingWalletStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraBillingWalletList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraBillingWallet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InvoraBillingWallet{}, &InvoraBillingWalletList{})
}
