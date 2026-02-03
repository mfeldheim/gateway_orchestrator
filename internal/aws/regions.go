package aws

import (
	"fmt"
	"strings"
)

// ALBHostedZoneIDs maps AWS regions to their ALB canonical hosted zone IDs
// These are well-known, public values provided by AWS
var ALBHostedZoneIDs = map[string]string{
	"us-east-1":      "Z35SXDOTRQ7X7K",
	"us-east-2":      "Z3AADJGX6KTTL2",
	"us-west-1":      "Z368ELLRRE2KJ0",
	"us-west-2":      "Z1H1FL5HABSF5",
	"ca-central-1":   "ZQSVJUPU6J1EY",
	"eu-central-1":   "Z215JYRZR1TBD5",
	"eu-west-1":      "Z32O12XQLNTSW2",
	"eu-west-2":      "ZHURV8PSTC4K8",
	"eu-west-3":      "Z3Q77PNBQS71R4",
	"eu-north-1":     "Z23TAZ6LKFMNIO",
	"eu-south-1":     "Z3ULH7SSC9OV64",
	"ap-east-1":      "Z3DQVH9N71FHZ0",
	"ap-northeast-1": "Z14GRHDCWA56QT",
	"ap-northeast-2": "ZWKZPGTI48KDX",
	"ap-northeast-3": "Z5LXEXXYW11ES",
	"ap-southeast-1": "Z1LMS91P8CMLE5",
	"ap-southeast-2": "Z1GM3OXH4ZPM65",
	"ap-south-1":     "ZP97RAFLXTNZK",
	"sa-east-1":      "Z2P70J7HTTTPLU",
	"me-south-1":     "ZS929ML54UICD",
	"af-south-1":     "Z268VQBMOI5EKX",
}

// GetALBHostedZoneID returns the canonical hosted zone ID for ALBs in the given region
// This is needed for creating Route53 ALIAS records pointing to ALBs
func GetALBHostedZoneID(region string) (string, error) {
	zoneID, ok := ALBHostedZoneIDs[region]
	if !ok {
		return "", fmt.Errorf("unknown region: %s (ALB hosted zone ID not found)", region)
	}
	return zoneID, nil
}

// ExtractRegionFromALBDNS attempts to extract the AWS region from an ALB DNS name
// ALB DNS names follow the pattern: <name>-<id>.<region>.elb.amazonaws.com
func ExtractRegionFromALBDNS(albDNS string) (string, error) {
	// Example: k8s-edge-gw01-abc123def456.us-east-1.elb.amazonaws.com
	parts := strings.Split(albDNS, ".")
	if len(parts) < 4 {
		return "", fmt.Errorf("invalid ALB DNS name format: %s", albDNS)
	}

	// The region is typically the second-to-last segment before "elb.amazonaws.com"
	// parts: [k8s-edge-gw01-abc123def456, us-east-1, elb, amazonaws, com]
	if len(parts) >= 5 && parts[len(parts)-3] == "elb" {
		return parts[len(parts)-4], nil
	}

	return "", fmt.Errorf("could not extract region from ALB DNS: %s", albDNS)
}
