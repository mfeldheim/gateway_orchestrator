package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HostnameGrantSpec defines the desired state of HostnameGrant
type HostnameGrantSpec struct {
	// Namespace that is allowed to use these hostnames
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`

	// Hostnames that the namespace is allowed to use
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Hostnames []string `json:"hostnames"`
}

// HostnameGrantStatus defines the observed state of HostnameGrant
type HostnameGrantStatus struct {
	// GrantedAt is the timestamp when the grant was created
	// +optional
	GrantedAt *metav1.Time `json:"grantedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hg
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Hostnames",type=string,JSONPath=`.spec.hostnames`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HostnameGrant is the Schema for the hostnamegrants API
// Records which hostnames a namespace is allowed to use (consumed by policy engine)
type HostnameGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostnameGrantSpec   `json:"spec,omitempty"`
	Status HostnameGrantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HostnameGrantList contains a list of HostnameGrant
type HostnameGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostnameGrant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostnameGrant{}, &HostnameGrantList{})
}
