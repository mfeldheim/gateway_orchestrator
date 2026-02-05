package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
)

// Annotations we use for tracking
const (
	AnnotationCertificateCount = "gateway.opendi.com/certificate-count"
	AnnotationRuleCount        = "gateway.opendi.com/rule-count"
	AnnotationVisibility       = "gateway.opendi.com/visibility"

	// LabelGatewayAccess is applied to namespaces that are allowed to create HTTPRoutes for a Gateway
	LabelGatewayAccess = "gateway.opendi.com/access"
)

// ensureGatewayAssignment assigns the request to a Gateway and attaches the certificate
func (r *GatewayHostnameRequestReconciler) ensureGatewayAssignment(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	// If already assigned, verify the assignment is still valid
	if ghr.Status.AssignedGateway != "" {
		var gw gwapiv1.Gateway
		err := r.Get(ctx, types.NamespacedName{
			Name:      ghr.Status.AssignedGateway,
			Namespace: ghr.Status.AssignedGatewayNamespace,
		}, &gw)
		if err == nil {
			// Gateway still exists, ensure certificate is attached
			return r.attachCertificateToGateway(ctx, ghr, &gw)
		}
		// Gateway no longer exists, need to reassign
		logger.Info("Previously assigned Gateway not found, reassigning", "gateway", ghr.Status.AssignedGateway)
	}

	// Select or create a Gateway from the pool
	visibility := ghr.Spec.Visibility
	if visibility == "" {
		visibility = "internet-facing"
	}

	gwInfo, err := r.GatewayPool.SelectGateway(ctx, visibility, ghr.Spec.GatewaySelector)
	if err != nil {
		return fmt.Errorf("failed to select gateway: %w", err)
	}

	// If no Gateway found with capacity, create a new one (unless a selector is specified)
	if gwInfo == nil {
		if ghr.Spec.GatewaySelector != nil {
			return fmt.Errorf("no Gateway matching selector with available capacity")
		}
		logger.Info("No Gateway with capacity found, creating new Gateway")
		index, err := r.GatewayPool.GetNextGatewayIndex(ctx)
		if err != nil {
			return fmt.Errorf("failed to get next gateway index: %w", err)
		}

		// Create Gateway (without certificate - that comes via LoadBalancerConfiguration)
		gwInfo, err = r.GatewayPool.CreateGateway(ctx, visibility, index)
		if err != nil {
			return fmt.Errorf("failed to create new gateway: %w", err)
		}
		logger.Info("Created new Gateway", "name", gwInfo.Name, "index", index)
	}

	// Update status with assigned Gateway
	ghr.Status.AssignedGateway = gwInfo.Name
	ghr.Status.AssignedGatewayNamespace = gwInfo.Namespace

	// Attach certificate via LoadBalancerConfiguration
	if err := r.syncLoadBalancerConfiguration(ctx, gwInfo.Name, gwInfo.Namespace, visibility); err != nil {
		return fmt.Errorf("failed to sync LoadBalancerConfiguration: %w", err)
	}

	logger.Info("Successfully assigned to Gateway", "gateway", gwInfo.Name, "hostname", ghr.Spec.Hostname)
	return nil
}

// syncLoadBalancerConfiguration collects all certificate ARNs for a Gateway and updates its LoadBalancerConfiguration
func (r *GatewayHostnameRequestReconciler) syncLoadBalancerConfiguration(ctx context.Context, gatewayName, gatewayNamespace, visibility string) error {
	// Collect all certificate ARNs from GatewayHostnameRequests assigned to this Gateway
	arns, err := r.getGatewayCertificateARNs(ctx, gatewayName, gatewayNamespace)
	if err != nil {
		return err
	}

	// Create or update the LoadBalancerConfiguration
	return r.ensureLoadBalancerConfiguration(ctx, gatewayName, gatewayNamespace, arns, visibility)
}

