package aws

import (
	"context"
	"fmt"
)

// MockACMClient is a mock implementation for testing
type MockACMClient struct {
	Certificates      map[string]*CertificateDetails
	ValidationRecords map[string][]ValidationRecord
}

func NewMockACMClient() *MockACMClient {
	return &MockACMClient{
		Certificates:      make(map[string]*CertificateDetails),
		ValidationRecords: make(map[string][]ValidationRecord),
	}
}

func (m *MockACMClient) RequestCertificate(ctx context.Context, domain string, tags map[string]string) (string, error) {
	arn := fmt.Sprintf("arn:aws:acm:us-east-1:123456789012:certificate/%s", domain)
	m.Certificates[arn] = &CertificateDetails{
		Arn:    arn,
		Domain: domain,
		Status: "PENDING_VALIDATION",
	}
	m.ValidationRecords[arn] = []ValidationRecord{
		{
			Name:  fmt.Sprintf("_acm-validation.%s", domain),
			Type:  "CNAME",
			Value: fmt.Sprintf("_validation-value.acm-validations.aws."),
		},
	}
	return arn, nil
}

func (m *MockACMClient) DescribeCertificate(ctx context.Context, certArn string) (*CertificateDetails, error) {
	cert, ok := m.Certificates[certArn]
	if !ok {
		return nil, fmt.Errorf("certificate not found: %s", certArn)
	}
	return cert, nil
}

func (m *MockACMClient) DeleteCertificate(ctx context.Context, certArn string) error {
	delete(m.Certificates, certArn)
	delete(m.ValidationRecords, certArn)
	return nil
}

func (m *MockACMClient) GetValidationRecords(ctx context.Context, certArn string) ([]ValidationRecord, error) {
	records, ok := m.ValidationRecords[certArn]
	if !ok {
		return nil, fmt.Errorf("certificate not found: %s", certArn)
	}
	return records, nil
}

// MockRoute53Client is a mock implementation for testing
type MockRoute53Client struct {
	Records map[string]DNSRecord // key: zoneId:name:type
}

func NewMockRoute53Client() *MockRoute53Client {
	return &MockRoute53Client{
		Records: make(map[string]DNSRecord),
	}
}

func (m *MockRoute53Client) CreateOrUpdateRecord(ctx context.Context, zoneId string, record DNSRecord) error {
	key := fmt.Sprintf("%s:%s:%s", zoneId, record.Name, record.Type)
	m.Records[key] = record
	return nil
}

func (m *MockRoute53Client) DeleteRecord(ctx context.Context, zoneId string, record DNSRecord) error {
	key := fmt.Sprintf("%s:%s:%s", zoneId, record.Name, record.Type)
	delete(m.Records, key)
	return nil
}

func (m *MockRoute53Client) GetRecord(ctx context.Context, zoneId string, name, recordType string) (*DNSRecord, error) {
	key := fmt.Sprintf("%s:%s:%s", zoneId, name, recordType)
	record, ok := m.Records[key]
	if !ok {
		return nil, fmt.Errorf("record not found")
	}
	return &record, nil
}
