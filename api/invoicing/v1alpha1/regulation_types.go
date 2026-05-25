package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraInvoicingRegulationSpec defines regulation enrollment config for a branch.
// Each regulation plugin (ZATCA, ETA Egypt, Peppol) has its own enrollment
// requirements and credentials.
type InvoraInvoicingRegulationSpec struct {
	// InstanceRef references the InvoraInstance (gateway connection).
	InstanceRef ResourceRef `json:"instanceRef"`

	// BranchRef references the InvoraBranch this regulation applies to.
	BranchRef ResourceRef `json:"branchRef"`

	// RegulationType identifies the regulation plugin.
	// +kubebuilder:validation:Enum=zatca;eta_egypt;peppol_sa;peppol_ae;peppol_be;peppol_de;peppol_sg;jordan;uae
	RegulationType string `json:"regulationType"`

	// Enabled controls whether this regulation is actively enforced.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// SubmissionModel defines how documents are submitted to the authority.
	// +kubebuilder:validation:Enum=clearance;network_delivery;reporting
	// +optional
	SubmissionModel string `json:"submissionModel,omitempty"`

	// CredentialsRef references the Secret containing regulation-specific
	// credentials (e.g., ZATCA CSID certificate, ETA client credentials).
	// +optional
	CredentialsRef *SecretKeySelector `json:"credentialsRef,omitempty"`

	// Config holds regulation-specific configuration as key-value pairs.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// SecretKeySelector references a key within a Secret.
type SecretKeySelector struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Key       string `json:"key"`
}

// InvoraInvoicingRegulationStatus defines the observed state.
type InvoraInvoicingRegulationStatus struct {
	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// EnrollmentStatus reflects the backend enrollment state.
	// +optional
	EnrollmentStatus string `json:"enrollmentStatus,omitempty"`

	// ObservedGeneration is the last spec generation the controller acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncedAt is the last time this resource was synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ireg
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.regulationType`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.submissionModel`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.enrollmentStatus`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraInvoicingRegulation represents regulation enrollment config for a branch.
type InvoraInvoicingRegulation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraInvoicingRegulationSpec   `json:"spec,omitempty"`
	Status InvoraInvoicingRegulationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraInvoicingRegulationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraInvoicingRegulation `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraInvoicingRegulation{}, &InvoraInvoicingRegulationList{}) }
