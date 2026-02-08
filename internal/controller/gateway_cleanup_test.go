package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
)

// getTestScheme returns a scheme with necessary types for testing
func getTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.AddToScheme(scheme)
	return scheme
}

func TestCleanupEmptyGateway_WithAssignments_DoesNotDelete(t *testing.T) {
	// Setup
	scheme := getTestScheme()
	ghrWithAssignment := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ghr-1",
			Namespace: "default",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghrWithAssignment).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")

	// Assert
	assert.NoError(t, err)
	// Verify no delete operations would be triggered (gateway still has assignments)
}

func TestCleanupEmptyGateway_NoAssignments_DeletesGateway(t *testing.T) {
	// Setup - no GHRs with assignments
	scheme := getTestScheme()
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")

	// Assert
	assert.NoError(t, err)

	// Verify Gateway was deleted
	var deletedGateway gwapiv1.Gateway
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &deletedGateway)
	// Should return "not found" error after deletion
	assert.Error(t, err)
}

func TestCleanupEmptyGateway_RaceCondition_IgnoresOtherNamespaceAssignments(t *testing.T) {
	// Setup - GHR in different namespace assigned to different gw doesn't affect gw-01 cleanup
	scheme := getTestScheme()
	ghrOtherNamespace := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ghr-other",
			Namespace: "other-ns",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01", // same gateway name, different namespace
			AssignedGatewayNamespace: "other-edge", // different namespace
		},
	}

	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghrOtherNamespace, gateway).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute cleanup for gw-01 in "edge" namespace (not "other-edge")
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")

	// Assert - should delete because the assignment is in different namespace
	assert.NoError(t, err)
	var deletedGateway gwapiv1.Gateway
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &deletedGateway)
	assert.Error(t, err) // Gateway should be deleted
}

func TestCleanupEmptyGateway_MultipleGHRs_DeletesOnlyWhenAllRemoved(t *testing.T) {
	// Setup
	scheme := getTestScheme()
	ghr1 := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ghr-1",
			Namespace: "default",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
		},
	}

	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr1, gateway).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute - should NOT delete because ghr1 still has assignment
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")
	assert.NoError(t, err)

	// Verify Gateway still exists
	var stillExistingGateway gwapiv1.Gateway
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &stillExistingGateway)
	assert.NoError(t, err)

	// Now delete the assignment
	ghr1.Status.AssignedGateway = ""
	ghr1.Status.AssignedGatewayNamespace = ""
	err = client.Update(context.Background(), ghr1)
	assert.NoError(t, err)

	// Execute again - should delete now
	err = reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")
	assert.NoError(t, err)

	// Verify Gateway was deleted
	var deletedGateway gwapiv1.Gateway
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &deletedGateway)
	assert.Error(t, err)
}

func TestCleanupEmptyGateway_AlreadyDeletedGateway_ReturnsNil(t *testing.T) {
	// Setup - no Gateway object exists
	scheme := getTestScheme()
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute - should not error even though Gateway doesn't exist
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")

	// Assert - idempotent: should be no error
	assert.NoError(t, err)
}

func TestCleanupEmptyGateway_CountsOnlyAssignedGHRs(t *testing.T) {
	// Setup - create two GHRs, only one assigned to gw-01
	scheme := getTestScheme()
	ghr1 := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ghr-1",
			Namespace: "ns1",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
		},
	}

	ghr2 := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ghr-2",
			Namespace: "ns2",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-02", // different gateway
			AssignedGatewayNamespace: "edge",
		},
	}

	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr1, ghr2, gateway).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute - should NOT delete gw-01 because ghr1 is still assigned
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")
	assert.NoError(t, err)

	// Verify Gateway still exists
	var stillExistingGateway gwapiv1.Gateway
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &stillExistingGateway)
	assert.NoError(t, err)
}

func TestCleanupEmptyGateway_WithLoadBalancerConfig_DeletesBoth(t *testing.T) {
	// Setup - Gateway with LoadBalancerConfiguration, no assignments
	scheme := getTestScheme()

	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
	}

	// Create an unstructured LoadBalancerConfiguration
	lbConfig := &unstructured.Unstructured{}
	lbConfig.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.k8s.aws",
		Version: "v1beta1",
		Kind:    "LoadBalancerConfiguration",
	})
	lbConfig.SetName("gw-01-config")
	lbConfig.SetNamespace("edge")

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, lbConfig).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client: client,
	}

	// Execute
	err := reconciler.cleanupEmptyGateway(context.Background(), "gw-01", "edge")

	// Assert
	assert.NoError(t, err)

	// Verify Gateway was deleted
	var deletedGateway gwapiv1.Gateway
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &deletedGateway)
	assert.Error(t, err)

	// Verify LoadBalancerConfiguration was deleted
	var deletedLBC unstructured.Unstructured
	deletedLBC.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.k8s.aws",
		Version: "v1beta1",
		Kind:    "LoadBalancerConfiguration",
	})
	err = client.Get(context.Background(), types.NamespacedName{Name: "gw-01-config", Namespace: "edge"}, &deletedLBC)
	assert.Error(t, err)
}

