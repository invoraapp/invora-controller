package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraInvoicingSettingsSpec defines tenant-level invoicing settings.
type InvoraInvoicingSettingsSpec struct {
	// InstanceRef references the InvoraBillingInstance (gateway connection).
	InstanceRef ResourceRef `json:"instanceRef"`

	// DefaultCurrency is the default currency for new documents (ISO 4217).
	// +optional
	DefaultCurrency string `json:"defaultCurrency,omitempty"`

	// DefaultLanguage is the default document language (ISO 639-1).
	// +optional
	DefaultLanguage string `json:"defaultLanguage,omitempty"`

	// DocumentNumberFormat defines the number format pattern.
	// +optional
	DocumentNumberFormat string `json:"documentNumberFormat,omitempty"`

	// AutoCalculate enables automatic total/tax calculation on document save.
	// +kubebuilder:default=true
	// +optional
	AutoCalculate bool `json:"autoCalculate,omitempty"`

	// DefaultPaymentTermsDays is the default net payment terms.
	// +optional
	DefaultPaymentTermsDays *int32 `json:"defaultPaymentTermsDays,omitempty"`
}

// InvoraInvoicingSettingsStatus defines the observed state.
type InvoraInvoicingSettingsStatus struct {
	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last spec generation the controller acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncedAt is the last time this resource was synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iset
// +kubebuilder:printcolumn:name="Currency",type=string,JSONPath=`.spec.defaultCurrency`
// +kubebuilder:printcolumn:name="Language",type=string,JSONPath=`.spec.defaultLanguage`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraInvoicingSettings represents tenant-level invoicing configuration.
type InvoraInvoicingSettings struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraInvoicingSettingsSpec   `json:"spec,omitempty"`
	Status InvoraInvoicingSettingsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraInvoicingSettingsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraInvoicingSettings `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraInvoicingSettings{}, &InvoraInvoicingSettingsList{}) }
