package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
)

// AWS Load Balancer Controller annotations for Gateways
const (
	// AnnotationCertificateARN specifies the ARN of ACM certificates for the ALB
	// Multiple certificates can be comma-separated (first is default, rest are SNI)
	AnnotationCertificateARN = "alb.ingress.kubernetes.io/certificate-arn"

	// AnnotationScheme specifies internet-facing or internal
	AnnotationScheme = "alb.ingress.kubernetes.io/scheme"

	// Annotations we use for tracking
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

		// Create Gateway with the certificate already attached
		gwInfo, err = r.GatewayPool.CreateGateway(ctx, visibility, index, ghr.Status.CertificateArn)
		if err != nil {
			return fmt.Errorf("failed to create new gateway: %w", err)
		}
		logger.Info("Created new Gateway", "name", gwInfo.Name, "index", index)
	}

	// Update status with assigned Gateway
	ghr.Status.AssignedGateway = gwInfo.Name
	ghr.Status.AssignedGatewayNamespace = gwInfo.Namespace

	// Get the actual Gateway resource to attach certificate (if not a newly created one)
	var gw gwapiv1.Gateway
	if err := r.Get(ctx, types.NamespacedName{
		Name:      gwInfo.Name,
		Namespace: gwInfo.Namespace,
	}, &gw); err != nil {
		return fmt.Errorf("failed to get gateway %s: %w", gwInfo.Name, err)
	}

	// Attach certificate to the Gateway
	// For newly created Gateways, certificate is already attached during creation
	// For existing Gateways, we need to add it to the annotation
	if err := r.attachCertificateToGateway(ctx, ghr, &gw); err != nil {
		return fmt.Errorf("failed to attach certificate to gateway: %w", err)
	}

	logger.Info("Successfully assigned to Gateway", "gateway", gwInfo.Name, "hostname", ghr.Spec.Hostname)
	return nil
}

// attachCertificateToGateway adds the ACM certificate ARN to the Gateway's annotations
// AWS Load Balancer Controller reads this annotation and attaches certs to the ALB listener
func (r *GatewayHostnameRequestReconciler) attachCertificateToGateway(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest, gw *gwapiv1.Gateway) error {
	logger := log.FromContext(ctx)

	if ghr.Status.CertificateArn == "" {
		return fmt.Errorf("certificate ARN not set in status")
	}

	// Initialize annotations if needed
	if gw.Annotations == nil {
		gw.Annotations = make(map[string]string)
	}

	// Get existing certificate ARNs from annotation
	existingCerts := []string{}
	if certArns, ok := gw.Annotations[AnnotationCertificateARN]; ok && certArns != "" {
		existingCerts = strings.Split(certArns, ",")
	}

	// Check if this certificate is already attached
	certAlreadyAttached := false
	for _, arn := range existingCerts {
		if strings.TrimSpace(arn) == ghr.Status.CertificateArn {
			certAlreadyAttached = true
			break
		}
	}

	if !certAlreadyAttached {
		// Add the new certificate to the list
		existingCerts = append(existingCerts, ghr.Status.CertificateArn)
		gw.Annotations[AnnotationCertificateARN] = strings.Join(existingCerts, ",")

		// Update certificate count annotation
		certCount := len(existingCerts)
		gw.Annotations[AnnotationCertificateCount] = fmt.Sprintf("%d", certCount)

		if err := r.Update(ctx, gw); err != nil {
			return fmt.Errorf("failed to update gateway annotations: %w", err)
		}

		logger.Info("Attached certificate to Gateway",
			"gateway", gw.Name,
			"certificateArn", ghr.Status.CertificateArn,
			"totalCerts", certCount)
	} else {
		logger.Info("Certificate already attached to Gateway",
			"gateway", gw.Name,
			"certificateArn", ghr.Status.CertificateArn)
	}

	return nil
}

// removeCertificateFromGateway removes the ACM certificate ARN from the Gateway's annotations
func (r *GatewayHostnameRequestReconciler) removeCertificateFromGateway(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	if ghr.Status.AssignedGateway == "" || ghr.Status.CertificateArn == "" {
		return nil
	}

	var gw gwapiv1.Gateway
	err := r.Get(ctx, types.NamespacedName{
		Name:      ghr.Status.AssignedGateway,
		Namespace: ghr.Status.AssignedGatewayNamespace,
	}, &gw)
	if err != nil {
		// Gateway might already be deleted
		return nil
	}

	// Get existing certificate ARNs from annotation
	if gw.Annotations == nil {
		return nil
	}

	certArns, ok := gw.Annotations[AnnotationCertificateARN]
	if !ok || certArns == "" {
		return nil
	}

	existingCerts := strings.Split(certArns, ",")
	newCerts := make([]string, 0, len(existingCerts))

	// Filter out the certificate to remove
	for _, arn := range existingCerts {
		arn = strings.TrimSpace(arn)
		if arn != "" && arn != ghr.Status.CertificateArn {
			newCerts = append(newCerts, arn)
		}
	}

	// Update the annotation
	if len(newCerts) == 0 {
		delete(gw.Annotations, AnnotationCertificateARN)
	} else {
		gw.Annotations[AnnotationCertificateARN] = strings.Join(newCerts, ",")
	}

	// Update certificate count
	gw.Annotations[AnnotationCertificateCount] = fmt.Sprintf("%d", len(newCerts))

	if err := r.Update(ctx, &gw); err != nil {
		return fmt.Errorf("failed to update gateway annotations: %w", err)
	}

	logger.Info("Removed certificate from Gateway",
		"gateway", gw.Name,
		"certificateArn", ghr.Status.CertificateArn,
		"remainingCerts", len(newCerts))

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
