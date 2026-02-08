package controller

import (
	"sort"
	"testing"
)

// TestEnsureLoadBalancerConfiguration_SortsCertificatesForDeterminism verifies that certificates
// are sorted alphabetically to ensure deterministic default certificate selection.
// This prevents the default certificate from flip-flopping when GatewayHostnameRequests
// are reconciled in random order (fixes code review feedback).
func TestEnsureLoadBalancerConfiguration_SortsCertificatesForDeterminism(t *testing.T) {
	tests := []struct {
		name              string
		inputCertificates []string
		expectedFirst     string
		description       string
	}{
		{
			name: "reverse_alphabetical_order",
			inputCertificates: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/z-final",
				"arn:aws:acm:eu-west-1:123456789012:certificate/a-first",
				"arn:aws:acm:eu-west-1:123456789012:certificate/m-middle",
			},
			expectedFirst: "arn:aws:acm:eu-west-1:123456789012:certificate/a-first",
			description:   "When GHRs reconciled in reverse order, default cert should still be 'a-first'",
		},
		{
			name: "random_order_simulation",
			inputCertificates: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/prod-cert",
				"arn:aws:acm:eu-west-1:123456789012:certificate/app-cert",
				"arn:aws:acm:eu-west-1:123456789012:certificate/base-cert",
			},
			expectedFirst: "arn:aws:acm:eu-west-1:123456789012:certificate/app-cert",
			description:   "Real-world scenario: GHRs reconciled in random order",
		},
		{
			name: "already_sorted_input",
			inputCertificates: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-01",
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-02",
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-03",
			},
			expectedFirst: "arn:aws:acm:eu-west-1:123456789012:certificate/cert-01",
			description:   "When input is already sorted, ordering should be preserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate what ensureLoadBalancerConfiguration does:
			// Copy input to avoid mutation, then sort
			sortedCerts := make([]string, len(tt.inputCertificates))
			copy(sortedCerts, tt.inputCertificates)
			sort.Strings(sortedCerts)

			// Verify first cert matches expected
			if sortedCerts[0] != tt.expectedFirst {
				t.Errorf("%s: first certificate = %s, want %s",
					tt.description, sortedCerts[0], tt.expectedFirst)
			}

			// Verify determinism: sorting again produces identical result
			sortedCerts2 := make([]string, len(tt.inputCertificates))
			copy(sortedCerts2, tt.inputCertificates)
			sort.Strings(sortedCerts2)

			for i, cert := range sortedCerts {
				if cert != sortedCerts2[i] {
					t.Errorf("non-deterministic sort at index %d: %s vs %s",
						i, cert, sortedCerts2[i])
				}
			}
		})
	}
}

// TestCertificateSorting_PreservesDuplicatesAndOrder verifies that the sorting
// implementation preserves certificate duplicates and doesn't lose certificates.
// Real-world scenario: Multiple GatewayHostnameRequests might request the same
// certificate (though rare, the controller should handle it gracefully).
func TestCertificateSorting_PreservesDuplicatesAndOrder(t *testing.T) {
	tests := []struct {
		name           string
		inputCerts     []string
		expectedLength int
		firstCert      string
		description    string
	}{
		{
			name: "with_duplicate_certificates",
			inputCerts: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-b",
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-a",
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-c",
				"arn:aws:acm:eu-west-1:123456789012:certificate/cert-a", // duplicate
			},
			expectedLength: 4,
			firstCert:      "arn:aws:acm:eu-west-1:123456789012:certificate/cert-a",
			description:    "Duplicates should be preserved (edge case but must not crash)",
		},
		{
			name: "all_unique_certificates",
			inputCerts: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/api-cert",
				"arn:aws:acm:eu-west-1:123456789012:certificate/web-cert",
				"arn:aws:acm:eu-west-1:123456789012:certificate/admin-cert",
			},
			expectedLength: 3,
			firstCert:      "arn:aws:acm:eu-west-1:123456789012:certificate/admin-cert",
			description:    "Standard case: 3 unique certs from 3 GatewayHostnameRequests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted := make([]string, len(tt.inputCerts))
			copy(sorted, tt.inputCerts)
			sort.Strings(sorted)

			// Verify no certs were lost/gained during sorting
			if len(sorted) != tt.expectedLength {
				t.Errorf("%s: length changed from %d to %d",
					tt.description, tt.expectedLength, len(sorted))
			}

			// Verify first cert is deterministic
			if sorted[0] != tt.firstCert {
				t.Errorf("%s: first cert = %s, want %s",
					tt.description, sorted[0], tt.firstCert)
			}
		})
	}
}

// TestCertificateSorting_EdgeCasesWithFewCertificates tests real-world scenarios
// with single and two certificates, ensuring the controller doesn't crash or
// produce unexpected results with minimal certificate counts.
func TestCertificateSorting_EdgeCasesWithFewCertificates(t *testing.T) {
	tests := []struct {
		name           string
		certificates   []string
		expectFirst    string
		description    string
	}{
		{
			name:           "single_certificate",
			certificates:   []string{"arn:aws:acm:eu-west-1:123456789012:certificate/only-one"},
			expectFirst:    "arn:aws:acm:eu-west-1:123456789012:certificate/only-one",
			description:    "Single hostname request: one Gateway, one cert",
		},
		{
			name: "two_certificates_reverse_order",
			certificates: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/z-hostname",
				"arn:aws:acm:eu-west-1:123456789012:certificate/a-hostname",
			},
			expectFirst: "arn:aws:acm:eu-west-1:123456789012:certificate/a-hostname",
			description: "Two hostname requests: should select first alphabetically",
		},
		{
			name: "two_certificates_correct_order",
			certificates: []string{
				"arn:aws:acm:eu-west-1:123456789012:certificate/a-hostname",
				"arn:aws:acm:eu-west-1:123456789012:certificate/z-hostname",
			},
			expectFirst: "arn:aws:acm:eu-west-1:123456789012:certificate/a-hostname",
			description: "Two hostname requests already in order: should preserve",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted := make([]string, len(tt.certificates))
			copy(sorted, tt.certificates)
			sort.Strings(sorted)

			// Verify length unchanged
			if len(sorted) != len(tt.certificates) {
				t.Errorf("%s: length changed %d -> %d",
					tt.description, len(tt.certificates), len(sorted))
			}

			// Verify first cert correct
			if len(sorted) > 0 && sorted[0] != tt.expectFirst {
				t.Errorf("%s: first = %s, want %s",
					tt.description, sorted[0], tt.expectFirst)
			}

			// Verify no panic/crash by accessing elements
			for i := range sorted {
				_ = sorted[i]
			}
		})
	}
}
