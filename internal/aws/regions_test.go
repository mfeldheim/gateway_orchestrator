package aws

import (
	"testing"
)

func TestGetALBHostedZoneID(t *testing.T) {
	tests := []struct {
		name      string
		region    string
		want      string
		wantError bool
	}{
		{
			name:      "us-east-1",
			region:    "us-east-1",
			want:      "Z35SXDOTRQ7X7K",
			wantError: false,
		},
		{
			name:      "us-west-2",
			region:    "us-west-2",
			want:      "Z1H1FL5HABSF5",
			wantError: false,
		},
		{
			name:      "eu-west-1",
			region:    "eu-west-1",
			want:      "Z32O12XQLNTSW2",
			wantError: false,
		},
		{
			name:      "ap-southeast-1",
			region:    "ap-southeast-1",
			want:      "Z1LMS91P8CMLE5",
			wantError: false,
		},
		{
			name:      "unknown region",
			region:    "mars-1",
			want:      "",
			wantError: true,
		},
		{
			name:      "empty region",
			region:    "",
			want:      "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetALBHostedZoneID(tt.region)
			if (err != nil) != tt.wantError {
				t.Errorf("GetALBHostedZoneID() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if got != tt.want {
				t.Errorf("GetALBHostedZoneID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractRegionFromALBDNS(t *testing.T) {
	tests := []struct {
		name      string
		albDNS    string
		want      string
		wantError bool
	}{
		{
			name:      "standard ALB DNS",
			albDNS:    "k8s-edge-gw01-abc123def456.us-east-1.elb.amazonaws.com",
			want:      "us-east-1",
			wantError: false,
		},
		{
			name:      "us-west-2 ALB",
			albDNS:    "k8s-production-gw-xyz789.us-west-2.elb.amazonaws.com",
			want:      "us-west-2",
			wantError: false,
		},
		{
			name:      "eu-central-1 ALB",
			albDNS:    "my-alb-123.eu-central-1.elb.amazonaws.com",
			want:      "eu-central-1",
			wantError: false,
		},
		{
			name:      "ap-southeast-1 ALB",
			albDNS:    "gateway-456.ap-southeast-1.elb.amazonaws.com",
			want:      "ap-southeast-1",
			wantError: false,
		},
		{
			name:      "invalid DNS - too short",
			albDNS:    "short.dns",
			want:      "",
			wantError: true,
		},
		{
			name:      "invalid DNS - not ALB format",
			albDNS:    "www.example.com",
			want:      "",
			wantError: true,
		},
		{
			name:      "empty DNS",
			albDNS:    "",
			want:      "",
			wantError: true,
		},
		{
			name:      "DNS without region",
			albDNS:    "alb.elb.amazonaws.com",
			want:      "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractRegionFromALBDNS(tt.albDNS)
			if (err != nil) != tt.wantError {
				t.Errorf("ExtractRegionFromALBDNS() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractRegionFromALBDNS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractAndGetHostedZone(t *testing.T) {
	// Integration test: extract region and get hosted zone
	tests := []struct {
		name          string
		albDNS        string
		wantZoneID    string
		wantError     bool
	}{
		{
			name:       "complete flow us-east-1",
			albDNS:     "k8s-gw-abc.us-east-1.elb.amazonaws.com",
			wantZoneID: "Z35SXDOTRQ7X7K",
			wantError:  false,
		},
		{
			name:       "complete flow eu-west-1",
			albDNS:     "alb.eu-west-1.elb.amazonaws.com",
			wantZoneID: "Z32O12XQLNTSW2",
			wantError:  false,
		},
		{
			name:       "invalid DNS",
			albDNS:     "invalid",
			wantZoneID: "",
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, err := ExtractRegionFromALBDNS(tt.albDNS)
			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			zoneID, err := GetALBHostedZoneID(region)
			if err != nil {
				t.Errorf("unexpected error getting zone ID: %v", err)
				return
			}

			if zoneID != tt.wantZoneID {
				t.Errorf("got zone ID %v, want %v", zoneID, tt.wantZoneID)
			}
		})
	}
}
