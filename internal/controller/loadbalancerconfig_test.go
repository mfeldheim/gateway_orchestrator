package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
)

// TestEnsureLoadBalancerConfiguration_IncludesTargetTypeIP verifies that
// ensureLoadBalancerConfiguration creates a LoadBalancerConfiguration with
// targetGroupConfiguration.targetType set to "ip" to enable ClusterIP services.
func TestEnsureLoadBalancerConfiguration_IncludesTargetTypeIP(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: fakeClient,
	}

	ctx := context.Background()
	certificateARNs := []string{
		"arn:aws:acm:eu-west-1:123456789012:certificate/test-cert",
	}

	// Call the controller method
	err := reconciler.ensureLoadBalancerConfiguration(ctx, "gw-01", "edge", certificateARNs, "internet-facing", "")
	if err != nil {
		t.Fatalf("ensureLoadBalancerConfiguration() error = %v", err)
	}

	// Verify the LoadBalancerConfiguration was created
	lbc := &unstructured.Unstructured{}
	lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "gw-01-config", Namespace: "edge"}, lbc)
	if err != nil {
		t.Fatalf("LoadBalancerConfiguration not found: %v", err)
	}

	// Extract spec and verify targetGroupConfiguration
	spec, ok := lbc.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("spec not found or invalid type")
	}

	targetGroupConfig, ok := spec["targetGroupConfiguration"].(map[string]interface{})
	if !ok {
		t.Fatal("targetGroupConfiguration not found in spec")
	}

	targetType, ok := targetGroupConfig["targetType"].(string)
	if !ok {
		t.Fatal("targetType not found or not a string")
	}

	if targetType != "ip" {
		t.Errorf("targetType = %q, want %q", targetType, "ip")
	}
}

// TestEnsureLoadBalancerConfiguration_SortsCertificatesForDeterminism verifies that
// ensureLoadBalancerConfiguration creates a LoadBalancerConfiguration with certificates
// sorted alphabetically in the spec, ensuring the default certificate is deterministic
// regardless of the order GatewayHostnameRequests are reconciled.
func TestEnsureLoadBalancerConfiguration_SortsCertificatesForDeterminism(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: fakeClient,
	}

	ctx := context.Background()

	tests := []struct {
		name                string
		certs               []string
		wantDefaultCert     string
		wantAdditionalCerts []string
	}{
		{
			name:                "reverse_order",
			certs:               []string{"z-final", "a-first", "m-middle"},
			wantDefaultCert:     "a-first",
			wantAdditionalCerts: []string{"m-middle", "z-final"},
		},
		{
			name:                "random_order",
			certs:               []string{"prod", "app", "base"},
			wantDefaultCert:     "app",
			wantAdditionalCerts: []string{"base", "prod"},
		},
		{
			name:                "already_sorted",
			certs:               []string{"cert-01", "cert-02", "cert-03"},
			wantDefaultCert:     "cert-01",
			wantAdditionalCerts: []string{"cert-02", "cert-03"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call the controller method with unsorted certs
			err := reconciler.ensureLoadBalancerConfiguration(ctx, "gw-sort-test", "edge", tt.certs, "internet-facing", "")
			if err != nil {
				t.Fatalf("ensureLoadBalancerConfiguration() error = %v", err)
			}

			// Verify the LoadBalancerConfiguration was created
			lbc := &unstructured.Unstructured{}
			lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
			err = fakeClient.Get(ctx, types.NamespacedName{Name: "gw-sort-test-config", Namespace: "edge"}, lbc)
			if err != nil {
				t.Fatalf("LoadBalancerConfiguration not found: %v", err)
			}

			// Extract listener configurations
			spec, ok := lbc.Object["spec"].(map[string]interface{})
			if !ok {
				t.Fatal("spec not found or invalid type")
			}

			listenerConfigs, ok := spec["listenerConfigurations"].([]interface{})
			if !ok || len(listenerConfigs) == 0 {
				t.Fatal("listenerConfigurations not found or empty")
			}

			// Find HTTPS listener (first should be HTTPS if certs provided)
			httpsListener, ok := listenerConfigs[0].(map[string]interface{})
			if !ok {
				t.Fatal("first listener config is not a map")
			}

			// Verify default certificate is first alphabetically
			defaultCert, ok := httpsListener["defaultCertificate"].(string)
			if !ok {
				t.Fatal("defaultCertificate not found or not a string")
			}

			if defaultCert != tt.wantDefaultCert {
				t.Errorf("defaultCertificate = %q, want %q", defaultCert, tt.wantDefaultCert)
			}

			// Verify additional certs (SNI) are also sorted
			if len(tt.certs) > 1 {
				additionalCerts, ok := httpsListener["certificates"].([]interface{})
				if !ok {
					t.Fatal("certificates field not found or not a slice")
				}

				if len(additionalCerts) != len(tt.wantAdditionalCerts) {
					t.Errorf("additional certs count = %d, want %d", len(additionalCerts), len(tt.wantAdditionalCerts))
				}

				for i, cert := range additionalCerts {
					certStr, ok := cert.(string)
					if !ok {
						t.Fatalf("certificate at index %d is not a string", i)
					}
					if certStr != tt.wantAdditionalCerts[i] {
						t.Errorf("certificate[%d] = %q, want %q", i, certStr, tt.wantAdditionalCerts[i])
					}
				}
			}
		})
	}
}

