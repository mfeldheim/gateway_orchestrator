package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	gwInfo, err := r.GatewayPool.SelectGateway(ctx, visibility, ghr.Spec.WafArn, ghr.Spec.GatewaySelector)
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

		gatewayName := fmt.Sprintf("gw-%02d", index)
		gatewayNamespace := r.GatewayPool.Namespace()

		// Create LoadBalancerConfiguration FIRST with the initial certificate
		initialCerts := []string{ghr.Status.CertificateArn}
		if err := r.ensureLoadBalancerConfiguration(ctx, gatewayName, gatewayNamespace, initialCerts, visibility, ghr.Spec.WafArn); err != nil {
			return fmt.Errorf("failed to create LoadBalancerConfiguration: %w", err)
		}

		// Now create Gateway referencing the LoadBalancerConfiguration
		gwInfo, err = r.GatewayPool.CreateGateway(ctx, visibility, ghr.Spec.WafArn, index)
		if err != nil {
			return fmt.Errorf("failed to create new gateway: %w", err)
		}
		logger.Info("Created new Gateway with LoadBalancerConfiguration", "name", gwInfo.Name, "index", index)

		// Update status with assigned Gateway
		ghr.Status.AssignedGateway = gwInfo.Name
		ghr.Status.AssignedGatewayNamespace = gwInfo.Namespace

		logger.Info("Successfully assigned to Gateway", "gateway", gwInfo.Name, "hostname", ghr.Spec.Hostname)
		return nil
	}

	// Update status with assigned Gateway (existing Gateway case)
	ghr.Status.AssignedGateway = gwInfo.Name
	ghr.Status.AssignedGatewayNamespace = gwInfo.Namespace

	// Sync LoadBalancerConfiguration to add this certificate to existing Gateway
	if err := r.syncLoadBalancerConfiguration(ctx, gwInfo.Name, gwInfo.Namespace, visibility, ghr.Spec.WafArn, ghr.Status.CertificateArn); err != nil {
		return fmt.Errorf("failed to sync LoadBalancerConfiguration: %w", err)
	}

	logger.Info("Successfully assigned to Gateway", "gateway", gwInfo.Name, "hostname", ghr.Spec.Hostname)
	return nil
}

// syncLoadBalancerConfiguration collects all certificate ARNs for a Gateway and updates its LoadBalancerConfiguration
// If newCertARN is provided, it's included even if the GHR isn't assigned yet
func (r *GatewayHostnameRequestReconciler) syncLoadBalancerConfiguration(ctx context.Context, gatewayName, gatewayNamespace, visibility, wafArn, newCertARN string) error {
	// Collect all certificate ARNs from GatewayHostnameRequests assigned to this Gateway
	arns, err := r.getGatewayCertificateARNs(ctx, gatewayName, gatewayNamespace)
	if err != nil {
		return err
	}

	// Add the new cert if not already in the list
	if newCertARN != "" {
		found := false
		for _, arn := range arns {
			if arn == newCertARN {
				found = true
				break
			}
		}
		if !found {
			arns = append(arns, newCertARN)
		}
	}

	// Create or update the LoadBalancerConfiguration
	return r.ensureLoadBalancerConfiguration(ctx, gatewayName, gatewayNamespace, arns, visibility, wafArn)
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

	wafArn := gw.Annotations["gateway.opendi.com/waf-arn"]

	// Re-sync LoadBalancerConfiguration (this will exclude the deleted GHR's certificate)
	if err := r.syncLoadBalancerConfiguration(ctx, ghr.Status.AssignedGateway, ghr.Status.AssignedGatewayNamespace, visibility, wafArn, ""); err != nil {
		return fmt.Errorf("failed to sync LoadBalancerConfiguration after certificate removal: %w", err)
	}

	// NOTE: WAF Orphan Scenario
	// If this is the last GHR deleted and it had a custom WAF, the Gateway's WAF annotation remains.
	// The WAF is no longer in use but not cleared from the annotation. This is acceptable because:
	// 1. The Gateway might be reused later for another WAF-protected hostname
	// 2. Clearing it would require cross-cluster state tracking
	// 3. The orphaned WAF annotation doesn't affect functionality (just unused metadata)
	// If needed, operators can manually clear it via: kubectl annotate gateway gw-01 gateway.opendi.com/waf-arn=""

	logger.Info("Removed certificate from Gateway",
		"gateway", ghr.Status.AssignedGateway,
		"certificateArn", ghr.Status.CertificateArn)

	return nil
}

