package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
)

// LoadBalancerConfiguration GVK
var loadBalancerConfigGVK = schema.GroupVersionKind{
	Group:   "gateway.k8s.aws",
	Version: "v1beta1",
	Kind:    "LoadBalancerConfiguration",
}

// ensureLoadBalancerConfiguration creates or updates the LoadBalancerConfiguration for a Gateway
// with all certificate ARNs from GatewayHostnameRequests assigned to that Gateway
func (r *GatewayHostnameRequestReconciler) ensureLoadBalancerConfiguration(
	ctx context.Context,
	gatewayName string,
	gatewayNamespace string,
	certificateARNs []string,
	visibility string,
) error {
	logger := log.FromContext(ctx)

	configName := fmt.Sprintf("%s-config", gatewayName)

	// Build the LoadBalancerConfiguration
	lbConfig := &unstructured.Unstructured{}
	lbConfig.SetGroupVersionKind(loadBalancerConfigGVK)
	lbConfig.SetName(configName)
	lbConfig.SetNamespace(gatewayNamespace)

	// Try to get existing config
	existingConfig := &unstructured.Unstructured{}
	existingConfig.SetGroupVersionKind(loadBalancerConfigGVK)
	err := r.Get(ctx, types.NamespacedName{Name: configName, Namespace: gatewayNamespace}, existingConfig)

	// Build listener configuration with certificates
	listenerConfigs := []interface{}{}
	
	if len(certificateARNs) > 0 {
		// HTTPS listener with certificates
		httpsListener := map[string]interface{}{
			"protocolPort":       "HTTPS:443",
			"defaultCertificate": certificateARNs[0], // First cert is default
		}
		if len(certificateARNs) > 1 {
			// Additional certs for SNI
			httpsListener["certificates"] = certificateARNs[1:]
		}
		listenerConfigs = append(listenerConfigs, httpsListener)
	}

	// HTTP listener (no certs needed)
	httpListener := map[string]interface{}{
		"protocolPort": "HTTP:80",
	}
	listenerConfigs = append(listenerConfigs, httpListener)

	// Build spec
	spec := map[string]interface{}{
		"scheme":                 visibility,
		"listenerConfigurations": listenerConfigs,
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

// deleteLoadBalancerConfiguration removes the LoadBalancerConfiguration for a Gateway
func (r *GatewayHostnameRequestReconciler) deleteLoadBalancerConfiguration(ctx context.Context, gatewayName, gatewayNamespace string) error {
	logger := log.FromContext(ctx)
	configName := fmt.Sprintf("%s-config", gatewayName)

	config := &unstructured.Unstructured{}
	config.SetGroupVersionKind(loadBalancerConfigGVK)
	config.SetName(configName)
	config.SetNamespace(gatewayNamespace)

	if err := r.Delete(ctx, config); err != nil {
		// Ignore not found
		return nil
	}

	logger.Info("Deleted LoadBalancerConfiguration", "name", configName)
	return nil
}
