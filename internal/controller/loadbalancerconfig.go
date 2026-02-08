package controller

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
)

// LoadBalancerConfigurationGVK is the GVK for AWS LoadBalancerConfiguration
var LoadBalancerConfigurationGVK = schema.GroupVersionKind{
	Group:   "gateway.k8s.aws",
	Version: "v1beta1",
	Kind:    "LoadBalancerConfiguration",
}

// TargetGroupConfigurationGVK is the GVK for AWS TargetGroupConfiguration
var TargetGroupConfigurationGVK = schema.GroupVersionKind{
	Group:   "gateway.k8s.aws",
	Version: "v1beta1",
	Kind:    "TargetGroupConfiguration",
}

// ensureLoadBalancerConfiguration creates or updates the LoadBalancerConfiguration for a Gateway
// with all certificate ARNs from GatewayHostnameRequests assigned to that Gateway
// wafArn can be empty (no WAF) or a WAF ARN to associate with the load balancer
func (r *GatewayHostnameRequestReconciler) ensureLoadBalancerConfiguration(
	ctx context.Context,
	gatewayName string,
	gatewayNamespace string,
	certificateARNs []string,
	visibility string,
	wafArn string,
) error {
	logger := log.FromContext(ctx)

	configName := fmt.Sprintf("%s-config", gatewayName)

	// Build the LoadBalancerConfiguration
	lbConfig := &unstructured.Unstructured{}
	lbConfig.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	lbConfig.SetName(configName)
	lbConfig.SetNamespace(gatewayNamespace)

	// Try to get existing config
	existingConfig := &unstructured.Unstructured{}
	existingConfig.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	err := r.Get(ctx, types.NamespacedName{Name: configName, Namespace: gatewayNamespace}, existingConfig)

	// Build listener configuration with certificates
	listenerConfigs := []interface{}{}

	if len(certificateARNs) > 0 {
		// Sort certificates for deterministic ordering (ensures same default cert on each reconcile)
		// Make a copy to avoid mutating the input slice
		sortedCerts := make([]string, len(certificateARNs))
		copy(sortedCerts, certificateARNs)
		sort.Strings(sortedCerts)

		// HTTPS listener with certificates
		httpsListener := map[string]interface{}{
			"protocolPort":       fmt.Sprintf("HTTPS:%d", r.httpsPort()),
			"defaultCertificate": sortedCerts[0], // First cert is default (now deterministic)
		}
		if len(sortedCerts) > 1 {
			// Additional certs for SNI
			// Convert []string to []interface{} for unstructured object compatibility
			additionalCerts := make([]interface{}, len(sortedCerts)-1)
			for i, cert := range sortedCerts[1:] {
				additionalCerts[i] = cert
			}
			httpsListener["certificates"] = additionalCerts
		}
		listenerConfigs = append(listenerConfigs, httpsListener)
	}

	// HTTP listener (no certs needed)
	httpListener := map[string]interface{}{
		"protocolPort": fmt.Sprintf("HTTP:%d", r.httpPort()),
	}
	listenerConfigs = append(listenerConfigs, httpListener)

	// Build spec
	spec := map[string]interface{}{
		"scheme":                 visibility,
		"listenerConfigurations": listenerConfigs,
	}

	// Add WAF if specified
	if wafArn != "" {
		spec["wafV2"] = map[string]interface{}{
			"arnOrName": wafArn,
		}
	}

	if err != nil {
		// Create new config
		lbConfig.Object["spec"] = spec

		if err := r.Create(ctx, lbConfig); err != nil {
			return fmt.Errorf("failed to create LoadBalancerConfiguration %s: %w", configName, err)
		}
		logger.Info("Created LoadBalancerConfiguration", "name", configName, "certificates", len(certificateARNs))
	} else {
		// Update existing config
		existingConfig.Object["spec"] = spec
		if err := r.Update(ctx, existingConfig); err != nil {
			return fmt.Errorf("failed to update LoadBalancerConfiguration %s: %w", configName, err)
		}
		logger.Info("Updated LoadBalancerConfiguration", "name", configName, "certificates", len(certificateARNs))
	}

	return nil
}

