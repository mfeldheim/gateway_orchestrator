package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	goerrors "errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
	"github.com/michelfeldheim/gateway-orchestrator/internal/aws"
	"github.com/michelfeldheim/gateway-orchestrator/internal/gateway"
)

const (
	FinalizerName = "gateway-orchestrator.opendi.com/finalizer"
)

// Condition types
const (
	ConditionTypeClaimed              = "Claimed"
	ConditionTypeCertificateRequested = "CertificateRequested"
	ConditionTypeDnsValidated         = "DnsValidated"
	ConditionTypeCertificateIssued    = "CertificateIssued"
	ConditionTypeListenerAttached     = "ListenerAttached"
	ConditionTypeDnsAliasReady        = "DnsAliasReady"
	ConditionTypeReady                = "Ready"
	ConditionTypeDeleting             = "Deleting"
)

// GatewayHostnameRequestReconciler reconciles a GatewayHostnameRequest object
type GatewayHostnameRequestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	ACMClient     aws.ACMClient
	Route53Client aws.Route53Client
	GatewayPool   *gateway.Pool
}

//+kubebuilder:rbac:groups=gateway.opendi.com,resources=gatewayhostnamerequests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.opendi.com,resources=gatewayhostnamerequests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gateway.opendi.com,resources=gatewayhostnamerequests/finalizers,verbs=update
//+kubebuilder:rbac:groups=gateway.opendi.com,resources=domainclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;update;patch

// Reconcile implements the reconciliation loop
func (r *GatewayHostnameRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the GatewayHostnameRequest
	var ghr gatewayv1alpha1.GatewayHostnameRequest
	if err := r.Get(ctx, req.NamespacedName, &ghr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !ghr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &ghr)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&ghr, FinalizerName) {
		controllerutil.AddFinalizer(&ghr, FinalizerName)
		if err := r.Update(ctx, &ghr); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Reconciling GatewayHostnameRequest", "hostname", ghr.Spec.Hostname, "zoneId", ghr.Spec.ZoneId)

	// Reconciliation state machine
	result, err := r.reconcileNormal(ctx, &ghr)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		return result, err
	}

	return result, nil
}

