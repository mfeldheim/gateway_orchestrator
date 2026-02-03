package aws

import (
	"context"
	"testing"
)

func TestMockACMClient_RequestCertificate(t *testing.T) {
	client := NewMockACMClient()
	ctx := context.Background()

	tests := []struct {
		name     string
		domain   string
		tags     map[string]string
		wantErr  bool
	}{
		{
			name:   "request certificate for domain",
			domain: "test.example.com",
			tags: map[string]string{
				"managed-by": "gateway-orchestrator",
				"environment": "dev",
			},
			wantErr: false,
		},
		{
			name:    "request certificate without tags",
			domain:  "prod.example.com",
			tags:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arn, err := client.RequestCertificate(ctx, tt.domain, tt.tags)
			if (err != nil) != tt.wantErr {
				t.Errorf("RequestCertificate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if arn == "" {
				t.Error("RequestCertificate() returned empty ARN")
			}

			// Verify certificate was stored
			cert, err := client.DescribeCertificate(ctx, arn)
			if err != nil {
				t.Errorf("DescribeCertificate() error = %v", err)
			}
			if cert.Domain != tt.domain {
				t.Errorf("cert domain = %v, want %v", cert.Domain, tt.domain)
			}
			if cert.Status != "PENDING_VALIDATION" {
				t.Errorf("cert status = %v, want PENDING_VALIDATION", cert.Status)
			}
		})
	}
}

func TestMockACMClient_GetValidationRecords(t *testing.T) {
	client := NewMockACMClient()
	ctx := context.Background()

	arn, _ := client.RequestCertificate(ctx, "test.example.com", nil)

	records, err := client.GetValidationRecords(ctx, arn)
	if err != nil {
		t.Fatalf("GetValidationRecords() error = %v", err)
	}

	if len(records) == 0 {
		t.Error("expected validation records, got none")
	}

	record := records[0]
	if record.Type != "CNAME" {
		t.Errorf("record type = %v, want CNAME", record.Type)
	}
	if record.Name == "" {
		t.Error("record name is empty")
	}
	if record.Value == "" {
		t.Error("record value is empty")
	}
}

func TestMockACMClient_DeleteCertificate(t *testing.T) {
	client := NewMockACMClient()
	ctx := context.Background()

	arn, _ := client.RequestCertificate(ctx, "test.example.com", nil)

	// Verify exists
	_, err := client.DescribeCertificate(ctx, arn)
	if err != nil {
		t.Fatalf("certificate should exist before delete")
	}

	// Delete
	err = client.DeleteCertificate(ctx, arn)
	if err != nil {
		t.Fatalf("DeleteCertificate() error = %v", err)
	}

	// Verify deleted
	_, err = client.DescribeCertificate(ctx, arn)
	if err == nil {
		t.Error("certificate should not exist after delete")
	}
}

func TestMockRoute53Client_CreateAndGetRecord(t *testing.T) {
	client := NewMockRoute53Client()
	ctx := context.Background()

	record := DNSRecord{
		Name: "test.example.com",
		Type: "CNAME",
		Value: "_validation.acm.aws.com",
		TTL:   300,
	}

	// Create record
	err := client.CreateOrUpdateRecord(ctx, "Z123456", record)
	if err != nil {
		t.Fatalf("CreateOrUpdateRecord() error = %v", err)
	}

	// Get record
	got, err := client.GetRecord(ctx, "Z123456", "test.example.com", "CNAME")
	if err != nil {
		t.Fatalf("GetRecord() error = %v", err)
	}

	if got.Name != record.Name {
		t.Errorf("name = %v, want %v", got.Name, record.Name)
	}
	if got.Type != record.Type {
		t.Errorf("type = %v, want %v", got.Type, record.Type)
	}
	if got.Value != record.Value {
		t.Errorf("value = %v, want %v", got.Value, record.Value)
	}
}

func TestMockRoute53Client_ALIASRecord(t *testing.T) {
	client := NewMockRoute53Client()
	ctx := context.Background()

	record := DNSRecord{
		Name: "app.example.com",
		Type: "A",
		AliasTarget: &AliasTarget{
			DNSName:              "k8s-gw.us-east-1.elb.amazonaws.com",
			HostedZoneID:         "Z35SXDOTRQ7X7K",
			EvaluateTargetHealth: true,
		},
	}

	err := client.CreateOrUpdateRecord(ctx, "Z789012", record)
	if err != nil {
		t.Fatalf("CreateOrUpdateRecord() error = %v", err)
	}

	got, err := client.GetRecord(ctx, "Z789012", "app.example.com", "A")
	if err != nil {
		t.Fatalf("GetRecord() error = %v", err)
	}

	if got.AliasTarget == nil {
		t.Fatal("alias target is nil")
	}
	if got.AliasTarget.DNSName != record.AliasTarget.DNSName {
		t.Errorf("DNS name = %v, want %v", got.AliasTarget.DNSName, record.AliasTarget.DNSName)
	}
}

func TestMockRoute53Client_DeleteRecord(t *testing.T) {
	client := NewMockRoute53Client()
	ctx := context.Background()

	record := DNSRecord{
		Name:  "test.example.com",
		Type:  "CNAME",
		Value: "validation.aws.com",
		TTL:   300,
	}

	// Create
	client.CreateOrUpdateRecord(ctx, "Z123", record)

	// Verify exists
	_, err := client.GetRecord(ctx, "Z123", "test.example.com", "CNAME")
	if err != nil {
		t.Fatal("record should exist before delete")
	}

	// Delete
	err = client.DeleteRecord(ctx, "Z123", record)
	if err != nil {
		t.Fatalf("DeleteRecord() error = %v", err)
	}

	// Verify deleted
	_, err = client.GetRecord(ctx, "Z123", "test.example.com", "CNAME")
	if err == nil {
		t.Error("record should not exist after delete")
	}
}

func TestMockRoute53Client_UpdateRecord(t *testing.T) {
	client := NewMockRoute53Client()
	ctx := context.Background()

	original := DNSRecord{
		Name:  "test.example.com",
		Type:  "CNAME",
		Value: "original.aws.com",
		TTL:   300,
	}

	client.CreateOrUpdateRecord(ctx, "Z123", original)

	// Update with new value
	updated := DNSRecord{
		Name:  "test.example.com",
		Type:  "CNAME",
		Value: "updated.aws.com",
		TTL:   600,
	}

	err := client.CreateOrUpdateRecord(ctx, "Z123", updated)
	if err != nil {
		t.Fatalf("CreateOrUpdateRecord() error = %v", err)
	}

	// Verify updated
	got, _ := client.GetRecord(ctx, "Z123", "test.example.com", "CNAME")
	if got.Value != "updated.aws.com" {
		t.Errorf("value = %v, want updated.aws.com", got.Value)
	}
	if got.TTL != 600 {
		t.Errorf("TTL = %v, want 600", got.TTL)
	}
}