// attachCertificateToGateway is now a no-op - certificates are managed via LoadBalancerConfiguration
// Keeping for backwards compatibility during transition
func (r *GatewayHostnameRequestReconciler) attachCertificateToGateway(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest, gw *gwapiv1.Gateway) error {
	// Certificate attachment now happens via syncLoadBalancerConfiguration
	// This function is kept for interface compatibility but does nothing
	return nil
}

// removeCertificateFromGateway removes the certificate by re-syncing the LoadBalancerConfiguration
func (r *GatewayHostnameRequestReconciler) removeCertificateFromGateway(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	if ghr.Status.AssignedGateway == "" {
		return nil
	}

	// Get Gateway to find visibility setting
	var gw gwapiv1.Gateway
	err := r.Get(ctx, types.NamespacedName{
		Name:      ghr.Status.AssignedGateway,
		Namespace: ghr.Status.AssignedGatewayNamespace,
	}, &gw)
	if err != nil {
		// Gateway might be deleted already
		return nil
	}

	visibility := gw.Annotations[AnnotationVisibility]
	if visibility == "" {
		visibility = "internet-facing"
	}

	// Re-sync LoadBalancerConfiguration (this will exclude the deleted GHR's certificate)
	if err := r.syncLoadBalancerConfiguration(ctx, ghr.Status.AssignedGateway, ghr.Status.AssignedGatewayNamespace, visibility); err != nil {
		return fmt.Errorf("failed to sync LoadBalancerConfiguration after certificate removal: %w", err)
	}

	logger.Info("Removed certificate from Gateway",
		"gateway", ghr.Status.AssignedGateway,
		"certificateArn", ghr.Status.CertificateArn)

	return nil
}

// ensureAllowedRoutes updates the Gateway to allow HTTPRoutes from the requesting namespace
func (r *GatewayHostnameRequestReconciler) ensureAllowedRoutes(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	if ghr.Status.AssignedGateway == "" {
		return fmt.Errorf("no gateway assigned")
	}

	var gw gwapiv1.Gateway
	err := r.Get(ctx, types.NamespacedName{
		Name:      ghr.Status.AssignedGateway,
		Namespace: ghr.Status.AssignedGatewayNamespace,
	}, &gw)
	if err != nil {
		return fmt.Errorf("failed to get gateway: %w", err)
	}

	// Find the HTTPS listener
	updated := false
	for i := range gw.Spec.Listeners {
		listener := &gw.Spec.Listeners[i]

		if listener.Protocol == gwapiv1.HTTPSProtocolType || listener.Protocol == gwapiv1.HTTPProtocolType {
			// Initialize AllowedRoutes if needed
			if listener.AllowedRoutes == nil {
				listener.AllowedRoutes = &gwapiv1.AllowedRoutes{}
			}

			// Allow HTTPRoute kind
			httpRouteKind := gwapiv1.Kind("HTTPRoute")
			listener.AllowedRoutes.Kinds = []gwapiv1.RouteGroupKind{
				{
					Group: (*gwapiv1.Group)(stringPtr("gateway.networking.k8s.io")),
					Kind:  httpRouteKind,
				},
			}

			// Allow from namespaces with the gateway access label
			fromNamespaces := gwapiv1.NamespacesFromSelector
			listener.AllowedRoutes.Namespaces = &gwapiv1.RouteNamespaces{
				From: &fromNamespaces,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						LabelGatewayAccess: gw.Name,
					},
				},
			}

			updated = true
		}
	}

	if updated {
		if err := r.Update(ctx, &gw); err != nil {
			return fmt.Errorf("failed to update gateway allowedRoutes: %w", err)
		}
		logger.Info("Updated Gateway allowedRoutes", "gateway", gw.Name, "namespace", ghr.Namespace)
	}

	return nil
}

