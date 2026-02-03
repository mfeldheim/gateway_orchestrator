package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

	// Step 8: Configure allowedRoutes
	if err := r.ensureAllowedRoutes(ctx, ghr); err != nil {
		logger.Info("Failed to configure allowedRoutes, continuing anyway", "error", err.Error())
		// Don't fail reconciliation for this, just log it
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

	// Cleanup steps (best-effort, log errors but continue)

	// 1. Remove Route53 alias record
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
		if err := r.Route53Client.DeleteRecord(ctx, ghr.Spec.ZoneId, aliasRecord); err != nil {
			logger.Error(err, "Failed to delete Route53 alias record", "hostname", ghr.Spec.Hostname)
		} else {
			logger.Info("Deleted Route53 alias record", "hostname", ghr.Spec.Hostname)
		}
	}

	// 2. Remove certificate ARN from Gateway annotation
	if ghr.Status.AssignedGateway != "" && ghr.Status.CertificateArn != "" {
		if err := r.removeCertificateFromGateway(ctx, ghr); err != nil {
			logger.Error(err, "Failed to remove certificate from gateway", "gateway", ghr.Status.AssignedGateway)
		} else {
			logger.Info("Removed certificate from gateway", "gateway", ghr.Status.AssignedGateway)
		}
	}

	// 3. Delete DNS validation records
	if ghr.Status.CertificateArn != "" {
		validationRecords, err := r.ACMClient.GetValidationRecords(ctx, ghr.Status.CertificateArn)
		if err == nil {
			for _, vr := range validationRecords {
				record := aws.DNSRecord{
					Name:  vr.Name,
					Type:  vr.Type,
					Value: vr.Value,
					TTL:   300,
				}
				if err := r.Route53Client.DeleteRecord(ctx, ghr.Spec.ZoneId, record); err != nil {
					logger.Error(err, "Failed to delete validation record", "name", vr.Name)
				}
			}
			logger.Info("Deleted DNS validation records")
		}
	}

	// 4. Delete ACM certificate
	if ghr.Status.CertificateArn != "" {
		if err := r.ACMClient.DeleteCertificate(ctx, ghr.Status.CertificateArn); err != nil {
			logger.Error(err, "Failed to delete ACM certificate", "arn", ghr.Status.CertificateArn)
		} else {
			logger.Info("Deleted ACM certificate", "arn", ghr.Status.CertificateArn)
		}
	}

	// 5. Release DomainClaim
	if err := r.deleteDomainClaim(ctx, ghr); err != nil {
		logger.Error(err, "Failed to delete domain claim")
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(ghr, FinalizerName)
	if err := r.Update(ctx, ghr); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted GatewayHostnameRequest", "hostname", ghr.Spec.Hostname)
	return ctrl.Result{}, nil
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
		if err := r.Route53Client.DeleteRecord(ctx, ghr.Spec.ZoneId, aliasRecord); err != nil {
			logger.Error(err, "Failed to delete Route53 alias record during reprovisioning", "hostname", ghr.Spec.Hostname)
		} else {
			logger.Info("Deleted Route53 alias record during reprovisioning", "hostname", ghr.Spec.Hostname)
		}
	}

	// Step 2: Remove certificate ARN from Gateway annotation
	if ghr.Status.AssignedGateway != "" && ghr.Status.CertificateArn != "" {
		if err := r.removeCertificateFromGateway(ctx, ghr); err != nil {
			logger.Error(err, "Failed to remove certificate from gateway during reprovisioning")
		} else {
			logger.Info("Removed certificate from gateway during reprovisioning", "gateway", ghr.Status.AssignedGateway)
		}
	}

	// Step 3: Delete DNS validation records
	if ghr.Status.CertificateArn != "" {
		validationRecords, err := r.ACMClient.GetValidationRecords(ctx, ghr.Status.CertificateArn)
		if err == nil {
			for _, vr := range validationRecords {
				record := aws.DNSRecord{
					Name:  vr.Name,
					Type:  vr.Type,
					Value: vr.Value,
					TTL:   300,
				}
				if err := r.Route53Client.DeleteRecord(ctx, ghr.Spec.ZoneId, record); err != nil {
					logger.Error(err, "Failed to delete validation record during reprovisioning", "name", vr.Name)
				}
			}
			logger.Info("Deleted DNS validation records during reprovisioning")
		}
	}

	// Step 4: Delete ACM certificate (best effort, may fail if still in use)
	if ghr.Status.CertificateArn != "" {
		if err := r.ACMClient.DeleteCertificate(ctx, ghr.Status.CertificateArn); err != nil {
			logger.Error(err, "Failed to delete ACM certificate during reprovisioning (may still be in use)", "arn", ghr.Status.CertificateArn)
		} else {
			logger.Info("Deleted ACM certificate during reprovisioning", "arn", ghr.Status.CertificateArn)
		}
	}

	// Step 5: Release DomainClaim
	if err := r.deleteDomainClaim(ctx, ghr); err != nil {
		logger.Error(err, "Failed to delete domain claim during reprovisioning")
	} else {
		logger.Info("Released domain claim during reprovisioning")
	}

	return nil
}
