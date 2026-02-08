package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
)

func TestReconciler_requestCertificate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	acmClient := aws.NewMockACMClient()

	r := &GatewayHostnameRequestReconciler{
		ACMClient: acmClient,
	}

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			Hostname:    "test.example.com",
			Environment: "dev",
		},
	}

	ctx := context.Background()
	arn, err := r.requestCertificate(ctx, ghr)
	if err != nil {
		t.Fatalf("requestCertificate() error = %v", err)
	}

	if arn == "" {
		t.Error("expected certificate ARN, got empty string")
	}

	// Verify certificate was created in ACM
	cert, err := acmClient.DescribeCertificate(ctx, arn)
	if err != nil {
		t.Fatalf("certificate should exist: %v", err)
	}

	if cert.Domain != ghr.Spec.Hostname {
		t.Errorf("certificate domain = %v, want %v", cert.Domain, ghr.Spec.Hostname)
	}

	if cert.Status != "PENDING_VALIDATION" {
		t.Errorf("certificate status = %v, want PENDING_VALIDATION", cert.Status)
	}
}

func TestReconciler_ensureValidationRecords(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	acmClient := aws.NewMockACMClient()
	route53Client := aws.NewMockRoute53Client()

	r := &GatewayHostnameRequestReconciler{
		ACMClient:     acmClient,
		Route53Client: route53Client,
	}

	ctx := context.Background()

	// Request a certificate first
	arn, _ := acmClient.RequestCertificate(ctx, "test.example.com", nil)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			ZoneId:   "Z123456",
			Hostname: "test.example.com",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			CertificateArn: arn,
		},
	}

	err := r.ensureValidationRecords(ctx, ghr)
	if err != nil {
		t.Fatalf("ensureValidationRecords() error = %v", err)
	}

	// Verify validation records were created
	validationRecords, _ := acmClient.GetValidationRecords(ctx, arn)
	if len(validationRecords) == 0 {
		t.Fatal("no validation records found")
	}

	// Verify records exist in Route53
	record, err := route53Client.GetRecord(ctx, "Z123456", validationRecords[0].Name, "CNAME")
	if err != nil {
		t.Errorf("validation record should exist in Route53: %v", err)
	}

	if record.Value != validationRecords[0].Value {
		t.Errorf("record value = %v, want %v", record.Value, validationRecords[0].Value)
	}
}

func TestReconciler_ensureValidationRecords_PendingACMRecords(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	acmClient := aws.NewMockACMClient()
	route53Client := aws.NewMockRoute53Client()

	r := &GatewayHostnameRequestReconciler{
		ACMClient:     acmClient,
		Route53Client: route53Client,
	}

	ctx := context.Background()

	// Request a certificate and then simulate ACM returning no validation records yet
	arn, _ := acmClient.RequestCertificate(ctx, "test.example.com", nil)
	acmClient.ValidationRecords[arn] = []aws.ValidationRecord{}

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			ZoneId:   "Z123456",
			Hostname: "test.example.com",
		},
		Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
			CertificateArn: arn,
		},
	}

	err := r.ensureValidationRecords(ctx, ghr)
	if err == nil {
		t.Fatal("expected ErrValidationRecordsNotReady, got nil")
	}
	if !errors.Is(err, ErrValidationRecordsNotReady) {
		t.Fatalf("expected ErrValidationRecordsNotReady, got %v", err)
	}

	if len(route53Client.Records) != 0 {
		t.Fatalf("expected no Route53 records to be created, got %d", len(route53Client.Records))
	}
}

func TestReconciler_checkCertificateStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	acmClient := aws.NewMockACMClient()

	r := &GatewayHostnameRequestReconciler{
		ACMClient: acmClient,
	}

	ctx := context.Background()

	tests := []struct {
		name       string
		certStatus string
		wantIssued bool
		wantErr    bool
	}{
		{
			name:       "pending validation",
			certStatus: "PENDING_VALIDATION",
			wantIssued: false,
			wantErr:    false,
		},
		{
			name:       "issued",
			certStatus: "ISSUED",
			wantIssued: true,
			wantErr:    false,
		},
		{
			name:       "failed",
			certStatus: "FAILED",
			wantIssued: false,
			wantErr:    true,
		},
		{
			name:       "validation timed out",
			certStatus: "VALIDATION_TIMED_OUT",
			wantIssued: false,
			wantErr:    true,
		},
		{
			name:       "revoked",
			certStatus: "REVOKED",
			wantIssued: false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create certificate with specific status
			arn, _ := acmClient.RequestCertificate(ctx, "test.example.com", nil)
			acmClient.Certificates[arn].Status = tt.certStatus

			ghr := &gatewayv1alpha1.GatewayHostnameRequest{
				Status: gatewayv1alpha1.GatewayHostnameRequestStatus{
					CertificateArn: arn,
				},
			}

			issued, err := r.checkCertificateStatus(ctx, ghr)

			if (err != nil) != tt.wantErr {
				t.Errorf("checkCertificateStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if issued != tt.wantIssued {
				t.Errorf("checkCertificateStatus() issued = %v, want %v", issued, tt.wantIssued)
			}
		})
	}
}

func TestReconciler_validateRequest(t *testing.T) {
	r := &GatewayHostnameRequestReconciler{}

	tests := []struct {
		name    string
		ghr     *gatewayv1alpha1.GatewayHostnameRequest
		wantErr bool
	}{
		{
			name: "valid request",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
					ZoneId:   "Z123456",
					Hostname: "test.example.com",
				},
			},
			wantErr: false,
		},
		{
			name: "missing zoneId",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
					Hostname: "test.example.com",
				},
			},
			wantErr: true,
		},
		{
			name: "missing hostname",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
					ZoneId: "Z123456",
				},
			},
			wantErr: true,
		},
		{
			name: "both missing",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.validateRequest(tt.ghr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
