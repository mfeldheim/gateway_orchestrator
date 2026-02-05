package gateway

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// MaxCertificatesPerGateway is the soft limit for certs per Gateway (ALB SNI limit ~25)
	MaxCertificatesPerGateway = 20

	// MaxRulesPerGateway is the soft limit for rules per Gateway
	MaxRulesPerGateway = 100
)

// Pool manages the Gateway pool
type Pool struct {
	client       client.Client
	namespace    string
	gatewayClass string
}

// NewPool creates a new Gateway pool manager
func NewPool(c client.Client, namespace, gatewayClass string) *Pool {
	return &Pool{
		client:       c,
		namespace:    namespace,
		gatewayClass: gatewayClass,
	}
}

// GatewayInfo holds Gateway metadata and capacity info
type GatewayInfo struct {
	Name             string
	Namespace        string
	CertificateCount int
	RuleCount        int
	LoadBalancerDNS  string
	LoadBalancerZone string
}

// SelectGateway chooses an appropriate Gateway from the pool using first-fit
// If selector is specified, only Gateways matching the label selector will be considered
func (p *Pool) SelectGateway(ctx context.Context, visibility string, selector *metav1.LabelSelector) (*GatewayInfo, error) {
	// List all Gateways in the namespace
	var gatewayList gwapiv1.GatewayList
	if err := p.client.List(ctx, &gatewayList, client.InNamespace(p.namespace)); err != nil {
		return nil, fmt.Errorf("failed to list gateways: %w", err)
	}

	// Convert selector to labels.Selector for matching
	var labelSelector labels.Selector
	if selector != nil {
		var err error
		labelSelector, err = metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return nil, fmt.Errorf("invalid gateway selector: %w", err)
		}
	}

	// Filter by gatewayClass, visibility, and optional label selector
	for _, gw := range gatewayList.Items {
		if string(gw.Spec.GatewayClassName) != p.gatewayClass {
			continue
		}

		// Check annotations for visibility
		gwVisibility := gw.Annotations["gateway.opendi.com/visibility"]
		if gwVisibility != visibility {
			continue
		}

		// Check label selector if specified
		if labelSelector != nil && !labelSelector.Matches(labels.Set(gw.Labels)) {
			continue
		}

		// Get capacity info
		info := p.getGatewayInfo(&gw)

		// Check if Gateway has capacity (first-fit)
		if info.CertificateCount < MaxCertificatesPerGateway && info.RuleCount < MaxRulesPerGateway {
			return info, nil
		}
	}

	// No Gateway with capacity found, need to create new one
	return nil, nil
}

// getGatewayInfo extracts capacity information from a Gateway
func (p *Pool) getGatewayInfo(gw *gwapiv1.Gateway) *GatewayInfo {
	info := &GatewayInfo{
		Name:      gw.Name,
		Namespace: gw.Namespace,
	}

	// Parse certificate count from annotations (updated by reconciler)
	if certCount, ok := gw.Annotations["gateway.opendi.com/certificate-count"]; ok {
		fmt.Sscanf(certCount, "%d", &info.CertificateCount)
	}

	// Parse rule count from annotations
	if ruleCount, ok := gw.Annotations["gateway.opendi.com/rule-count"]; ok {
		fmt.Sscanf(ruleCount, "%d", &info.RuleCount)
	}

	// Extract LoadBalancer info from status
	for _, addr := range gw.Status.Addresses {
		if addr.Type != nil && *addr.Type == gwapiv1.HostnameAddressType {
			info.LoadBalancerDNS = addr.Value
		}
	}

	return info
}

// CreateGateway creates a new Gateway in the pool
func (p *Pool) CreateGateway(ctx context.Context, visibility string, index int) (*GatewayInfo, error) {
	name := fmt.Sprintf("gw-%02d", index)

	gw := &gwapiv1.Gateway{}
	gw.Name = name
	gw.Namespace = p.namespace
	gw.Annotations = map[string]string{
		"gateway.opendi.com/visibility":        visibility,
		"gateway.opendi.com/certificate-count": "0",
		"gateway.opendi.com/rule-count":        "0",
		"alb.ingress.kubernetes.io/scheme":     visibility, // AWS LBC annotation for ALB scheme
	}
	gw.Spec.GatewayClassName = gwapiv1.ObjectName(p.gatewayClass)

	// Configure listeners based on visibility
	// AWS Load Balancer Controller will provision the ALB
	gw.Spec.Listeners = []gwapiv1.Listener{
		{
			Name:     "https",
			Protocol: gwapiv1.HTTPSProtocolType,
			Port:     443,
			TLS: &gwapiv1.ListenerTLSConfig{
				Mode:            ptrTo(gwapiv1.TLSModeTerminate),
				CertificateRefs: []gwapiv1.SecretObjectReference{}, // Empty initially, certificates added via annotations
			},
		},
		{
			Name:     "http",
			Protocol: gwapiv1.HTTPProtocolType,
			Port:     80,
		},
	}

	if err := p.client.Create(ctx, gw); err != nil {
		return nil, fmt.Errorf("failed to create gateway %s: %w", name, err)
	}

	return &GatewayInfo{
		Name:      name,
		Namespace: p.namespace,
	}, nil
}

// ptrTo returns a pointer to the given value
func ptrTo[T any](v T) *T {
	return &v
}

// GetNextGatewayIndex returns the next available Gateway index
func (p *Pool) GetNextGatewayIndex(ctx context.Context) (int, error) {
	var gatewayList gwapiv1.GatewayList
	if err := p.client.List(ctx, &gatewayList, client.InNamespace(p.namespace)); err != nil {
		return 0, fmt.Errorf("failed to list gateways: %w", err)
	}

	maxIndex := 0
	for _, gw := range gatewayList.Items {
		var idx int
		if _, err := fmt.Sscanf(gw.Name, "gw-%d", &idx); err == nil {
			if idx > maxIndex {
				maxIndex = idx
			}
		}
	}

	return maxIndex + 1, nil
}
