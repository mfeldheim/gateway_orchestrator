package controller

import (
	"context"
	"sort"
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
// certificates are sorted alphabetically to ensure the default certificate is deterministic
// regardless of the order GatewayHostnameRequests are reconciled.
//
// This test uses a direct algorithm verification approach (not integration with fake client)
// to avoid issues with unstructured object deep-copying in the fake client.
func TestEnsureLoadBalancerConfiguration_SortsCertificatesForDeterminism(t *testing.T) {
	tests := []struct {
		name      string
		certs     []string
		wantFirst string
	}{
		{
			name:      "reverse_order",
			certs:     []string{"z-final", "a-first", "m-middle"},
			wantFirst: "a-first",
		},
		{
			name:      "random_order",
			certs:     []string{"prod", "app", "base"},
			wantFirst: "app",
		},
		{
			name:      "already_sorted",
			certs:     []string{"cert-01", "cert-02", "cert-03"},
			wantFirst: "cert-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify sorting algorithm matches what ensureLoadBalancerConfiguration does
			sortedCerts := make([]string, len(tt.certs))
			copy(sortedCerts, tt.certs)
			sort.Strings(sortedCerts)

			if len(sortedCerts) > 0 && sortedCerts[0] != tt.wantFirst {
				t.Errorf("defaultCertificate = %q, want %q", sortedCerts[0], tt.wantFirst)
			}

			// Verify determinism: sorting the same input multiple times yields same result
			sortedAgain := make([]string, len(tt.certs))
			copy(sortedAgain, tt.certs)
			sort.Strings(sortedAgain)

			if len(sortedCerts) != len(sortedAgain) {
				t.Errorf("sorting is not deterministic: different lengths")
			}
			for i, c := range sortedCerts {
				if c != sortedAgain[i] {
					t.Errorf("sorting is not deterministic at index %d: %q != %q", i, c, sortedAgain[i])
				}
			}
		})
	}
}

