package v1alpha1

// ResourceRef identifies a Kubernetes resource by name and optional namespace.
type ResourceRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}
