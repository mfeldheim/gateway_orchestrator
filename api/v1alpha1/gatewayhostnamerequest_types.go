package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GatewayHostnameRequestSpec defines the desired state of GatewayHostnameRequest
type GatewayHostnameRequestSpec struct {
	// ZoneId is the Route53 hosted zone ID where DNS records will be created
	// +kubebuilder:validation:Required
	ZoneId string `json:"zoneId"`

	// Hostname is the FQDN to expose (e.g., test.opendi.com)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^([a-z0-9]+(-[a-z0-9]+)*\.)+[a-z]{2,}$`
	Hostname string `json:"hostname"`

	// Environment is the logical environment (dev, staging, prod)
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=dev;staging;prod
	Environment string `json:"environment,omitempty"`

	// Visibility specifies whether the Gateway should be internet-facing or internal
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=internet-facing;internal
	// +kubebuilder:default=internet-facing
	Visibility string `json:"visibility,omitempty"`

	// GatewayClass specifies which GatewayClass to use
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=aws-alb
	GatewayClass string `json:"gatewayClass,omitempty"`
}

// GatewayHostnameRequestStatus defines the observed state of GatewayHostnameRequest
type GatewayHostnameRequestStatus struct {
	// AssignedGateway is the name of the Gateway this hostname is assigned to
	// +optional
	AssignedGateway string `json:"assignedGateway,omitempty"`

	// AssignedGatewayNamespace is the namespace of the assigned Gateway
	// +optional
	AssignedGatewayNamespace string `json:"assignedGatewayNamespace,omitempty"`

	// AssignedLoadBalancer is the ALB DNS name
	// +optional
	AssignedLoadBalancer string `json:"assignedLoadBalancer,omitempty"`

	// CertificateArn is the ACM certificate ARN
	// +optional
	CertificateArn string `json:"certificateArn,omitempty"`

	// Conditions represent the latest available observations of an object's state
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ghr
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.status.assignedGateway`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GatewayHostnameRequest is the Schema for the gatewayhostnamerequests API
type GatewayHostnameRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GatewayHostnameRequestSpec   `json:"spec,omitempty"`
	Status GatewayHostnameRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GatewayHostnameRequestList contains a list of GatewayHostnameRequest
type GatewayHostnameRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GatewayHostnameRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GatewayHostnameRequest{}, &GatewayHostnameRequestList{})
}