// reconcileNormal handles the normal reconciliation flow
func (r *GatewayHostnameRequestReconciler) reconcileNormal(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Detect spec drift - if spec changed, cleanup and re-provision
	currentHash := computeSpecHash(&ghr.Spec)
	if ghr.Status.ObservedSpecHash != "" && ghr.Status.ObservedSpecHash != currentHash {
		logger.Info("Spec changed, triggering re-provisioning",
			"oldHash", ghr.Status.ObservedSpecHash,
			"newHash", currentHash,
			"hostname", ghr.Spec.Hostname)
		r.Recorder.Event(ghr, corev1.EventTypeNormal, "SpecChanged", "Spec changed, cleaning up for re-provisioning")

		// Clean up old resources
		if err := r.cleanupForReprovisioning(ctx, ghr); err != nil {
			logger.Error(err, "Failed to cleanup during reprovisioning")
			// Continue anyway - best effort cleanup
		}

		// Clear status fields to trigger full re-reconciliation
		ghr.Status.CertificateArn = ""
		ghr.Status.AssignedGateway = ""
		ghr.Status.AssignedGatewayNamespace = ""
		ghr.Status.AssignedLoadBalancer = ""
		ghr.Status.Conditions = nil
		ghr.Status.ObservedSpecHash = ""
		ghr.Status.ObservedGeneration = 0

		if err := r.Status().Update(ctx, ghr); err != nil {
			return ctrl.Result{}, err
		}

		// Requeue to start fresh reconciliation
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate assigned resources still exist (drift detection)
	if err := r.validateAssignedResources(ctx, ghr); err != nil {
		logger.Error(err, "Resource validation failed")
		// Set condition so user knows validation had issues, but continue reconciliation
		r.setCondition(ghr, "ResourceValidationError", metav1.ConditionTrue, "ValidationFailed",
			fmt.Sprintf("Validation error (will auto-correct): %v", err))
		if err := r.Status().Update(ctx, ghr); err != nil {
			logger.Error(err, "Failed to update validation error condition")
		}
		// Continue with reconciliation anyway - resources will be recreated if needed
	}

	// Step 1: Validate request
	if err := r.validateRequest(ghr); err != nil {
		r.setCondition(ghr, ConditionTypeReady, metav1.ConditionFalse, "ValidationFailed", err.Error())
		_ = r.Status().Update(ctx, ghr)
		r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "ValidationFailed", "Request validation failed: %v", err)
		return ctrl.Result{}, err
	}

	// Step 2: Claim domain (first-come-first-serve)
	claimed, err := r.ensureDomainClaim(ctx, ghr)
	if err != nil {
		r.setCondition(ghr, ConditionTypeClaimed, metav1.ConditionFalse, "ClaimFailed", err.Error())
		_ = r.Status().Update(ctx, ghr)
		r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "ClaimFailed", "Failed to claim domain: %v", err)
		return ctrl.Result{}, err
	}
	if !claimed {
		r.setCondition(ghr, ConditionTypeClaimed, metav1.ConditionFalse, "AlreadyClaimed", "Hostname already claimed by another request")
		_ = r.Status().Update(ctx, ghr)
		r.Recorder.Event(ghr, corev1.EventTypeWarning, "AlreadyClaimed", "Hostname already claimed by another request")
		return ctrl.Result{}, nil // Don't requeue, claim conflict
	}
	r.setCondition(ghr, ConditionTypeClaimed, metav1.ConditionTrue, "Claimed", "Domain successfully claimed")
	r.Recorder.Event(ghr, corev1.EventTypeNormal, "Claimed", "Domain successfully claimed")

	// Step 3: Request ACM certificate
	if ghr.Status.CertificateArn == "" {
		certArn, err := r.requestCertificate(ctx, ghr)
		if err != nil {
			r.setCondition(ghr, ConditionTypeCertificateRequested, metav1.ConditionFalse, "RequestFailed", err.Error())
			_ = r.Status().Update(ctx, ghr)
			return ctrl.Result{}, err
		}
		ghr.Status.CertificateArn = certArn
		r.setCondition(ghr, ConditionTypeCertificateRequested, metav1.ConditionTrue, "Requested", "Certificate requested from ACM")
		if err := r.Status().Update(ctx, ghr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 4: Ensure DNS validation records
	if !meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeDnsValidated) {
		if err := r.ensureValidationRecords(ctx, ghr); err != nil {
			if goerrors.Is(err, ErrValidationRecordsNotReady) {
				r.setCondition(ghr, ConditionTypeDnsValidated, metav1.ConditionFalse, "PendingValidationRecords", "Waiting for ACM to provide DNS validation records")
				_ = r.Status().Update(ctx, ghr)
				return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
			}
			r.setCondition(ghr, ConditionTypeDnsValidated, metav1.ConditionFalse, "ValidationRecordFailed", err.Error())
			_ = r.Status().Update(ctx, ghr)
			return ctrl.Result{}, err
		}
		r.setCondition(ghr, ConditionTypeDnsValidated, metav1.ConditionTrue, "RecordsCreated", "DNS validation records created")
		if err := r.Status().Update(ctx, ghr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 5: Wait for certificate issuance
	if !meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeCertificateIssued) {
		issued, err := r.checkCertificateStatus(ctx, ghr)
		if err != nil {
			r.setCondition(ghr, ConditionTypeCertificateIssued, metav1.ConditionFalse, "CheckFailed", err.Error())
			_ = r.Status().Update(ctx, ghr)
			r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "CertificateCheckFailed", "Failed to check certificate status: %v", err)
			return ctrl.Result{}, err
		}
		if !issued {
			logger.Info("Certificate not yet issued, requeuing", "hostname", ghr.Spec.Hostname)
			r.setCondition(ghr, ConditionTypeCertificateIssued, metav1.ConditionFalse, "PendingIssuance", "Waiting for ACM to issue certificate")
			_ = r.Status().Update(ctx, ghr)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.setCondition(ghr, ConditionTypeCertificateIssued, metav1.ConditionTrue, "Issued", "Certificate issued by ACM")
		r.Recorder.Event(ghr, corev1.EventTypeNormal, "CertificateIssued", "ACM certificate issued")
		if err := r.Status().Update(ctx, ghr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 6: Assign to Gateway and attach certificate
	if !meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeListenerAttached) {
		if err := r.ensureGatewayAssignment(ctx, ghr); err != nil {
			r.setCondition(ghr, ConditionTypeListenerAttached, metav1.ConditionFalse, "AttachmentFailed", err.Error())
			_ = r.Status().Update(ctx, ghr)
			r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "GatewayAssignmentFailed", "Failed to assign gateway: %v", err)
			return ctrl.Result{}, err
		}
		r.setCondition(ghr, ConditionTypeListenerAttached, metav1.ConditionTrue, "Attached", "Certificate attached to Gateway")
		r.Recorder.Eventf(ghr, corev1.EventTypeNormal, "GatewayAssigned", "Assigned to gateway %s", ghr.Status.AssignedGateway)
		if err := r.Status().Update(ctx, ghr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 7: Create Route53 ALIAS record
	if !meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeDnsAliasReady) {
		if err := r.ensureRoute53Alias(ctx, ghr); err != nil {
			// If LoadBalancer not ready yet, requeue
			if err.Error() == "gateway "+ghr.Status.AssignedGateway+" does not have LoadBalancer address yet" {
				logger.Info("Waiting for LoadBalancer to be provisioned", "gateway", ghr.Status.AssignedGateway)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			r.setCondition(ghr, ConditionTypeDnsAliasReady, metav1.ConditionFalse, "AliasFailed", err.Error())
			_ = r.Status().Update(ctx, ghr)
			return ctrl.Result{}, err
		}
		r.setCondition(ghr, ConditionTypeDnsAliasReady, metav1.ConditionTrue, "Created", "Route53 ALIAS record created")
		if err := r.Status().Update(ctx, ghr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 8: Label namespace for gateway access and configure allowedRoutes
	// These run every reconciliation to ensure configuration stays correct (idempotent)
	if err := r.ensureNamespaceLabel(ctx, ghr); err != nil {
		logger.Info("Failed to label namespace for gateway access", "error", err.Error())
		// Don't fail reconciliation for this, just log it
	}
	if err := r.ensureAllowedRoutes(ctx, ghr); err != nil {
		logger.Info("Failed to configure allowedRoutes, continuing anyway", "error", err.Error())
		// Don't fail reconciliation for this, just log it
	}

	// Continuously sync Gateway configuration (idempotent drift correction)
	if ghr.Status.AssignedGateway != "" {
		if err := r.ensureGatewayConfiguration(ctx, ghr); err != nil {
			logger.Info("Failed to sync Gateway configuration", "error", err.Error())
			// Don't fail reconciliation, will retry on next reconcile
		}
	}

	// Step 9: Mark as Ready and update observed generation/hash
	ghr.Status.ObservedGeneration = ghr.Generation
	ghr.Status.ObservedSpecHash = computeSpecHash(&ghr.Spec)
	r.setCondition(ghr, ConditionTypeReady, metav1.ConditionTrue, "Ready", "Hostname request fully provisioned")
	r.Recorder.Event(ghr, corev1.EventTypeNormal, "Ready", "Hostname fully provisioned")
	if err := r.Status().Update(ctx, ghr); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled GatewayHostnameRequest", "hostname", ghr.Spec.Hostname)
	return ctrl.Result{}, nil
}

// reconcileDelete handles cleanup when GatewayHostnameRequest is deleted
func (r *GatewayHostnameRequestReconciler) reconcileDelete(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Deleting GatewayHostnameRequest", "hostname", ghr.Spec.Hostname)

	if !controllerutil.ContainsFinalizer(ghr, FinalizerName) {
		return ctrl.Result{}, nil
	}

	// Set deleting condition
	r.setCondition(ghr, ConditionTypeDeleting, metav1.ConditionTrue, "DeletionInProgress", "Cleanup in progress")
	if err := r.Status().Update(ctx, ghr); err != nil {
		logger.Error(err, "Failed to update deleting condition")
		// Continue anyway
	}

	// Step 1: Remove Route53 alias record (independent of cert, can happen anytime)
	if ghr.Status.AssignedLoadBalancer != "" {
		aliasRecord := aws.DNSRecord{
			Name: ghr.Spec.Hostname,
			Type: "A",
			AliasTarget: &aws.AliasTarget{
				DNSName:              ghr.Status.AssignedLoadBalancer,
				HostedZoneID:         r.getALBHostedZoneId(ghr.Status.AssignedLoadBalancer),
				EvaluateTargetHealth: true,
			},
		}
		awsCtx, cancel := withAWSTimeout(ctx)
		err := r.Route53Client.DeleteRecord(awsCtx, ghr.Spec.ZoneId, aliasRecord)
		cancel()
		if err != nil {
			logger.Error(err, "Failed to delete Route53 alias record",
				"hostname", ghr.Spec.Hostname,
				"zoneId", ghr.Spec.ZoneId)
		} else {
			logger.Info("Deleted Route53 alias record", "hostname", ghr.Spec.Hostname)
		}
	}

	// Step 2: Remove certificate ARN from Gateway annotation (triggers AWS LBC to update ALB)
	if ghr.Status.AssignedGateway != "" && ghr.Status.CertificateArn != "" {
		if err := r.removeCertificateFromGateway(ctx, ghr); err != nil {
			logger.Error(err, "Failed to remove certificate from gateway",
				"gateway", ghr.Status.AssignedGateway,
				"hostname", ghr.Spec.Hostname)
		} else {
			logger.Info("Removed certificate from gateway", "gateway", ghr.Status.AssignedGateway)
		}
	}

	// Step 3: Remove namespace label for gateway access
	if err := r.removeNamespaceLabel(ctx, ghr); err != nil {
		logger.Error(err, "Failed to remove namespace label",
			"namespace", ghr.Namespace,
			"hostname", ghr.Spec.Hostname)
	}

	// Step 4: Delete DNS validation records
	if ghr.Status.CertificateArn != "" {
		awsCtx, cancel := withAWSTimeout(ctx)
		validationRecords, err := r.ACMClient.GetValidationRecords(awsCtx, ghr.Status.CertificateArn)
		cancel()
		if err == nil {
			for _, vr := range validationRecords {
				record := aws.DNSRecord{
					Name:  vr.Name,
					Type:  vr.Type,
					Value: vr.Value,
					TTL:   300,
				}
				recordCtx, recordCancel := withAWSTimeout(ctx)
				err := r.Route53Client.DeleteRecord(recordCtx, ghr.Spec.ZoneId, record)
				recordCancel()
				if err != nil {
					logger.Error(err, "Failed to delete validation record",
						"name", vr.Name,
						"hostname", ghr.Spec.Hostname)
				}
			}
			logger.Info("Deleted DNS validation records", "hostname", ghr.Spec.Hostname)
		}
	}

	// Step 5: Check if certificate is still in use by ALB before deletion
	if ghr.Status.CertificateArn != "" {
		inUse, err := r.isCertificateInUse(ctx, ghr.Status.CertificateArn)
		if err != nil {
			logger.Error(err, "Failed to check certificate usage, continuing anyway",
				"arn", ghr.Status.CertificateArn,
				"hostname", ghr.Spec.Hostname)
			// Continue with deletion attempt - best effort
		} else if inUse {
			logger.Info("Certificate still in use by ALB, requeuing deletion",
				"arn", ghr.Status.CertificateArn,
				"hostname", ghr.Spec.Hostname)
			r.setCondition(ghr, ConditionTypeDeleting, metav1.ConditionTrue, "WaitingForCertDetachment",
				"Waiting for ALB to detach certificate")
			if err := r.Status().Update(ctx, ghr); err != nil {
				logger.Error(err, "Failed to update status")
			}
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}

		// Step 6: Delete ACM certificate (only after confirmed not in use)
		awsCtx, cancel := withAWSTimeout(ctx)
		err = r.ACMClient.DeleteCertificate(awsCtx, ghr.Status.CertificateArn)
		cancel()
		if err != nil {
			logger.Error(err, "Failed to delete ACM certificate",
				"arn", ghr.Status.CertificateArn,
				"hostname", ghr.Spec.Hostname)
		} else {
			logger.Info("Deleted ACM certificate", "arn", ghr.Status.CertificateArn)
		}
	}

	// Step 7: Release DomainClaim
	if err := r.deleteDomainClaim(ctx, ghr); err != nil {
		logger.Error(err, "Failed to delete domain claim")
	}

	// Step 7: Clean up Gateway if it's now empty (no other GHRs assigned)
	// Pass current GHR info so it can be excluded from assignment count
	if ghr.Status.AssignedGateway != "" && ghr.Status.AssignedGatewayNamespace != "" {
		if err := r.cleanupEmptyGateway(ctx, ghr.Status.AssignedGateway, ghr.Status.AssignedGatewayNamespace, ghr.Namespace, ghr.Name); err != nil {
			logger.Error(err, "Failed to cleanup empty gateway", "gateway", ghr.Status.AssignedGateway)
			// Gateway cleanup failure should block deletion - requeue to retry
			return ctrl.Result{}, err
		}
		// Clear assignment after successful cleanup
		ghr.Status.AssignedGateway = ""
		ghr.Status.AssignedGatewayNamespace = ""

		// Persist status changes before removing finalizer
		if err := r.Status().Update(ctx, ghr); err != nil {
			logger.Error(err, "Failed to update status after clearing assignment")
			return ctrl.Result{}, err
		}
	}

	// Step 8: Remove finalizer
	controllerutil.RemoveFinalizer(ghr, FinalizerName)
	if err := r.Update(ctx, ghr); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted GatewayHostnameRequest", "hostname", ghr.Spec.Hostname)
	return ctrl.Result{}, nil

}

// isCertificateInUse checks if the ACM certificate is still referenced by any resource (e.g., ALB listener)
func (r *GatewayHostnameRequestReconciler) isCertificateInUse(ctx context.Context, certArn string) (bool, error) {
	awsCtx, cancel := withAWSTimeout(ctx)
	defer cancel()

	details, err := r.ACMClient.DescribeCertificate(awsCtx, certArn)
	if err != nil {
		return false, err
	}
	return len(details.InUseBy) > 0, nil
}

// getALBHostedZoneId extracts the ALB hosted zone ID from the load balancer DNS name
func (r *GatewayHostnameRequestReconciler) getALBHostedZoneId(albDNS string) string {
	region, err := aws.ExtractRegionFromALBDNS(albDNS)
	if err != nil {
		return ""
	}
	zoneId, _ := aws.GetALBHostedZoneID(region)
	return zoneId
}

// validateRequest validates the GatewayHostnameRequest spec
func (r *GatewayHostnameRequestReconciler) validateRequest(ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	if ghr.Spec.ZoneId == "" {
		return fmt.Errorf("zoneId is required")
	}
	if ghr.Spec.Hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	// TODO: Add domain allowlist validation
	return nil
}

// setCondition sets a condition on the GatewayHostnameRequest status
func (r *GatewayHostnameRequestReconciler) setCondition(ghr *gatewayv1alpha1.GatewayHostnameRequest, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ghr.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ghr.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager
func (r *GatewayHostnameRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.GatewayHostnameRequest{}).
		Complete(r)
}

// computeSpecHash computes a hash of the spec fields that require re-provisioning when changed
func computeSpecHash(spec *gatewayv1alpha1.GatewayHostnameRequestSpec) string {
	// Hash hostname + zoneId + visibility + gatewayClass
	data := fmt.Sprintf("%s|%s|%s|%s", spec.Hostname, spec.ZoneId, spec.Visibility, spec.GatewayClass)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8]) // First 8 bytes is enough
}

// cleanupForReprovisioning removes resources created by the previous spec without removing the finalizer
// This is called when spec drift is detected to clean up before re-provisioning with new settings
func (r *GatewayHostnameRequestReconciler) cleanupForReprovisioning(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)
	logger.Info("Cleaning up resources for reprovisioning", "hostname", ghr.Spec.Hostname)

	// Step 1: Remove Route53 alias record
	if ghr.Status.AssignedLoadBalancer != "" {
		aliasRecord := aws.DNSRecord{
			Name: ghr.Spec.Hostname,
			Type: "A",
			AliasTarget: &aws.AliasTarget{
				DNSName:              ghr.Status.AssignedLoadBalancer,
				HostedZoneID:         r.getALBHostedZoneId(ghr.Status.AssignedLoadBalancer),
				EvaluateTargetHealth: true,
			},
		}
		awsCtx, cancel := withAWSTimeout(ctx)
		err := r.Route53Client.DeleteRecord(awsCtx, ghr.Spec.ZoneId, aliasRecord)
		cancel()
		if err != nil {
			logger.Error(err, "Failed to delete Route53 alias record during reprovisioning",
				"hostname", ghr.Spec.Hostname)
		} else {
			logger.Info("Deleted Route53 alias record during reprovisioning", "hostname", ghr.Spec.Hostname)
		}
	}

	// Step 2: Remove certificate ARN from Gateway annotation
	if ghr.Status.AssignedGateway != "" && ghr.Status.CertificateArn != "" {
		if err := r.removeCertificateFromGateway(ctx, ghr); err != nil {
			logger.Error(err, "Failed to remove certificate from gateway during reprovisioning",
				"gateway", ghr.Status.AssignedGateway)
		} else {
			logger.Info("Removed certificate from gateway during reprovisioning", "gateway", ghr.Status.AssignedGateway)
		}
	}

	// Step 3: Remove namespace label for gateway access
	if err := r.removeNamespaceLabel(ctx, ghr); err != nil {
		logger.Error(err, "Failed to remove namespace label during reprovisioning",
			"namespace", ghr.Namespace)
	}

	// Step 4: Delete DNS validation records
	if ghr.Status.CertificateArn != "" {
		awsCtx, cancel := withAWSTimeout(ctx)
		validationRecords, err := r.ACMClient.GetValidationRecords(awsCtx, ghr.Status.CertificateArn)
		cancel()
		if err == nil {
			for _, vr := range validationRecords {
				record := aws.DNSRecord{
					Name:  vr.Name,
					Type:  vr.Type,
					Value: vr.Value,
					TTL:   300,
				}
				recordCtx, recordCancel := withAWSTimeout(ctx)
				err := r.Route53Client.DeleteRecord(recordCtx, ghr.Spec.ZoneId, record)
				recordCancel()
				if err != nil {
					logger.Error(err, "Failed to delete validation record during reprovisioning",
						"name", vr.Name)
				}
			}
			logger.Info("Deleted DNS validation records during reprovisioning", "hostname", ghr.Spec.Hostname)
		}
	}

	// Step 5: Delete ACM certificate (best effort, may fail if still in use)
	if ghr.Status.CertificateArn != "" {
		awsCtx, cancel := withAWSTimeout(ctx)
		err := r.ACMClient.DeleteCertificate(awsCtx, ghr.Status.CertificateArn)
		cancel()
		if err != nil {
			logger.Error(err, "Failed to delete ACM certificate during reprovisioning (may still be in use)",
				"arn", ghr.Status.CertificateArn)
		} else {
			logger.Info("Deleted ACM certificate during reprovisioning", "arn", ghr.Status.CertificateArn)
		}
	}

	// Step 6: Release DomainClaim
	if err := r.deleteDomainClaim(ctx, ghr); err != nil {
		logger.Error(err, "Failed to delete domain claim during reprovisioning")
	} else {
		logger.Info("Released domain claim during reprovisioning")
	}

	return nil
}

// ensureGatewayConfiguration ensures Gateway and LoadBalancerConfiguration have correct settings
// This runs every reconciliation to correct configuration drift (idempotent)
func (r *GatewayHostnameRequestReconciler) ensureGatewayConfiguration(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)

	// Ensure LoadBalancerConfiguration is synced with current certificate list
	visibility := ghr.Spec.Visibility
	if visibility == "" {
		visibility = "internet-facing"
	}

	if err := r.syncLoadBalancerConfiguration(ctx, ghr.Status.AssignedGateway, ghr.Status.AssignedGatewayNamespace, visibility, ghr.Spec.WafArn, ghr.Status.CertificateArn); err != nil {
		logger.Info("Failed to sync LoadBalancerConfiguration", "error", err)
		return err
	}

	// Ensure Gateway has correct annotations
	var gw gwapiv1.Gateway
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ghr.Status.AssignedGateway,
		Namespace: ghr.Status.AssignedGatewayNamespace,
	}, &gw); err != nil {
		return fmt.Errorf("failed to get gateway: %w", err)
	}

	needsUpdate := false
	if gw.Annotations == nil {
		gw.Annotations = make(map[string]string)
	}

	// Ensure loadbalancer-configuration annotation
	configName := fmt.Sprintf("%s-config", ghr.Status.AssignedGateway)
	if gw.Annotations["gateway.k8s.aws/loadbalancer-configuration"] != configName {
		gw.Annotations["gateway.k8s.aws/loadbalancer-configuration"] = configName
		needsUpdate = true
	}

	// Ensure visibility annotation matches spec
	if gw.Annotations["gateway.opendi.com/visibility"] != visibility {
		gw.Annotations["gateway.opendi.com/visibility"] = visibility
		needsUpdate = true
	}

	// Ensure WAF annotation matches spec
	wafArn := ghr.Spec.WafArn
	if gw.Annotations["gateway.opendi.com/waf-arn"] != wafArn {
		gw.Annotations["gateway.opendi.com/waf-arn"] = wafArn
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Update(ctx, &gw); err != nil {
			return fmt.Errorf("failed to update gateway annotations: %w", err)
		}
		logger.Info("Updated Gateway annotations to correct drift")
	}

	return nil
}

