package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InvoraInstanceSpec defines connection parameters to an Invora deployment.
// Every other CRD (billing, core, invoicing) references an InvoraInstance
// to know which gateway to communicate with.
type InvoraInstanceSpec struct {
	// GatewayURL is the base URL of the Invora gateway
	// (e.g., "https://gateway.invora.app").
	// +kubebuilder:validation:MinLength=1
	GatewayURL string `json:"gatewayUrl"`

	// TokenRef references the Secret containing the service account
	// token for authenticating to the gateway.
	TokenRef SecretKeyRef `json:"tokenRef"`
}

// SecretKeyRef references a specific key within a Kubernetes Secret.
type SecretKeyRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Key       string `json:"key"`
}

// InvoraInstanceStatus defines the observed state.
type InvoraInstanceStatus struct {
	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Connected indicates whether the gateway is reachable.
	// +optional
	Connected bool `json:"connected,omitempty"`

	// ObservedGeneration is the last spec generation the controller acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncedAt is the last time connectivity was verified.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iinst
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.spec.gatewayUrl`
// +kubebuilder:printcolumn:name="Connected",type=boolean,JSONPath=`.status.connected`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InvoraInstance defines a connection to an Invora deployment. It serves as
// the universal gateway entry point for all billing, core, and invoicing CRDs.
type InvoraInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InvoraInstanceSpec   `json:"spec,omitempty"`
	Status InvoraInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type InvoraInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InvoraInstance `json:"items"`
}

func init() { SchemeBuilder.Register(&InvoraInstance{}, &InvoraInstanceList{}) }