// ensureAllowedRoutes ensures the Gateway allows HTTPRoutes from all namespaces.
// Security is enforced by HostnameGrant + policy engine (Kyverno/Gatekeeper),
// not by Gateway allowedRoutes restrictions.
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

	updated := false
	fromAll := gwapiv1.NamespacesFromAll
	for i := range gw.Spec.Listeners {
		listener := &gw.Spec.Listeners[i]

		// Ensure AllowedRoutes is set to allow from all namespaces
		needsUpdate := listener.AllowedRoutes == nil ||
			listener.AllowedRoutes.Namespaces == nil ||
			listener.AllowedRoutes.Namespaces.From == nil ||
			*listener.AllowedRoutes.Namespaces.From != fromAll

		if needsUpdate {
			listener.AllowedRoutes = &gwapiv1.AllowedRoutes{
				Namespaces: &gwapiv1.RouteNamespaces{
					From: &fromAll,
				},
			}
			updated = true
		}
	}

	if updated {
		if err := r.Update(ctx, &gw); err != nil {
			return fmt.Errorf("failed to update gateway allowedRoutes: %w", err)
		}
		logger.Info("Updated Gateway allowedRoutes to allow all namespaces", "gateway", gw.Name)
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

// cleanupEmptyGateway checks if a Gateway has any assigned GatewayHostnameRequests.
// If not, it deletes the Gateway and its LoadBalancerConfiguration.
// excludeGHRNamespace and excludeGHRName identify the currently-deleting GHR to exclude from the count.
// This implements idempotent cleanup: if the gateway is already deleted, it returns nil.
func (r *GatewayHostnameRequestReconciler) cleanupEmptyGateway(ctx context.Context, gatewayName, gatewayNamespace, excludeGHRNamespace, excludeGHRName string) error {
	logger := log.FromContext(ctx)

	// Count how many GatewayHostnameRequests are still assigned to this Gateway
	// (excluding the one currently being deleted)
	var ghrList gatewayv1alpha1.GatewayHostnameRequestList
	if err := r.List(ctx, &ghrList); err != nil {
		logger.Error(err, "Failed to list GatewayHostnameRequests while checking if Gateway is empty")
		return err
	}

	assignmentCount := 0
	for _, ghr := range ghrList.Items {
		// Skip the GHR that's currently being deleted
		if ghr.Namespace == excludeGHRNamespace && ghr.Name == excludeGHRName {
			continue
		}
		// Check both gateway name AND namespace to avoid cross-namespace confusion
		if ghr.Status.AssignedGateway == gatewayName &&
			ghr.Status.AssignedGatewayNamespace == gatewayNamespace {
			assignmentCount++
		}
	}

	// If there are still assignments, don't delete the Gateway
	if assignmentCount > 0 {
		logger.Info("Gateway still has assignments, not cleaning up", "gateway", gatewayName, "assignments", assignmentCount)
		return nil
	}

	logger.Info("Gateway has no remaining assignments, cleaning up", "gateway", gatewayName)

	// Step 1: Delete LoadBalancerConfiguration
	lbcName := fmt.Sprintf("%s-config", gatewayName)
	lbcKey := types.NamespacedName{Name: lbcName, Namespace: gatewayNamespace}
	lbc := &unstructured.Unstructured{}
	lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	if err := r.Get(ctx, lbcKey, lbc); err == nil {
		// LoadBalancerConfiguration exists, delete it
		if err := r.Delete(ctx, lbc); err != nil {
			logger.Error(err, "Failed to delete LoadBalancerConfiguration", "name", lbcName)
			return fmt.Errorf("failed to delete LoadBalancerConfiguration: %w", err)
		}
		logger.Info("Deleted LoadBalancerConfiguration", "name", lbcName)
	}
	// If not found, that's fine - it may have been manually deleted

	// Step 2: Delete Gateway
	var gw gwapiv1.Gateway
	gwKey := types.NamespacedName{Name: gatewayName, Namespace: gatewayNamespace}
	if err := r.Get(ctx, gwKey, &gw); err == nil {
		// Gateway exists, delete it
		if err := r.Delete(ctx, &gw); err != nil {
			logger.Error(err, "Failed to delete Gateway", "name", gatewayName)
			return fmt.Errorf("failed to delete Gateway: %w", err)
		}
		logger.Info("Deleted Gateway", "name", gatewayName)
	}
	// If not found, gateway is already deleted - success

	return nil
}

// isGatewayEmpty checks whether a Gateway has any GHR assignments remaining,
// excluding the specified GHR (which is being deleted).
func (r *GatewayHostnameRequestReconciler) isGatewayEmpty(ctx context.Context, gatewayName, gatewayNamespace, excludeGHRNamespace, excludeGHRName string) (bool, error) {
	var ghrList gatewayv1alpha1.GatewayHostnameRequestList
	if err := r.List(ctx, &ghrList); err != nil {
		return false, err
	}

	for _, ghr := range ghrList.Items {
		if ghr.Namespace == excludeGHRNamespace && ghr.Name == excludeGHRName {
			continue
		}
		if ghr.Status.AssignedGateway == gatewayName &&
			ghr.Status.AssignedGatewayNamespace == gatewayNamespace {
			return false, nil
		}
	}
	return true, nil
}
