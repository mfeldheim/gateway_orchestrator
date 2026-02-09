package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
)

// ensureDomainClaim ensures a DomainClaim exists for this hostname
// Returns true if claim is owned by this request, false if claimed by another
func (r *GatewayHostnameRequestReconciler) ensureDomainClaim(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) (bool, error) {
	claimName := generateClaimName(ghr.Spec.ZoneId, ghr.Spec.Hostname)

	var claim gatewayv1alpha1.DomainClaim
	err := r.Get(ctx, types.NamespacedName{Name: claimName}, &claim)

	if err == nil {
		// Claim exists, check if it's owned by this request
		if claim.Spec.OwnerRef.Namespace == ghr.Namespace &&
			claim.Spec.OwnerRef.Name == ghr.Name &&
			claim.Spec.OwnerRef.UID == string(ghr.UID) {
			return true, nil // Already owned by this request
		}
		// Claimed by someone else
		return false, nil
	}

	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("failed to get domain claim: %w", err)
	}

	// Claim doesn't exist, create it
	now := metav1.Now()
	claim = gatewayv1alpha1.DomainClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: claimName,
		},
		Spec: gatewayv1alpha1.DomainClaimSpec{
			ZoneId:   ghr.Spec.ZoneId,
			Hostname: ghr.Spec.Hostname,
			OwnerRef: gatewayv1alpha1.DomainClaimOwnerRef{
				Namespace: ghr.Namespace,
				Name:      ghr.Name,
				UID:       string(ghr.UID),
			},
		},
		Status: gatewayv1alpha1.DomainClaimStatus{
			ClaimedAt: &now,
		},
	}

	if err := r.Create(ctx, &claim); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race condition: someone else created it between our Get and Create
			return false, nil
		}
		return false, fmt.Errorf("failed to create domain claim: %w", err)
	}

	return true, nil
}

// deleteDomainClaim deletes the DomainClaim owned by this request
func (r *GatewayHostnameRequestReconciler) deleteDomainClaim(ctx context.Context, ghr *gatewayv1alpha1.GatewayHostnameRequest) error {
	claimName := generateClaimName(ghr.Spec.ZoneId, ghr.Spec.Hostname)

	var claim gatewayv1alpha1.DomainClaim
	err := r.Get(ctx, types.NamespacedName{Name: claimName}, &claim)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Already deleted
		}
		return err
	}

	// Only delete if owned by this request
	if claim.Spec.OwnerRef.Namespace == ghr.Namespace &&
		claim.Spec.OwnerRef.Name == ghr.Name &&
		claim.Spec.OwnerRef.UID == string(ghr.UID) {
		return client.IgnoreNotFound(r.Delete(ctx, &claim))
	}

	return nil
}

// generateClaimName creates a deterministic name for a DomainClaim
func generateClaimName(zoneId, hostname string) string {
	// Sanitize hostname: replace * with 'wildcard' for valid K8s name
	sanitized := strings.ReplaceAll(hostname, "*", "wildcard")
	// Use a simple naming scheme: zone-hostname
	// In production, might want to hash long names
	return fmt.Sprintf("%s-%s", strings.ToLower(zoneId), strings.ToLower(sanitized))
}