// ensureRoute53Alias creates or updates the Route53 ALIAS record pointing to the ALB
func (r *GatewayHostnameRequestReconciler) ensureRoute53Alias(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	if ghr.Status.AssignedGateway == "" {
		return fmt.Errorf("no gateway assigned")
	}

	// Get the Gateway to extract LoadBalancer info from status
	var gw gwapiv1.Gateway
	err := r.Get(ctx, types.NamespacedName{
		Name:      ghr.Status.AssignedGateway,
		Namespace: ghr.Status.AssignedGatewayNamespace,
	}, &gw)
	if err != nil {
		return fmt.Errorf("failed to get gateway: %w", err)
	}

	// Extract LoadBalancer DNS name from Gateway status (populated by AWS Load Balancer Controller)
	var lbDNS string
	for _, addr := range gw.Status.Addresses {
		if addr.Type != nil && *addr.Type == gwapiv1.HostnameAddressType {
			lbDNS = addr.Value
			break
		}
	}

	if lbDNS == "" {
		// LoadBalancer not yet provisioned by AWS Load Balancer Controller
		return fmt.Errorf("gateway %s does not have LoadBalancer address yet", gw.Name)
	}

	// Extract region from ALB DNS name and get the canonical hosted zone ID
	region, err := aws.ExtractRegionFromALBDNS(lbDNS)
	if err != nil {
		return fmt.Errorf("failed to extract region from ALB DNS: %w", err)
	}

	hostedZoneID, err := aws.GetALBHostedZoneID(region)
	if err != nil {
		return fmt.Errorf("failed to get ALB hosted zone ID: %w", err)
	}

	// Update status with LoadBalancer info
	ghr.Status.AssignedLoadBalancer = lbDNS

	// Create Route53 ALIAS record
	record := aws.DNSRecord{
		Name: ghr.Spec.Hostname,
		Type: "A", // ALIAS record for A record type
		AliasTarget: &aws.AliasTarget{
			DNSName:              lbDNS,
			HostedZoneID:         hostedZoneID,
			EvaluateTargetHealth: true,
		},
	}

	if err := r.Route53Client.CreateOrUpdateRecord(ctx, ghr.Spec.ZoneId, record); err != nil {
		return fmt.Errorf("failed to create Route53 ALIAS record: %w", err)
	}

	logger.Info("Created Route53 ALIAS record",
		"hostname", ghr.Spec.Hostname,
		"target", lbDNS,
		"region", region,
		"hostedZoneId", hostedZoneID,
		"zoneId", ghr.Spec.ZoneId)

	return nil
}

func stringPtr(s string) *string {
	return &s
}

// ensureNamespaceLabel labels the requesting namespace to allow HTTPRoute creation for the assigned Gateway
func (r *GatewayHostnameRequestReconciler) ensureNamespaceLabel(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	// Get the namespace
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: ghr.Namespace}, &ns); err != nil {
		return fmt.Errorf("failed to get namespace %s: %w", ghr.Namespace, err)
	}

	// Check if label already exists
	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}

	gatewayName := ghr.Status.AssignedGateway
	if gatewayName == "" {
		return fmt.Errorf("no gateway assigned yet")
	}

	// Add or update the label
	if ns.Labels[LabelGatewayAccess] != gatewayName {
		ns.Labels[LabelGatewayAccess] = gatewayName
		if err := r.Update(ctx, &ns); err != nil {
			return fmt.Errorf("failed to update namespace label: %w", err)
		}
		logger.Info("Added gateway access label to namespace", "namespace", ghr.Namespace, "gateway", gatewayName)
	}

	return nil
}

// removeNamespaceLabel removes the gateway access label from the namespace
func (r *GatewayHostnameRequestReconciler) removeNamespaceLabel(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	// Get the namespace
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: ghr.Namespace}, &ns); err != nil {
		// Namespace might be deleted already
		return nil
	}

	if ns.Labels == nil {
		return nil
	}

	// Remove the label if it exists
	if _, exists := ns.Labels[LabelGatewayAccess]; exists {
		delete(ns.Labels, LabelGatewayAccess)
		if err := r.Update(ctx, &ns); err != nil {
			return fmt.Errorf("failed to remove namespace label: %w", err)
		}
		logger.Info("Removed gateway access label from namespace", "namespace", ghr.Namespace)
	}

	return nil
}
