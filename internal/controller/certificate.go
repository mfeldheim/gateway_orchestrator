package controller

import (
	"context"
	"fmt"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
)

// requestCertificate requests a new ACM certificate for the hostname
func (r *GatewayHostnameRequestReconciler) requestCertificate(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) (string, error) {
	tags := map[string]string{
		"managed-by":  "gateway-orchestrator",
		"hostname":    ghr.Spec.Hostname,
		"namespace":   ghr.Namespace,
		"environment": ghr.Spec.Environment,
	}

	certArn, err := r.ACMClient.RequestCertificate(ctx, ghr.Spec.Hostname, tags)
	if err != nil {
		return "", fmt.Errorf("failed to request certificate: %w", err)
	}

	return certArn, nil
}

// ensureValidationRecords creates DNS validation records in Route53
func (r *GatewayHostnameRequestReconciler) ensureValidationRecords(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)
	if ghr.Status.CertificateArn == "" {
		return fmt.Errorf("certificate ARN not set")
	}

	// Get validation records from ACM
	validationRecords, err := r.ACMClient.GetValidationRecords(ctx, ghr.Status.CertificateArn)
	if err != nil {
		return fmt.Errorf("failed to get validation records: %w", err)
	}

	logger.Info("Retrieved validation records from ACM", "count", len(validationRecords), "certificateArn", ghr.Status.CertificateArn)

	// Create each validation record in Route53
	for _, valRec := range validationRecords {
		record := aws.DNSRecord{
			Name:  valRec.Name,
			Type:  valRec.Type,
			Value: valRec.Value,
			TTL:   300,
		}

		logger.Info("Creating validation record in Route53", "name", record.Name, "type", record.Type, "value", record.Value, "zoneId", ghr.Spec.ZoneId)

		if err := r.Route53Client.CreateOrUpdateRecord(ctx, ghr.Spec.ZoneId, record); err != nil {
			logger.Error(err, "Failed to create validation record", "name", record.Name, "zoneId", ghr.Spec.ZoneId)
			return fmt.Errorf("failed to create validation record: %w", err)
		}
	}

	logger.Info("All validation records created successfully", "count", len(validationRecords))
	return nil
}

// checkCertificateStatus checks if the ACM certificate has been issued
func (r *GatewayHostnameRequestReconciler) checkCertificateStatus(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) (bool, error) {
	if ghr.Status.CertificateArn == "" {
		return false, fmt.Errorf("certificate ARN not set")
	}

	certDetails, err := r.ACMClient.DescribeCertificate(ctx, ghr.Status.CertificateArn)
	if err != nil {
		return false, fmt.Errorf("failed to describe certificate: %w", err)
	}

	switch certDetails.Status {
	case "ISSUED":
		return true, nil
	case "PENDING_VALIDATION":
		return false, nil
	case "FAILED", "VALIDATION_TIMED_OUT", "REVOKED":
		return false, fmt.Errorf("certificate in failed state: %s", certDetails.Status)
	default:
		return false, nil
	}
}
