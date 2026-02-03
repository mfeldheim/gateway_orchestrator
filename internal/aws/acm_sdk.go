package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acm/types"
)

// SDKACMClient implements ACMClient using AWS SDK v2
type SDKACMClient struct {
	client *acm.Client
}

// NewSDKACMClient creates a new ACM client using the provided AWS config
func NewSDKACMClient(cfg aws.Config) *SDKACMClient {
	return &SDKACMClient{
		client: acm.NewFromConfig(cfg),
	}
}

func (c *SDKACMClient) RequestCertificate(ctx context.Context, hostname string, tags map[string]string) (string, error) {
	// Convert tags to ACM format
	var acmTags []types.Tag
	for k, v := range tags {
		acmTags = append(acmTags, types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	input := &acm.RequestCertificateInput{
		DomainName:       aws.String(hostname),
		ValidationMethod: types.ValidationMethodDns,
		Tags:             acmTags,
	}

	result, err := c.client.RequestCertificate(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to request certificate: %w", err)
	}

	return *result.CertificateArn, nil
}

func (c *SDKACMClient) DescribeCertificate(ctx context.Context, arn string) (*CertificateDetails, error) {
	input := &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	}

	result, err := c.client.DescribeCertificate(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe certificate: %w", err)
	}

	return &CertificateDetails{
		Arn:    arn,
		Domain: aws.ToString(result.Certificate.DomainName),
		Status: string(result.Certificate.Status),
	}, nil
}

func (c *SDKACMClient) DeleteCertificate(ctx context.Context, arn string) error {
	input := &acm.DeleteCertificateInput{
		CertificateArn: aws.String(arn),
	}

	_, err := c.client.DeleteCertificate(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete certificate: %w", err)
	}

	return nil
}

func (c *SDKACMClient) GetValidationRecords(ctx context.Context, arn string) ([]ValidationRecord, error) {
	input := &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	}

	result, err := c.client.DescribeCertificate(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe certificate: %w", err)
	}

	var records []ValidationRecord
	for _, dvo := range result.Certificate.DomainValidationOptions {
		if dvo.ResourceRecord != nil {
			records = append(records, ValidationRecord{
				Name:  aws.ToString(dvo.ResourceRecord.Name),
				Type:  string(dvo.ResourceRecord.Type),
				Value: aws.ToString(dvo.ResourceRecord.Value),
			})
		}
	}

	return records, nil
}
