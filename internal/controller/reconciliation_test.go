package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
)

// MockACMClient for testing
type MockACMClient struct {
	certificates map[string]string // ARN -> status
}

func (m *MockACMClient) RequestCertificate(ctx context.Context, hostname string, tags map[string]string) (string, error) {
	arn := "arn:aws:acm:us-east-1:123456789012:certificate/test-cert-" + hostname
	m.certificates[arn] = "PENDING_VALIDATION"
	return arn, nil
}

func (m *MockACMClient) DescribeCertificate(ctx context.Context, arn string) (*aws.CertificateDetails, error) {
	status, ok := m.certificates[arn]
	if !ok {
		return nil, errors.NewNotFound(corev1.Resource("certificate"), arn)
	}
	return &aws.CertificateDetails{
		Arn:    arn,
		Status: status,
	}, nil
}

func (m *MockACMClient) GetValidationRecords(ctx context.Context, arn string) ([]aws.ValidationRecord, error) {
	return []aws.ValidationRecord{
		{Name: "_test.example.com", Type: "CNAME", Value: "_validation.acm.aws"},
	}, nil
}

func (m *MockACMClient) DeleteCertificate(ctx context.Context, arn string) error {
	delete(m.certificates, arn)
	return nil
}

// MockRoute53Client for testing
type MockRoute53Client struct {
	records map[string][]aws.DNSRecord // zoneId -> records
}

func (m *MockRoute53Client) CreateOrUpdateRecord(ctx context.Context, zoneId string, record aws.DNSRecord) error {
	if m.records == nil {
		m.records = make(map[string][]aws.DNSRecord)
	}
	m.records[zoneId] = append(m.records[zoneId], record)
	return nil
}

func (m *MockRoute53Client) DeleteRecord(ctx context.Context, zoneId string, record aws.DNSRecord) error {
	if records, ok := m.records[zoneId]; ok {
		filtered := []aws.DNSRecord{}
		for _, r := range records {
			if r.Name != record.Name || r.Type != record.Type {
				filtered = append(filtered, r)
			}
		}
		m.records[zoneId] = filtered
	}
	return nil
}

func (m *MockRoute53Client) GetRecord(ctx context.Context, zoneId string, name, recordType string) (*aws.DNSRecord, error) {
	if records, ok := m.records[zoneId]; ok {
		for _, r := range records {
			if r.Name == name && r.Type == recordType {
				return &r, nil
			}
		}
	}
	return nil, nil
}

func TestValidateAssignedResources_GatewayDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "test.example.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
			CertificateArn:           "arn:aws:acm:us-east-1:123456789012:certificate/test",
			Conditions: []metav1.Condition{
				{
					Type:   ConditionTypeListenerAttached,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   ConditionTypeDnsAliasReady,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   ConditionTypeReady,
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	// Create fake client WITHOUT the Gateway (simulating deletion)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr).
		WithStatusSubresource(ghr).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		ACMClient: &MockACMClient{
			certificates: map[string]string{
				"arn:aws:acm:us-east-1:123456789012:certificate/test": "ISSUED",
			},
		},
	}

	// Run validation
	err := reconciler.validateAssignedResources(context.Background(), ghr)
	if err != nil {
		t.Fatalf("validateAssignedResources() returned error: %v", err)
	}

	// Verify conditions were cleared
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeListenerAttached) {
		t.Error("Expected ListenerAttached condition to be removed")
	}
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeDnsAliasReady) {
		t.Error("Expected DnsAliasReady condition to be removed")
	}
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeReady) {
		t.Error("Expected Ready condition to be removed")
	}

	// Verify status fields were cleared
	if ghr.Status.AssignedGateway != "" {
		t.Errorf("Expected AssignedGateway to be cleared, got %s", ghr.Status.AssignedGateway)
	}
	if ghr.Status.AssignedGatewayNamespace != "" {
		t.Errorf("Expected AssignedGatewayNamespace to be cleared, got %s", ghr.Status.AssignedGatewayNamespace)
	}
}

func TestValidateAssignedResources_LoadBalancerConfigurationDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "test.example.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
			CertificateArn:           "arn:aws:acm:us-east-1:123456789012:certificate/test",
			Conditions: []metav1.Condition{
				{
					Type:   ConditionTypeListenerAttached,
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	// Create Gateway but NOT LoadBalancerConfiguration
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr, gateway).
		WithStatusSubresource(ghr).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		ACMClient: &MockACMClient{
			certificates: map[string]string{
				"arn:aws:acm:us-east-1:123456789012:certificate/test": "ISSUED",
			},
		},
	}

	// Run validation
	err := reconciler.validateAssignedResources(context.Background(), ghr)
	if err != nil {
		t.Fatalf("validateAssignedResources() returned error: %v", err)
	}

	// Verify conditions were cleared
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeListenerAttached) {
		t.Error("Expected ListenerAttached condition to be removed")
	}
}

