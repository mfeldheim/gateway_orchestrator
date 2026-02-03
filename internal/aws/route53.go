package aws

import (
	"context"
)

// Route53Client defines the interface for Route53 operations
type Route53Client interface {
	// CreateOrUpdateRecord creates or updates a DNS record in Route53
	CreateOrUpdateRecord(ctx context.Context, zoneId string, record DNSRecord) error

	// DeleteRecord deletes a DNS record from Route53
	DeleteRecord(ctx context.Context, zoneId string, record DNSRecord) error

	// GetRecord retrieves a DNS record from Route53
	GetRecord(ctx context.Context, zoneId string, name, recordType string) (*DNSRecord, error)
}

// DNSRecord represents a Route53 DNS record
type DNSRecord struct {
	Name string
	Type string // A, AAAA, CNAME, ALIAS, etc.

	// For ALIAS records (pointing to ALB)
	AliasTarget *AliasTarget

	// For CNAME records (ACM validation)
	Value string
	TTL   int64
}

// AliasTarget represents Route53 ALIAS record target
type AliasTarget struct {
	DNSName              string
	HostedZoneID         string // The hosted zone ID of the ALB
	EvaluateTargetHealth bool
}
