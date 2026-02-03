package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DomainClaimSpec defines the desired state of DomainClaim
type DomainClaimSpec struct {
	// ZoneId is the Route53 hosted zone ID
	// +kubebuilder:validation:Required
	ZoneId string `json:"zoneId"`

	// Hostname is the claimed FQDN
	// +kubebuilder:validation:Required
	Hostname string `json:"hostname"`

	// OwnerRef references the GatewayHostnameRequest that owns this claim
	// +kubebuilder:validation:Required
	OwnerRef DomainClaimOwnerRef `json:"ownerRef"`
}

type DomainClaimOwnerRef struct {
	// Namespace of the owning GatewayHostnameRequest
	Namespace string `json:"namespace"`

	// Name of the owning GatewayHostnameRequest
	Name string `json:"name"`

	// UID of the owning GatewayHostnameRequest
	UID string `json:"uid"`
}

// DomainClaimStatus defines the observed state of DomainClaim
type DomainClaimStatus struct {
	// ClaimedAt is the timestamp when the claim was established
	// +optional
	ClaimedAt *metav1.Time `json:"claimedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=dc
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.ownerRef.namespace`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DomainClaim is the Schema for the domainclaims API
// Implements atomic first-come-first-serve for (zoneId, hostname) pairs
type DomainClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DomainClaimSpec   `json:"spec,omitempty"`
	Status DomainClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DomainClaimList contains a list of DomainClaim
type DomainClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DomainClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DomainClaim{}, &DomainClaimList{})
}