func TestValidateAssignedResources_CertificateDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "test.example.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			CertificateArn: "arn:aws:acm:us-east-1:123456789012:certificate/deleted",
			Conditions: []metav1.Condition{
				{
					Type:   ConditionTypeCertificateIssued,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   ConditionTypeListenerAttached,
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr).
		WithStatusSubresource(ghr).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		ACMClient: &MockACMClient{
			certificates: map[string]string{}, // Certificate not found
		},
	}

	// Run validation
	err := reconciler.validateAssignedResources(context.Background(), ghr)
	if err != nil {
		t.Fatalf("validateAssignedResources() returned error: %v", err)
	}

	// Verify all certificate-related conditions were cleared
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeCertificateIssued) {
		t.Error("Expected CertificateIssued condition to be removed")
	}
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeListenerAttached) {
		t.Error("Expected ListenerAttached condition to be removed")
	}

	// Verify certificate ARN was cleared
	if ghr.Status.CertificateArn != "" {
		t.Errorf("Expected CertificateArn to be cleared, got %s", ghr.Status.CertificateArn)
	}
}

func TestValidateAssignedResources_CertificateFailed(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "test.example.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			CertificateArn: "arn:aws:acm:us-east-1:123456789012:certificate/failed",
			Conditions: []metav1.Condition{
				{
					Type:   ConditionTypeCertificateIssued,
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr).
		WithStatusSubresource(ghr).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		ACMClient: &MockACMClient{
			certificates: map[string]string{
				"arn:aws:acm:us-east-1:123456789012:certificate/failed": "FAILED",
			},
		},
	}

	// Run validation
	err := reconciler.validateAssignedResources(context.Background(), ghr)
	if err != nil {
		t.Fatalf("validateAssignedResources() returned error: %v", err)
	}

	// Verify conditions were cleared
	if meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeCertificateIssued) {
		t.Error("Expected CertificateIssued condition to be removed for failed certificate")
	}

	// Verify certificate ARN was cleared
	if ghr.Status.CertificateArn != "" {
		t.Errorf("Expected CertificateArn to be cleared, got %s", ghr.Status.CertificateArn)
	}
}

func TestEnsureGatewayConfiguration_AnnotationDrift(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname:   "test.example.com",
			ZoneId:     "Z123456",
			Visibility: "internet-facing",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
			CertificateArn:           "arn:aws:acm:us-east-1:123456789012:certificate/test",
		},
	}

	// Create Gateway with MISSING loadbalancer-configuration annotation
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
			Annotations: map[string]string{
				"gateway.opendi.com/visibility": "internal", // Wrong value
				// Missing: gateway.k8s.aws/loadbalancer-configuration
			},
		},
	}

	// Create LoadBalancerConfiguration
	lbc := &unstructured.Unstructured{}
	lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	lbc.SetName("gw-01-config")
	lbc.SetNamespace("edge")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr, gateway, lbc).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	// Run configuration sync
	err := reconciler.ensureGatewayConfiguration(context.Background(), ghr)
	if err != nil {
		t.Fatalf("ensureGatewayConfiguration() returned error: %v", err)
	}

	// Verify Gateway was updated with correct annotations
	var updatedGw gwapiv1.Gateway
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &updatedGw)
	if err != nil {
		t.Fatalf("Failed to get updated Gateway: %v", err)
	}

	// Check loadbalancer-configuration annotation was added
	if updatedGw.Annotations["gateway.k8s.aws/loadbalancer-configuration"] != "gw-01-config" {
		t.Errorf("Expected loadbalancer-configuration annotation to be 'gw-01-config', got %s",
			updatedGw.Annotations["gateway.k8s.aws/loadbalancer-configuration"])
	}

	// Check visibility annotation was corrected
	if updatedGw.Annotations["gateway.opendi.com/visibility"] != "internet-facing" {
		t.Errorf("Expected visibility annotation to be 'internet-facing', got %s",
			updatedGw.Annotations["gateway.opendi.com/visibility"])
	}
}

func TestEnsureGatewayConfiguration_NoUpdateNeeded(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname:   "test.example.com",
			ZoneId:     "Z123456",
			Visibility: "internet-facing",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
			CertificateArn:           "arn:aws:acm:us-east-1:123456789012:certificate/test",
		},
	}

	// Create Gateway with CORRECT annotations already
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
			Annotations: map[string]string{
				"gateway.k8s.aws/loadbalancer-configuration": "gw-01-config",
				"gateway.opendi.com/visibility":              "internet-facing",
			},
		},
	}

	// Create LoadBalancerConfiguration
	lbc := &unstructured.Unstructured{}
	lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
	lbc.SetName("gw-01-config")
	lbc.SetNamespace("edge")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr, gateway, lbc).
		Build()

	reconciler := &GatewayHostnameRequestReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	// Run configuration sync
	err := reconciler.ensureGatewayConfiguration(context.Background(), ghr)
	if err != nil {
		t.Fatalf("ensureGatewayConfiguration() returned error: %v", err)
	}

	// Verify Gateway was NOT modified (idempotent)
	var updatedGw gwapiv1.Gateway
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "gw-01", Namespace: "edge"}, &updatedGw)
	if err != nil {
		t.Fatalf("Failed to get Gateway: %v", err)
	}

	// Annotations should remain unchanged
	if updatedGw.Annotations["gateway.k8s.aws/loadbalancer-configuration"] != "gw-01-config" {
		t.Error("loadbalancer-configuration annotation was incorrectly modified")
	}
	if updatedGw.Annotations["gateway.opendi.com/visibility"] != "internet-facing" {
		t.Error("visibility annotation was incorrectly modified")
	}
}