// validateAssignedResources checks if assigned resources still exist and clears conditions if not
// This handles the case where resources are manually deleted outside the controller
func (r *GatewayHostnameRequestReconciler) validateAssignedResources(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	logger := log.FromContext(ctx)
	driftDetected := false

	// Check if assigned Gateway still exists
	if ghr.Status.AssignedGateway != "" && meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeListenerAttached) {
		var gw gwapiv1.Gateway
		err := r.Get(ctx, types.NamespacedName{
			Name:      ghr.Status.AssignedGateway,
			Namespace: ghr.Status.AssignedGatewayNamespace,
		}, &gw)
		if err != nil {
			if errors.IsNotFound(err) {
				logger.Info("Drift detected: Gateway no longer exists", "gateway", ghr.Status.AssignedGateway)
				r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "DriftDetected", "Gateway %s no longer exists", ghr.Status.AssignedGateway)
				// Clear conditions to trigger reassignment
				meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeListenerAttached)
				meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeDnsAliasReady)
				meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeReady)
				ghr.Status.AssignedGateway = ""
				ghr.Status.AssignedGatewayNamespace = ""
				ghr.Status.AssignedLoadBalancer = ""
				driftDetected = true
			}
		} else {
			// Gateway exists, check if LoadBalancerConfiguration exists
			lbcName := fmt.Sprintf("%s-config", ghr.Status.AssignedGateway)
			lbc := &unstructured.Unstructured{}
			lbc.SetGroupVersionKind(LoadBalancerConfigurationGVK)
			err = r.Get(ctx, types.NamespacedName{
				Name:      lbcName,
				Namespace: ghr.Status.AssignedGatewayNamespace,
			}, lbc)
			if err != nil && errors.IsNotFound(err) {
				logger.Info("Drift detected: LoadBalancerConfiguration no longer exists", "name", lbcName)
				r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "DriftDetected", "LoadBalancerConfiguration %s no longer exists", lbcName)
				// Clear condition to trigger recreation
				meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeListenerAttached)
				meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeDnsAliasReady)
				meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeReady)
				driftDetected = true
			}
		}
	}

	// Check if ACM certificate still exists
	if ghr.Status.CertificateArn != "" && meta.IsStatusConditionTrue(ghr.Status.Conditions, ConditionTypeCertificateIssued) {
		awsCtx, cancel := withAWSTimeout(ctx)
		certDetails, err := r.ACMClient.DescribeCertificate(awsCtx, ghr.Status.CertificateArn)
		cancel()
		if err != nil {
			logger.Info("Drift detected: ACM certificate no longer exists or is inaccessible",
				"arn", ghr.Status.CertificateArn,
				"error", err,
				"hostname", ghr.Spec.Hostname)
			r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "DriftDetected", "ACM certificate %s no longer exists", ghr.Status.CertificateArn)
			// Clear conditions to trigger recreation
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeCertificateIssued)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeDnsValidated)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeListenerAttached)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeDnsAliasReady)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeReady)
			ghr.Status.CertificateArn = ""
			driftDetected = true
		} else if certDetails.Status == "FAILED" || certDetails.Status == "REVOKED" {
			logger.Info("Drift detected: ACM certificate in bad state", "arn", ghr.Status.CertificateArn, "status", certDetails.Status)
			r.Recorder.Eventf(ghr, corev1.EventTypeWarning, "CertificateFailed", "ACM certificate is in %s state", certDetails.Status)
			// Clear conditions to trigger recreation
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeCertificateIssued)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeDnsValidated)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeListenerAttached)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeDnsAliasReady)
			meta.RemoveStatusCondition(&ghr.Status.Conditions, ConditionTypeReady)
			ghr.Status.CertificateArn = ""
			driftDetected = true
		}
	}

	// If drift detected, update status to trigger re-reconciliation
	if driftDetected {
		if err := r.Status().Update(ctx, ghr); err != nil {
			return fmt.Errorf("failed to update status after drift detection: %w", err)
		}
		logger.Info("Drift fixed, re-reconciliation will occur")
	}

	return nil
}