// getGatewayCertificateARNs collects all certificate ARNs from GatewayHostnameRequests assigned to a Gateway
func (r *GatewayHostnameRequestReconciler) getGatewayCertificateARNs(ctx context.Context, gatewayName, gatewayNamespace string) ([]string, error) {
	// List all GatewayHostnameRequests
	var ghrList gatewayv1alpha1.GatewayHostnameRequestList
	if err := r.List(ctx, &ghrList); err != nil {
		return nil, fmt.Errorf("failed to list GatewayHostnameRequests: %w", err)
	}

	arns := []string{}
	for _, ghr := range ghrList.Items {
		if ghr.Status.AssignedGateway == gatewayName &&
			ghr.Status.AssignedGatewayNamespace == gatewayNamespace &&
			ghr.Status.CertificateArn != "" {
			arns = append(arns, ghr.Status.CertificateArn)
		}
	}

	return arns, nil
}

// httpPort returns the configured HTTP listener port, defaulting to 80
func (r *GatewayHostnameRequestReconciler) httpPort() int32 {
	if r.GatewayPool != nil {
		return r.GatewayPool.HTTPPort()
	}
	return 80
}

// httpsPort returns the configured HTTPS listener port, defaulting to 443
func (r *GatewayHostnameRequestReconciler) httpsPort() int32 {
	if r.GatewayPool != nil {
		return r.GatewayPool.HTTPSPort()
	}
	return 443
}

// deleteLoadBalancerConfiguration removes the LoadBalancerConfiguration for a Gateway
func (r *GatewayHostnameRequestReconciler) deleteLoadBalancerConfiguration(ctx context.Context, gatewayName, gatewayNamespace string) error {
	logger := log.FromContext(ctx)
	configName := fmt.Sprintf("%s-config", gatewayName)

	config := &unstructured.Unstructured{}
	config.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	config.SetName(configName)
	config.SetNamespace(gatewayNamespace)

	if err := r.Delete(ctx, config); err != nil {
		// Ignore not found
		return nil
	}

	logger.Info("Deleted LoadBalancerConfiguration", "name", configName)

	// Also delete the TargetGroupConfiguration
	_ = r.deleteTargetGroupConfiguration(ctx, gatewayName, gatewayNamespace)

	return nil
}

// ensureTargetGroupConfiguration creates or updates the TargetGroupConfiguration for a Gateway
// to use IP-based target groups, enabling ClusterIP services (default K8s service type).
func (r *GatewayHostnameRequestReconciler) ensureTargetGroupConfiguration(ctx context.Context, gatewayName, gatewayNamespace string) error {
	logger := log.FromContext(ctx)

	configName := fmt.Sprintf("%s-tgconfig", gatewayName)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(TargetGroupConfigurationGVK)
	err := r.Get(ctx, types.NamespacedName{Name: configName, Namespace: gatewayNamespace}, existing)

	spec := map[string]interface{}{
		"defaultConfiguration": map[string]interface{}{
			"targetType": "ip",
		},
	}

	if err != nil {
		// Create new TargetGroupConfiguration
		tgConfig := &unstructured.Unstructured{}
		tgConfig.SetGroupVersionKind(TargetGroupConfigurationGVK)
		tgConfig.SetName(configName)
		tgConfig.SetNamespace(gatewayNamespace)
		tgConfig.Object["spec"] = spec

		if err := r.Create(ctx, tgConfig); err != nil {
			return fmt.Errorf("failed to create TargetGroupConfiguration %s: %w", configName, err)
		}
		logger.Info("Created TargetGroupConfiguration", "name", configName, "targetType", "ip")
	} else {
		// Update existing if needed
		existingSpec, _ := existing.Object["spec"].(map[string]interface{})
		existingDefault, _ := existingSpec["defaultConfiguration"].(map[string]interface{})
		if existingDefault["targetType"] != "ip" {
			existing.Object["spec"] = spec
			if err := r.Update(ctx, existing); err != nil {
				return fmt.Errorf("failed to update TargetGroupConfiguration %s: %w", configName, err)
			}
			logger.Info("Updated TargetGroupConfiguration", "name", configName, "targetType", "ip")
		}
	}

	return nil
}

// deleteTargetGroupConfiguration removes the TargetGroupConfiguration for a Gateway
func (r *GatewayHostnameRequestReconciler) deleteTargetGroupConfiguration(ctx context.Context, gatewayName, gatewayNamespace string) error {
	logger := log.FromContext(ctx)
	configName := fmt.Sprintf("%s-tgconfig", gatewayName)

	config := &unstructured.Unstructured{}
	config.SetGroupVersionKind(TargetGroupConfigurationGVK)
	config.SetName(configName)
	config.SetNamespace(gatewayNamespace)

	if err := r.Delete(ctx, config); err != nil {
		return nil
	}

	logger.Info("Deleted TargetGroupConfiguration", "name", configName)
	return nil
}
