package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gatewayv1alpha1 "github.com/michelfeldheim/gateway-orchestrator/api/v1alpha1"
)

func TestGenerateClaimName(t *testing.T) {
	tests := []struct {
		name     string
		zoneId   string
		hostname string
		want     string
	}{
		{
			name:     "simple hostname",
			zoneId:   "Z123456",
			hostname: "test.example.com",
			want:     "Z123456-test.example.com",
		},
		{
			name:     "subdomain",
			zoneId:   "Z789012",
			hostname: "api.staging.example.com",
			want:     "Z789012-api.staging.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateClaimName(tt.zoneId, tt.hostname)
			if got != tt.want {
				t.Errorf("generateClaimName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconciler_ensureDomainClaim(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	tests := []struct {
		name           string
		ghr            *gatewayv1alpha1.GatewayHostnameRequest
		existingClaim  *gatewayv1alpha1.DomainClaim
		wantClaimed    bool
		wantErr        bool
	}{
		{
			name: "no existing claim - should succeed",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-request",
					Namespace: "default",
					UID:       "uid-123",
				},
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
					ZoneId:   "Z123456",
					Hostname: "test.example.com",
				},
			},
			existingClaim: nil,
			wantClaimed:   true,
			wantErr:       false,
		},
		{
			name: "claim owned by same request - should succeed",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-request",
					Namespace: "default",
					UID:       "uid-123",
				},
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
					ZoneId:   "Z123456",
					Hostname: "test.example.com",
				},
			},
			existingClaim: &gatewayv1alpha1.DomainClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "Z123456-test.example.com",
				},
				Spec: gatewayv1alpha1.DomainClaimSpec{
					ZoneId:   "Z123456",
					Hostname: "test.example.com",
					OwnerRef: gatewayv1alpha1.DomainClaimOwnerRef{
						Namespace: "default",
						Name:      "test-request",
						UID:       "uid-123",
					},
				},
			},
			wantClaimed: true,
			wantErr:     false,
		},
		{
			name: "claim owned by different request - should fail",
			ghr: &gatewayv1alpha1.GatewayHostnameRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-request",
					Namespace: "default",
					UID:       "uid-123",
				},
				Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
					ZoneId:   "Z123456",
					Hostname: "test.example.com",
				},
			},
			existingClaim: &gatewayv1alpha1.DomainClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "Z123456-test.example.com",
				},
				Spec: gatewayv1alpha1.DomainClaimSpec{
					ZoneId:   "Z123456",
					Hostname: "test.example.com",
					OwnerRef: gatewayv1alpha1.DomainClaimOwnerRef{
						Namespace: "other-namespace",
						Name:      "other-request",
						UID:       "uid-456",
					},
				},
			},
			wantClaimed: false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.existingClaim != nil {
				objs = append(objs, tt.existingClaim)
			}

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			r := &GatewayHostnameRequestReconciler{
				Client: client,
				Scheme: scheme,
			}

			ctx := context.Background()
			claimed, err := r.ensureDomainClaim(ctx, tt.ghr)

			if (err != nil) != tt.wantErr {
				t.Errorf("ensureDomainClaim() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if claimed != tt.wantClaimed {
				t.Errorf("ensureDomainClaim() claimed = %v, want %v", claimed, tt.wantClaimed)
			}

			// If claim was successful, verify it exists
			if claimed && !tt.wantErr {
				var claim gatewayv1alpha1.DomainClaim
				claimName := generateClaimName(tt.ghr.Spec.ZoneId, tt.ghr.Spec.Hostname)
				err := client.Get(ctx, types.NamespacedName{Name: claimName}, &claim)
				if err != nil {
					t.Errorf("claim should exist but got error: %v", err)
				}
			}
		})
	}
}

func TestReconciler_deleteDomainClaim(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gatewayv1alpha1.AddToScheme(scheme)

	ghr := &gatewayv1alpha1.GatewayHostnameRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
			UID:       "uid-123",
		},
		Spec: gatewayv1alpha1.GatewayHostnameRequestSpec{
			ZoneId:   "Z123456",
			Hostname: "test.example.com",
		},
	}

	claim := &gatewayv1alpha1.DomainClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "Z123456-test.example.com",
		},
		Spec: gatewayv1alpha1.DomainClaimSpec{
			ZoneId:   "Z123456",
			Hostname: "test.example.com",
			OwnerRef: gatewayv1alpha1.DomainClaimOwnerRef{
				Namespace: "default",
				Name:      "test-request",
				UID:       "uid-123",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(claim).
		Build()

	r := &GatewayHostnameRequestReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()

	// Verify claim exists before delete
	var beforeDelete gatewayv1alpha1.DomainClaim
	err := client.Get(ctx, types.NamespacedName{Name: claim.Name}, &beforeDelete)
	if err != nil {
		t.Fatalf("claim should exist before delete: %v", err)
	}

	// Delete claim
	err = r.deleteDomainClaim(ctx, ghr)
	if err != nil {
		t.Fatalf("deleteDomainClaim() error = %v", err)
	}

	// Verify claim is deleted
	var afterDelete gatewayv1alpha1.DomainClaim
	err = client.Get(ctx, types.NamespacedName{Name: claim.Name}, &afterDelete)
	if err == nil {
		t.Error("claim should be deleted but still exists")
	}
}
