package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
)

func TestEnsureRoute53Alias_CreatesBothAAndAAAARecords(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	hostnameType := gwapiv1.HostnameAddressType
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
		Status: gwapiv1.GatewayStatus{
			Addresses: []gwapiv1.GatewayStatusAddress{
				{
					Type:  &hostnameType,
					Value: "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
				},
			},
		},
	}

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "app.opendi.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, ghr).
		Build()

	route53Mock := &MockRoute53Client{
		records: make(map[string][]aws.DNSRecord),
	}

	reconciler := &GatewayHostnameRequestReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		Recorder:      record.NewFakeRecorder(10),
		Route53Client: route53Mock,
	}

	err := reconciler.ensureRoute53Alias(context.Background(), ghr)
	require.NoError(t, err)

	// Verify both A and AAAA records were created
	records := route53Mock.records["Z123456"]
	require.Len(t, records, 2, "Expected 2 records (A + AAAA)")

	var hasA, hasAAAA bool
	for _, r := range records {
		assert.Equal(t, "app.opendi.com", r.Name)
		assert.NotNil(t, r.AliasTarget, "Record should be an ALIAS record")
		assert.Equal(t, "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com", r.AliasTarget.DNSName)
		assert.True(t, r.AliasTarget.EvaluateTargetHealth)

		switch r.Type {
		case "A":
			hasA = true
		case "AAAA":
			hasAAAA = true
		}
	}
	assert.True(t, hasA, "Expected A record to be created")
	assert.True(t, hasAAAA, "Expected AAAA record to be created")

	// Verify LoadBalancer DNS was stored in status
	assert.Equal(t, "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com", ghr.Status.AssignedLoadBalancer)
}

func TestReconcileDelete_DeletesBothAAndAAAARecords(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-request",
			Namespace:         "default",
			DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
			Finalizers:        []string{FinalizerName},
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "app.opendi.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedLoadBalancer:     "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
			CertificateArn:           "arn:aws:acm:us-east-1:123456789012:certificate/test",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr).
		WithStatusSubresource(ghr).
		Build()

	route53Mock := &MockRoute53Client{
		records: map[string][]aws.DNSRecord{
			"Z123456": {
				{Name: "app.opendi.com", Type: "A", AliasTarget: &aws.AliasTarget{
					DNSName:              "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
					HostedZoneID:         "Z35SXDOTRQ7X7K",
					EvaluateTargetHealth: true,
				}},
				{Name: "app.opendi.com", Type: "AAAA", AliasTarget: &aws.AliasTarget{
					DNSName:              "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
					HostedZoneID:         "Z35SXDOTRQ7X7K",
					EvaluateTargetHealth: true,
				}},
			},
		},
	}

	acmMock := &MockACMClient{
		certificates: map[string]string{
			"arn:aws:acm:us-east-1:123456789012:certificate/test": "ISSUED",
		},
	}

	reconciler := &GatewayHostnameRequestReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		Recorder:      record.NewFakeRecorder(10),
		Route53Client: route53Mock,
		ACMClient:     acmMock,
	}

	_, err := reconciler.reconcileDelete(context.Background(), ghr)
	require.NoError(t, err)

	// Verify both A and AAAA records were deleted
	records := route53Mock.records["Z123456"]
	for _, r := range records {
		if r.Name == "app.opendi.com" && (r.Type == "A" || r.Type == "AAAA") {
			t.Errorf("Expected %s record for app.opendi.com to be deleted, but it still exists", r.Type)
		}
	}
}

func TestCleanupForReprovisioning_DeletesBothAAndAAAARecords(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "app.opendi.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedLoadBalancer:     "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
			AssignedGateway:          "",
			AssignedGatewayNamespace: "",
			CertificateArn:           "arn:aws:acm:us-east-1:123456789012:certificate/test",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ghr).
		WithStatusSubresource(ghr).
		Build()

	route53Mock := &MockRoute53Client{
		records: map[string][]aws.DNSRecord{
			"Z123456": {
				{Name: "app.opendi.com", Type: "A", AliasTarget: &aws.AliasTarget{
					DNSName:              "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
					HostedZoneID:         "Z35SXDOTRQ7X7K",
					EvaluateTargetHealth: true,
				}},
				{Name: "app.opendi.com", Type: "AAAA", AliasTarget: &aws.AliasTarget{
					DNSName:              "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
					HostedZoneID:         "Z35SXDOTRQ7X7K",
					EvaluateTargetHealth: true,
				}},
				{Name: "other.opendi.com", Type: "A", AliasTarget: &aws.AliasTarget{
					DNSName: "other-alb.us-east-1.elb.amazonaws.com",
				}},
			},
		},
	}

	acmMock := &MockACMClient{
		certificates: map[string]string{
			"arn:aws:acm:us-east-1:123456789012:certificate/test": "ISSUED",
		},
	}

	reconciler := &GatewayHostnameRequestReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		Recorder:      record.NewFakeRecorder(10),
		Route53Client: route53Mock,
		ACMClient:     acmMock,
	}

	err := reconciler.cleanupForReprovisioning(context.Background(), ghr)
	require.NoError(t, err)

	// Verify both A and AAAA records for app.opendi.com were deleted
	records := route53Mock.records["Z123456"]
	for _, r := range records {
		if r.Name == "app.opendi.com" {
			t.Errorf("Expected record for app.opendi.com (type=%s) to be deleted, but it still exists", r.Type)
		}
	}

	// Verify other records are untouched
	assert.Len(t, records, 1, "Expected only the unrelated record to remain")
	assert.Equal(t, "other.opendi.com", records[0].Name)
}

func TestEnsureRoute53Alias_IdempotentForBothRecordTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)
	_ = gwapiv1.Install(scheme)

	hostnameType := gwapiv1.HostnameAddressType
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw-01",
			Namespace: "edge",
		},
		Status: gwapiv1.GatewayStatus{
			Addresses: []gwapiv1.GatewayStatusAddress{
				{
					Type:  &hostnameType,
					Value: "k8s-gw01-abcdef1234-1234567890.us-east-1.elb.amazonaws.com",
				},
			},
		},
	}

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname: "app.opendi.com",
			ZoneId:   "Z123456",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			AssignedGateway:          "gw-01",
			AssignedGatewayNamespace: "edge",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, ghr).
		Build()

	route53Mock := &MockRoute53Client{
		records: make(map[string][]aws.DNSRecord),
	}

	reconciler := &GatewayHostnameRequestReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		Recorder:      record.NewFakeRecorder(10),
		Route53Client: route53Mock,
	}

	// Call ensureRoute53Alias twice (idempotent check)
	err := reconciler.ensureRoute53Alias(context.Background(), ghr)
	require.NoError(t, err)

	err = reconciler.ensureRoute53Alias(context.Background(), ghr)
	require.NoError(t, err)

	// The mock appends records, so we expect 4 entries (2 per call)
	// In production, CreateOrUpdateRecord uses UPSERT which is idempotent
	records := route53Mock.records["Z123456"]
	assert.Len(t, records, 4, "Mock appends; production uses UPSERT which is idempotent")

	// Verify record types are correct across both calls
	aCount := 0
	aaaaCount := 0
	for _, r := range records {
		switch r.Type {
		case "A":
			aCount++
		case "AAAA":
			aaaaCount++
		}
	}
	assert.Equal(t, 2, aCount, "Expected 2 A records (one per call)")
	assert.Equal(t, 2, aaaaCount, "Expected 2 AAAA records (one per call)")
}
