package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ResourceRef references a namespaced Kubernetes resource.
type ResourceRef struct {
	// Name of the referenced resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the referenced resource. Defaults to the namespace
	// of the referencing resource when omitted.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SecretKeyRef references a specific key within a Kubernetes Secret.
type SecretKeyRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Secret. Defaults to the referencing resource's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key within the Secret data.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// WriteSecretToRef specifies where to write generated sensitive outputs
// (e.g. API keys from organization creation). The controller creates the
// Secret if it does not exist and updates the data keys on every successful
// reconciliation. Ownership is set so that deleting the parent CR
// garbage-collects the Secret.
type WriteSecretToRef struct {
	// Name of the Secret to create or update.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Secret. Defaults to the referencing resource's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Labels to add to the created Secret.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations to add to the created Secret.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DeletionPolicy determines what happens to the external billing resource
// when the Kubernetes CR is deleted.
//
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	DeletionPolicyDelete DeletionPolicy = "Delete"
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// BillingResourceStatus is embedded in every CRD's status to provide
// common observability fields.
type BillingResourceStatus struct {
	// ID is the billing-backend-assigned resource identifier.
	// +optional
	ID string `json:"id,omitempty"`

	// ObservedGeneration is the most recent .metadata.generation observed
	// by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the
	// resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastSyncedAt is the last time the controller successfully reconciled
	// the resource against the billing API.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

const (
	ConditionReady              = "Ready"
	ConditionSynced             = "Synced"
	ConditionDependencyReady    = "DependencyReady"
	ConditionCredentialsWritten = "CredentialsWritten"
	ConditionDeletionBlocked    = "DeletionBlocked"
)

const (
	AnnotationImportID          = "billing.invora.app/import-id"
	AnnotationReconcileInterval = "billing.invora.app/reconcile-interval"
)

const FinalizerName = "billing.invora.app/cleanup"
