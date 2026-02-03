package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// SDKRoute53Client implements Route53Client using AWS SDK v2
type SDKRoute53Client struct {
	client *route53.Client
}

// NewSDKRoute53Client creates a new Route53 client using the provided AWS config
func NewSDKRoute53Client(cfg aws.Config) *SDKRoute53Client {
	return &SDKRoute53Client{
		client: route53.NewFromConfig(cfg),
	}
}

func (c *SDKRoute53Client) CreateOrUpdateRecord(ctx context.Context, zoneId string, record DNSRecord) error {
	var resourceRecords []types.ResourceRecord
	var aliasTarget *types.AliasTarget

	// Determine if this is an ALIAS record or standard record
	if record.AliasTarget != nil {
		aliasTarget = &types.AliasTarget{
			DNSName:              aws.String(record.AliasTarget.DNSName),
			HostedZoneId:         aws.String(record.AliasTarget.HostedZoneID),
			EvaluateTargetHealth: record.AliasTarget.EvaluateTargetHealth,
		}
	} else {
		resourceRecords = []types.ResourceRecord{
			{Value: aws.String(record.Value)},
		}
	}

	changeBatch := &types.ChangeBatch{
		Changes: []types.Change{
			{
				Action: types.ChangeActionUpsert,
				ResourceRecordSet: &types.ResourceRecordSet{
					Name:            aws.String(record.Name),
					Type:            types.RRType(record.Type),
					TTL:             aws.Int64(record.TTL),
					ResourceRecords: resourceRecords,
					AliasTarget:     aliasTarget,
				},
			},
		},
	}

	// For ALIAS records, TTL should not be set
	if aliasTarget != nil {
		changeBatch.Changes[0].ResourceRecordSet.TTL = nil
		changeBatch.Changes[0].ResourceRecordSet.ResourceRecords = nil
	}

	input := &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(normalizeZoneId(zoneId)),
		ChangeBatch:  changeBatch,
	}

	_, err := c.client.ChangeResourceRecordSets(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to create/update record: %w", err)
	}

	return nil
}

func (c *SDKRoute53Client) DeleteRecord(ctx context.Context, zoneId string, record DNSRecord) error {
	var resourceRecords []types.ResourceRecord
	var aliasTarget *types.AliasTarget

	if record.AliasTarget != nil {
		aliasTarget = &types.AliasTarget{
			DNSName:              aws.String(record.AliasTarget.DNSName),
			HostedZoneId:         aws.String(record.AliasTarget.HostedZoneID),
			EvaluateTargetHealth: record.AliasTarget.EvaluateTargetHealth,
		}
	} else {
		resourceRecords = []types.ResourceRecord{
			{Value: aws.String(record.Value)},
		}
	}

	changeBatch := &types.ChangeBatch{
		Changes: []types.Change{
			{
				Action: types.ChangeActionDelete,
				ResourceRecordSet: &types.ResourceRecordSet{
					Name:            aws.String(record.Name),
					Type:            types.RRType(record.Type),
					TTL:             aws.Int64(record.TTL),
					ResourceRecords: resourceRecords,
					AliasTarget:     aliasTarget,
				},
			},
		},
	}

	// For ALIAS records, TTL should not be set
	if aliasTarget != nil {
		changeBatch.Changes[0].ResourceRecordSet.TTL = nil
		changeBatch.Changes[0].ResourceRecordSet.ResourceRecords = nil
	}

	input := &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(normalizeZoneId(zoneId)),
		ChangeBatch:  changeBatch,
	}

	_, err := c.client.ChangeResourceRecordSets(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete record: %w", err)
	}

	return nil
}

func (c *SDKRoute53Client) GetRecord(ctx context.Context, zoneId, name, recordType string) (*DNSRecord, error) {
	input := &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(normalizeZoneId(zoneId)),
		StartRecordName: aws.String(name),
		StartRecordType: types.RRType(recordType),
		MaxItems:        aws.Int32(1),
	}

	result, err := c.client.ListResourceRecordSets(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to list records: %w", err)
	}

	for _, rrs := range result.ResourceRecordSets {
		// Check if name matches (Route53 returns names with trailing dot)
		recordName := aws.ToString(rrs.Name)
		if strings.TrimSuffix(recordName, ".") == strings.TrimSuffix(name, ".") &&
			string(rrs.Type) == recordType {

			record := &DNSRecord{
				Name: recordName,
				Type: string(rrs.Type),
				TTL:  aws.ToInt64(rrs.TTL),
			}

			if rrs.AliasTarget != nil {
				record.AliasTarget = &AliasTarget{
					DNSName:              aws.ToString(rrs.AliasTarget.DNSName),
					HostedZoneID:         aws.ToString(rrs.AliasTarget.HostedZoneId),
					EvaluateTargetHealth: rrs.AliasTarget.EvaluateTargetHealth,
				}
			} else if len(rrs.ResourceRecords) > 0 {
				record.Value = aws.ToString(rrs.ResourceRecords[0].Value)
			}

			return record, nil
		}
	}

	return nil, nil // Not found
}

// normalizeZoneId ensures the zone ID has the correct format
func normalizeZoneId(zoneId string) string {
	// Remove /hostedzone/ prefix if present
	zoneId = strings.TrimPrefix(zoneId, "/hostedzone/")
	return zoneId
}
