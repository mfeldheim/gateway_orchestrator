package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/gateway"
)

// TestEnsureLoadBalancerConfiguration_DoesNotIncludeTargetGroupConfiguration verifies that
// ensureLoadBalancerConfiguration does NOT put targetGroupConfiguration in the LBC spec
// (it's a separate CRD: TargetGroupConfiguration).
func TestEnsureLoadBalancerConfiguration_DoesNotIncludeTargetGroupConfiguration(t *testing.T) {
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

	// Verify targetGroupConfiguration is NOT in the LBC spec
	spec, ok := lbc.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("spec not found or invalid type")
	}

	if _, exists := spec["targetGroupConfiguration"]; exists {
		t.Error("targetGroupConfiguration should NOT be in LoadBalancerConfiguration spec (it's a separate CRD)")
	}
}

// TestEnsureTargetGroupConfiguration_CreatesWithTargetTypeIP verifies that
// ensureTargetGroupConfiguration creates a separate TargetGroupConfiguration with
// defaultConfiguration.targetType set to "ip" to enable ClusterIP services.
func TestEnsureTargetGroupConfiguration_CreatesWithTargetTypeIP(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: fakeClient,
	}

	ctx := context.Background()

	err := reconciler.ensureTargetGroupConfiguration(ctx, "gw-01", "edge")
	if err != nil {
		t.Fatalf("ensureTargetGroupConfiguration() error = %v", err)
	}

	// Verify the TargetGroupConfiguration was created
	tgc := &unstructured.Unstructured{}
	tgc.SetGroupVersionKind(TargetGroupConfigurationGVK)
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "gw-01-tgconfig", Namespace: "edge"}, tgc)
	if err != nil {
		t.Fatalf("TargetGroupConfiguration not found: %v", err)
	}

	spec, ok := tgc.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("spec not found or invalid type")
	}

	defaultConfig, ok := spec["defaultConfiguration"].(map[string]interface{})
	if !ok {
		t.Fatal("defaultConfiguration not found in spec")
	}

	targetType, ok := defaultConfig["targetType"].(string)
	if !ok {
		t.Fatal("targetType not found or not a string")
	}

	if targetType != "ip" {
		t.Errorf("targetType = %q, want %q", targetType, "ip")
	}

	// Verify targetReference points to the Gateway
	targetRef, ok := spec["targetReference"].(map[string]interface{})
	if !ok {
		t.Fatal("targetReference not found in spec")
	}
	if targetRef["name"] != "gw-01" {
		t.Errorf("targetReference.name = %q, want %q", targetRef["name"], "gw-01")
	}
	if targetRef["group"] != "gateway.networking.k8s.io" {
		t.Errorf("targetReference.group = %q, want %q", targetRef["group"], "gateway.networking.k8s.io")
	}
	if targetRef["kind"] != "Gateway" {
		t.Errorf("targetReference.kind = %q, want %q", targetRef["kind"], "Gateway")
	}
}

// TestEnsureTargetGroupConfiguration_IdempotentWhenAlreadyCorrect verifies that
// calling ensureTargetGroupConfiguration twice doesn't cause errors.
func TestEnsureTargetGroupConfiguration_IdempotentWhenAlreadyCorrect(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: fakeClient,
	}

	ctx := context.Background()

	// First call creates
	if err := reconciler.ensureTargetGroupConfiguration(ctx, "gw-01", "edge"); err != nil {
		t.Fatalf("first call error = %v", err)
	}

	// Second call should be a no-op (already correct)
	if err := reconciler.ensureTargetGroupConfiguration(ctx, "gw-01", "edge"); err != nil {
		t.Fatalf("second call error = %v", err)
	}
}

// TestEnsureLoadBalancerConfiguration_CustomPorts verifies that the LBC uses
// configurable ports from the GatewayPool when set, and defaults to 80/443 otherwise.
func TestEnsureLoadBalancerConfiguration_CustomPorts(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	pool := gateway.NewPool(fakeClient, "edge", "aws-alb", 8080, 8443)

	reconciler := &GatewayHostnameRequestReconciler{
		Client:      fakeClient,
		GatewayPool: pool,
	}

	ctx := context.Background()
	certs := []string{"arn:aws:acm:eu-west-1:123456789012:certificate/test-cert"}

	err := reconciler.ensureLoadBalancerConfiguration(ctx, "gw-01", "edge", certs, "internet-facing", "")
	if err != nil {
		t.Fatalf("ensureLoadBalancerConfiguration() error = %v", err)
	}

	// Verify the LoadBalancerConfiguration was created with custom ports
	lbc := &unstructured.Unstructured{}
	lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "gw-01-config", Namespace: "edge"}, lbc)
	if err != nil {
		t.Fatalf("LoadBalancerConfiguration not found: %v", err)
	}

	spec, ok := lbc.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("spec not found or invalid type")
	}

	listenerConfigs, ok := spec["listenerConfigurations"].([]interface{})
	if !ok || len(listenerConfigs) == 0 {
		t.Fatal("listenerConfigurations not found or empty")
	}

	// Check HTTPS listener uses custom port
	httpsListener, ok := listenerConfigs[0].(map[string]interface{})
	if !ok {
		t.Fatal("first listener config is not a map")
	}
	if httpsListener["protocolPort"] != "HTTPS:8443" {
		t.Errorf("HTTPS protocolPort = %v, want HTTPS:8443", httpsListener["protocolPort"])
	}

	// Check HTTP listener uses custom port
	httpListener, ok := listenerConfigs[1].(map[string]interface{})
	if !ok {
		t.Fatal("second listener config is not a map")
	}
	if httpListener["protocolPort"] != "HTTP:8080" {
		t.Errorf("HTTP protocolPort = %v, want HTTP:8080", httpListener["protocolPort"])
	}
}

// TestEnsureLoadBalancerConfiguration_DefaultPorts verifies that when no GatewayPool
// is set (e.g., in tests), the LBC defaults to standard ports 80 and 443.
func TestEnsureLoadBalancerConfiguration_DefaultPorts(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Reconciler without GatewayPool - should use defaults
	reconciler := &GatewayHostnameRequestReconciler{
		Client: fakeClient,
	}

	ctx := context.Background()
	certs := []string{"arn:aws:acm:eu-west-1:123456789012:certificate/test-cert"}

	err := reconciler.ensureLoadBalancerConfiguration(ctx, "gw-01", "edge", certs, "internet-facing", "")
	if err != nil {
		t.Fatalf("ensureLoadBalancerConfiguration() error = %v", err)
	}

	lbc := &unstructured.Unstructured{}
	lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "gw-01-config", Namespace: "edge"}, lbc)
	if err != nil {
		t.Fatalf("LoadBalancerConfiguration not found: %v", err)
	}

	spec := lbc.Object["spec"].(map[string]interface{})
	listenerConfigs := spec["listenerConfigurations"].([]interface{})

	httpsListener := listenerConfigs[0].(map[string]interface{})
	if httpsListener["protocolPort"] != "HTTPS:443" {
		t.Errorf("HTTPS protocolPort = %v, want HTTPS:443", httpsListener["protocolPort"])
	}

	httpListener := listenerConfigs[1].(map[string]interface{})
	if httpListener["protocolPort"] != "HTTP:80" {
		t.Errorf("HTTP protocolPort = %v, want HTTP:80", httpListener["protocolPort"])
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

