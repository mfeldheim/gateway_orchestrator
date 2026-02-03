package aws

import (
	"context"
)

// ACMClient defines the interface for ACM operations
type ACMClient interface {
	// RequestCertificate requests a new ACM certificate for the given domain
	RequestCertificate(ctx context.Context, domain string, tags map[string]string) (certArn string, err error)

	// DescribeCertificate gets the current status and details of a certificate
	DescribeCertificate(ctx context.Context, certArn string) (*CertificateDetails, error)

	// DeleteCertificate deletes an ACM certificate
	DeleteCertificate(ctx context.Context, certArn string) error

	// GetValidationRecords returns the DNS records needed for certificate validation
	GetValidationRecords(ctx context.Context, certArn string) ([]ValidationRecord, error)
}

// CertificateDetails represents ACM certificate information
type CertificateDetails struct {
	Arn    string
	Domain string
	Status string // PENDING_VALIDATION, ISSUED, FAILED, etc.
}

// ValidationRecord represents a DNS validation record for ACM
type ValidationRecord struct {
	Name  string
	Type  string // CNAME
	Value string
}
